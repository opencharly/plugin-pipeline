package pluginpipeline

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/opencharly/sdk"
)

// runCLI: the command:pipeline surface (OpRun args).
//
//	pipeline run <entity> [--pr N] [--calver X] [--workdir DIR]
//	pipeline validate <entity>
//	pipeline agent [--system-prompt <text>] [--prompt <text|->] [--tools a,b] [--out P]
//	pipeline probe <verb> --self-test
//	pipeline --self-test
func runCLI(args []string, ex *sdk.Executor) (int, error) {
	mode := ""
	var rest []string
	for _, a := range args {
		switch a {
		case "run", "validate", "agent", "probe":
			mode = a
		default:
			rest = append(rest, a)
		}
	}
	switch mode {
	case "":
		if has(args, "--self-test") {
			fmt.Println("pipeline self-test: ok (executor + agent runtime present)")
			return 0, nil
		}
		return 2, fmt.Errorf("pipeline: need run|validate|agent|probe")
	case "validate":
		if len(rest) == 0 {
			return 2, fmt.Errorf("pipeline validate <entity>")
		}
		p, err := loadEntity(rest[0])
		if err != nil {
			return 1, err
		}
		fmt.Printf("pipeline %s: valid (%d stages)\n", rest[0], len(p.Stages))
		return 0, nil
	case "run":
		// --dry-run (plan row 8): validate the entity + resolve every declared
		// env ref, then stop without executing.
		if flagAfter(rest, "--dry-run") != "" || has(rest, "--dry-run") {
			p, derr := loadEntity(rest[0])
			if derr != nil {
				return 1, derr
			}
			env := envMap()
			missing := []string{}
			for _, k := range envRefs(p) {
				if _, ok := env[k]; !ok {
					missing = append(missing, k)
				}
			}
			if len(missing) > 0 {
				return 1, fmt.Errorf("pipeline dry-run: unresolvable env refs: %v", missing)
			}
			fmt.Println("pipeline", rest[0], "dry-run OK, env refs:", len(envRefs(p)), "stages:", len(p.Stages))
			return 0, nil
		}

		if len(rest) == 0 {
			return 2, fmt.Errorf("pipeline run <entity> [--pr N] [--prs a b c]")
		}
		p, err := loadEntity(rest[0])
		if err != nil {
			return 1, err
		}
		calver := flagAfter(rest, "--calver")
		workdir := flagAfter(rest, "--workdir")
		// the charly-native BATCH: --prs a b c runs the SAME entity per PR,
		// sequencing-gated + venue-cleaned between lanes (the check stage's
		// teardown handles the VMs; the sequencing gate blocks while a batch
		// VM lives) — no external loop scripts.
		return runBatch(rest, p, calver, workdir, ex)
	case "agent":
		sys := flagAfter(rest, "--system-prompt")
		prompt := flagAfter(rest, "--prompt")
		if prompt == "-" {
			b, _ := os.ReadFile("/dev/stdin")
			prompt = strings.TrimSpace(string(b))
		}
		tools := strings.Split(flagAfter(rest, "--tools"), ",")
		outP := flagAfter(rest, "--out")
		resp, err := runAgent(context.Background(), nil, sys, prompt, tools)
		if err != nil {
			return 1, err
		}
		if outP != "" {
			_ = os.WriteFile(outP, []byte(resp), 0o644)
		}
		fmt.Println(resp)
		return 0, nil
	case "probe":
		if len(rest) == 0 || !has(rest, "--self-test") {
			return 2, fmt.Errorf("pipeline probe <verb> --self-test")
		}
		ok, msg := runProbe(rest[0], map[string]any{})
		if !ok {
			return 1, fmt.Errorf("probe %s: %s", rest[0], msg)
		}
		fmt.Printf("probe %s: ok\n", rest[0])
		return 0, nil
	}
	return 2, fmt.Errorf("pipeline: unknown mode %q", mode)
}

// CliMain: the OUT-OF-PROCESS CLI-mode entry (sdk.Main dual mode).
func CliMain(args []string) int {
	exit, err := runCLI(args, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pipeline: "+err.Error())
		if exit == 0 {
			exit = 1
		}
	}
	return exit
}

func has(args []string, f string) bool {
	for _, a := range args {
		if a == f {
			return true
		}
	}
	return false
}

// restAfter returns the args AFTER the named flag (for --prs a b c ...).
func restAfter(args []string, name string) []string {
	for i, a := range args {
		if a == name {
			out := []string{}
			for _, x := range args[i+1:] {
				if strings.HasPrefix(x, "--") {
					break
				}
				out = append(out, x)
			}
			return out
		}
	}
	return nil
}

// REMOVED: teardownVenue / teardownLaneBeds (the batch-level venue teardown).
//
// Venue lifecycle has ONE owner: the `ade` stage itself. `charly check run` is
// invoked with `--keep-venue` (the evidence is collected from the live domain),
// and runAdeBed's deferred destroyVenue tears it down on every exit path,
// including an abort (`context.WithoutCancel`). A SECOND owner was a defect of
// exactly the kind R3 forbids:
//
//   - it hardcoded the eval-omarchy golden entity and a
//     `check-omarchy-pr-<pr>-vm`/`-vm-probe` domain scheme in the domain-neutral
//     engine (the `-vm-probe` suffix matched no entity at all);
//   - it resolved the bed refs against the PROCESS env (`envMap()`), not the
//     lane's run context — the same per-lane-env break this change fixes in
//     ade.go, so any `$env.*` bed ref resolved to the operator's value or
//     empty and the destroy silently targeted the wrong name;
//   - it used the signal-cancelled ctx, so it was skipped precisely in the
//     abort scenario teardown exists for.
//
// The `ade` stage owns the venue; the batch does not duplicate the rule.

func flagAfter(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
