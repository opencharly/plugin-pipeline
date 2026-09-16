package pluginpipeline

// batch.go — the charly-native BATCH drive with the org's PARALLEL-LANE model:
// the entity's concurrency.lanes bounds a worker pool over the PR list (the
// per-PR beds are disjoint; only the golden capture is exclusive, which the
// sequencing gate scopes to). No external loop scripts.

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk"
)

func runBatch(rest []string, p params.PipelineInput, calver, workdir string, ex *sdk.Executor) (int, error) {
	// A KILLED engine must not orphan its child check runs.
	//
	// The lanes drive child processes (`charly check run` via the ADE kit's
	// executor) that hold the per-bed flock and keep a VM alive. The predecessor
	// passed `context.Background()` and installed NO signal handler, so a
	// SIGINT/SIGTERM (or a tool timeout) killed only the engine: the child kept
	// the flock, and every subsequent lane died on
	// `check bed … already running in this project — refusing a concurrent run`.
	// That is the stale-lock cascade observed live. Signal.NotifyContext cancels
	// the lane contexts, which the child `exec.CommandContext` calls turn into a
	// teardown, so the flock is released by the dying child rather than leaked.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	prs := restAfter(rest, "--prs")
	if len(prs) == 0 {
		if pr := flagAfter(rest, "--pr"); pr != "" {
			prs = []string{pr}
		}
	}
	if len(prs) == 0 {
		// a generic single-entity run with no PR dimension (a non-eval plan)
		prs = []string{""}
	}
	lanes := int(p.Concurrency.Lanes)
	if lanes < 1 {
		lanes = 1
	}
	if lf := flagAfter(rest, "--lanes"); lf != "" {
		if n, e := parseInt(lf); e == nil && n > 0 {
			lanes = n
		}
	}
	jobs := make(chan string, len(prs))
	var wg sync.WaitGroup
	// Lane outcomes are AGGREGATED, not just printed: the batch exit code is a
	// GATE (R7). The predecessor printed each lane's error and then returned
	// `0, nil` unconditionally, so a batch whose every lane FAILED still exited
	// 0 and printed "OK" — a caller (and CI) could not tell success from total
	// failure. Live-caught: a lane ending in LOOP-GUARD escalate, followed by
	// "pipeline eval-pr-plan: OK".
	rep := &laneReport{}
	for i := 0; i < lanes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for one := range jobs {
				// the lane identity rides the runPlan arg — runPlan binds it to the
				// run context per lane. os.Setenv here was PROCESS-GLOBAL and raced
				// the concurrent lanes (RCA 2026.252.2210).
				fmt.Printf("== lane %s ==\n", one)
				lerr := runPlan(ctx, p, one, calver, workdir, ex)
				rep.record(one, lerr)
				// per-lane venue hygiene (its own VM beds, by declared entity name)
				teardownLaneBeds(ctx, p, one, calver, workdir)
			}
		}()
	}
	for _, one := range prs {
		jobs <- one
	}
	close(jobs)
	wg.Wait()
	return rep.exit(rest[0], lanes)
}

// laneReport aggregates the per-lane outcomes of a batch run. A batch is a
// GATE: its exit status must reflect whether every lane succeeded, and the
// concurrent lanes make the counters mutex-guarded.
type laneReport struct {
	mu       sync.Mutex
	done     int
	failed   int
	failures []string
}

func (r *laneReport) record(lane string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done++
	if err != nil {
		r.failed++
		fmt.Printf("lane %s: FAILED (%v)\n", lane, err)
		r.failures = append(r.failures, fmt.Sprintf("%s: %v", lane, err))
		return
	}
	fmt.Printf("lane %s: done\n", lane)
}

// exit renders the honest summary and maps the aggregate to the process exit
// code: ANY failed lane is a non-zero exit, so a batch can never be read as
// success when a lane ended in FAIL-HARD or the LOOP-GUARD escalation.
func (r *laneReport) exit(name string, lanes int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed > 0 {
		fmt.Printf("pipeline %s: FAILED (%d/%d lanes failed, pool=%d)\n", name, r.failed, r.done, lanes)
		return 1, fmt.Errorf("pipeline %s: %d/%d lanes failed: %s", name, r.failed, r.done, strings.Join(r.failures, "; "))
	}
	fmt.Printf("pipeline %s: OK (lanes=%d, pool=%d)\n", name, r.done, lanes)
	return 0, nil
}
