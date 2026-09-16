package pluginpipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
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

// teardownLaneBeds: the between-lane venue hygiene — destroy the VM beds the
// lane's own `ade` stages declared, by ENTITY NAME, best-effort.
//
// This is deliberately ENTITY-DRIVEN (R3 + the kernel/plugin boundary law). The
// predecessor `teardownVenue` hardcoded the eval-omarchy golden
// (`check-omarchy-eval-base-inst`) and a domain suffix scheme
// (`check-omarchy-pr-<pr>-vm`, plus a `-vm-probe` suffix that no entity has) —
// a lane's private layout baked into the generic engine, which also meant every
// unrelated pipeline got an eval-omarchy-shaped teardown. The bed names are
// already declared in the stages, so they are read from there and ref-resolved.
//
// `--if-exists` makes an already-absent venue a SUCCESS: without it, every clean
// lane logged a spurious `no such VM … nothing destroyed`.
func teardownLaneBeds(ctx context.Context, p params.PipelineInput, pr, calver, workdir string) {
	charlyBin := os.Getenv("CHARLY_BIN")
	if charlyBin == "" {
		charlyBin = "charly"
	}
	rc := &runCtx{pr: pr, calver: calver, workdir: workdir, env: envMap()}
	seen := map[string]bool{}
	for _, raw := range p.Stages {
		if asString(raw["kind"]) != "ade" {
			continue
		}
		bed := strings.TrimSpace(rc.resolveRefs(asString(raw["bed"])))
		if bed == "" || seen[bed] {
			continue
		}
		seen[bed] = true
		args := []string{"vm", "destroy", bed, "--if-exists"}
		if workdir != "" {
			args = append([]string{"-C", workdir}, args...)
		}
		cmd := exec.CommandContext(ctx, charlyBin, args...)
		cmd.Env = os.Environ()
		_, _ = cmd.CombinedOutput()
	}
}

func flagAfter(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
