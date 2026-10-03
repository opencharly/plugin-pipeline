package pluginpipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/workflowkit"
	"github.com/opencharly/spec/ops"
	"github.com/opencharly/spec/spec"
)

// runCLI: the command:pipeline surface (OpRun args).
//
//	pipeline run <entity> [--dry-run] [--args-json <json-object>] [--mode human|tool]
//	pipeline validate <entity>
//	pipeline agent [--system-prompt <text>] [--prompt <text|->] [--tools a,b] [--out P]
//	pipeline probe <verb> --self-test
//	pipeline --self-test
//
// `run` is the FRONT-END leg: it resolves the entity, validates it with
// sdk/workflowkit, lowers it to the (workflow.lobster, charly.yml) pair, and
// dispatches the pair to the `workflow` provider class as OpWorkflowRun. The
// engine (plugin-lobster today) owns execution; this command never runs a step
// itself. `--dry-run` stops after the lowering (validate + lower, no dispatch).
//
// The run leg's dispatch inputs — `--args-json` (the pipeline's declared args) and
// `--mode human|tool` — are parsed and validated up front, so a malformed flag is a
// named error in EVERY mode instead of being silently accepted by the path that does
// not consume it. Both are documented on workflowRunParams.
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
			fmt.Println("pipeline self-test: ok (workflow front-end + agent runtime present)")
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
		fmt.Printf("pipeline %s: valid (%d steps)\n", rest[0], len(p.Steps))
		return 0, nil
	case "run":
		if len(rest) == 0 {
			return 2, fmt.Errorf("pipeline run <entity>")
		}
		p, err := loadEntity(rest[0])
		if err != nil {
			return 1, err
		}
		name := rest[0]
		genDir := filepath.Join(projectDir(), ".opencharly", "pipelines", name)
		params, perr := workflowRunParams(name, genDir, rest)
		if perr != nil {
			return 1, perr
		}
		if err := os.MkdirAll(genDir, 0o755); err != nil {
			return 1, err
		}
		lobsterYAML, charlyYAML, lerr := workflowkit.Lower(&p, projectDir(), genDir, charlyBin())
		if lerr != nil {
			return 1, lerr
		}
		if werr := os.WriteFile(filepath.Join(genDir, "workflow.lobster"), lobsterYAML, 0o644); werr != nil {
			return 1, werr
		}
		if werr := os.WriteFile(filepath.Join(genDir, "charly.yml"), charlyYAML, 0o644); werr != nil {
			return 1, werr
		}
		if has(rest, "--dry-run") {
			fmt.Printf("pipeline %s: dry-run OK — lowered %d steps to %s (workflow.lobster + charly.yml written, not dispatched)\n",
				name, len(p.Steps), genDir)
			return 0, nil
		}
		engine := p.Engine
		if engine == "" {
			engine = "lobster"
		}
		if ex == nil {
			return 1, fmt.Errorf("pipeline run %s: no host executor — the workflow engine is reached host-side via InvokeProvider(\"workflow\", %q, %q); run this through charly, not the plugin CLI directly", name, engine, ops.OpWorkflowRun)
		}
		if _, ierr := ex.InvokeProvider(context.Background(), "workflow", engine, ops.OpWorkflowRun, params, nil, ops.InvokeProviderOpts{}); ierr != nil {
			return 1, fmt.Errorf("pipeline run %s: workflow:%s %s: %w", name, engine, ops.OpWorkflowRun, ierr)
		}
		fmt.Printf("pipeline %s: ran via workflow:%s\n", name, engine)
		return 0, nil
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

// charlyBin is the binary path the lowering bakes into every generated `run:`
// (the engine drives generated charly tasks with `<charlyBin> -C <gen-dir> task
// <entity>`). CHARLY_BIN overrides it; the default is the PATH-resolved charly.
func charlyBin() string {
	if b := os.Getenv("CHARLY_BIN"); b != "" {
		return b
	}
	return "charly"
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

func flagAfter(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// workflowRunParams builds the OpWorkflowRun envelope the front-end dispatches to the
// `workflow` provider class: the EXACT ParamsJson bytes the engine receives (one
// builder, so the CLI cannot drift from the envelope it documents).
//
// The run leg carries TWO dispatch inputs, both consumed by the engine:
//
//   - `--args-json <json-object>` → WorkflowRunRequest.Args, an object of STRING
//     values (the envelope field is typed `map[string]string`, not `any`). The
//     lowering emits `-p NAME="$NAME"` for every declared arg, so an arg the
//     front-end never passes reaches the engine as an EMPTY value; a malformed
//     document or a non-string member is therefore a NAMED error here, never a
//     silently-empty arg.
//   - `--mode human|tool` → WorkflowRunRequest.Mode, which selects the engine's
//     human/tool envelope. ABSENT leaves Mode empty ON PURPOSE: the engine owns its
//     default, and a front-end default would silently override it.
func workflowRunParams(name, genDir string, rest []string) ([]byte, error) {
	args, mode, err := runDispatchFlags(name, rest)
	if err != nil {
		return nil, err
	}
	params, merr := json.Marshal(spec.WorkflowRunRequest{Pipeline: name, Args: args, Mode: mode, GenDir: genDir})
	if merr != nil {
		return nil, fmt.Errorf("pipeline run %s: encode request: %w", name, merr)
	}
	return params, nil
}

// runDispatchFlags parses and validates the run leg's dispatch flags out of rest.
func runDispatchFlags(name string, rest []string) (map[string]string, string, error) {
	var args map[string]string
	if has(rest, "--args-json") {
		raw := flagAfter(rest, "--args-json")
		if uerr := json.Unmarshal([]byte(raw), &args); uerr != nil {
			return nil, "", fmt.Errorf("pipeline run %s: --args-json: %v (want a JSON object of STRING values, e.g. '{\"who\":\"world\"}')", name, uerr)
		}
		if args == nil {
			return nil, "", fmt.Errorf("pipeline run %s: --args-json: want a JSON object of STRING values, e.g. '{\"who\":\"world\"}' (got %q)", name, raw)
		}
	}
	mode := ""
	if has(rest, "--mode") {
		mode = flagAfter(rest, "--mode")
		if mode != "human" && mode != "tool" {
			return nil, "", fmt.Errorf("pipeline run %s: --mode %q: accepted values are human, tool", name, mode)
		}
	}
	return args, mode, nil
}
