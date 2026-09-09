package pluginpipeline

// batch.go — the charly-native BATCH drive with the org's PARALLEL-LANE model:
// the entity's concurrency.lanes bounds a worker pool over the PR list (the
// per-PR beds are disjoint; only the golden capture is exclusive, which the
// sequencing gate scopes to). No external loop scripts.

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk"
)

func runBatch(rest []string, p params.PipelineInput, calver, workdir string, ex *sdk.Executor) (int, error) {
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
	for i := 0; i < lanes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for one := range jobs {
				_ = os.Setenv("PR_NUMBER", one)
				_ = os.Setenv("PR_HEAD_SHA", headSHA(one))
				fmt.Printf("== lane %s ==\n", one)
				if err := runPlan(context.Background(), p, one, calver, workdir, ex); err != nil {
					fmt.Printf("lane %s: ended (%v)\n", one, err)
				} else {
					fmt.Printf("lane %s: done\n", one)
				}
				// per-lane venue hygiene (its own VMs)
				_ = teardownVenue(one, workdir)
			}
		}()
	}
	for _, one := range prs {
		jobs <- one
	}
	close(jobs)
	wg.Wait()
	fmt.Printf("pipeline %s: OK (lanes=%d, pool=%d)\n", rest[0], len(prs), lanes)
	return 0, nil
}
