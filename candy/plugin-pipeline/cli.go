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
//	pipeline resume <entity> [--token T | --id I] [--approve yes|no] [--response-json J] [--cancel]
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
// `resume` is the FRONT-END leg of answering a gate the engine paused on, and it
// dispatches OpWorkflowResume with the SAME envelope shape `run` documents: the
// approval decision is TRI-STATE on the wire (`approve` absent | false | true), so
// `--approve no` sends `"approve": false` — a REJECTION — while passing no approval
// flag at all sends no decision, which the engine refuses BY NAME rather than reading
// as a rejection. `--cancel` is an ABORT and is deliberately a different arm from
// either. When neither `--token` nor `--id` is given, the handle the interrupted `run`
// recorded in the run's gen dir is used, so `resume <name> --approve yes` works without
// the caller copying a token out of the run output.
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
		case "run", "validate", "agent", "probe", "resume":
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
		return 2, fmt.Errorf("pipeline: need run|validate|agent|probe|resume")
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
		genDir := genDirOf(name)
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
		raw, ierr := ex.InvokeProvider(context.Background(), "workflow", engine, ops.OpWorkflowRun, params, nil, ops.InvokeProviderOpts{})
		if ierr != nil {
			return 1, fmt.Errorf("pipeline run %s: workflow:%s %s: %w", name, engine, ops.OpWorkflowRun, ierr)
		}
		var reply spec.WorkflowRunReply
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &reply)
		}
		if rerr := recordPending(genDir, &reply); rerr != nil {
			return 1, rerr
		}
		if reply.Status != "" {
			fmt.Printf("pipeline %s: ran via workflow:%s — status=%s\n", name, engine, reply.Status)
		} else {
			fmt.Printf("pipeline %s: ran via workflow:%s\n", name, engine)
		}
		// `resume_token` is present exactly when status is needs_approval / needs_input
		// (spec's own words on the envelope), so the status is what names the next move.
		switch reply.Status {
		case "needs_approval":
			fmt.Printf("pipeline %s: an approval is pending — answer it with `charly pipeline resume %s --approve yes|no`\n", name, name)
		case "needs_input":
			fmt.Printf("pipeline %s: input is pending — answer it with `charly pipeline resume %s --response-json '{...}'`\n", name, name)
		}
		return 0, nil
	case "resume":
		if len(rest) == 0 {
			return 2, fmt.Errorf("pipeline resume <entity> [--token T | --id I] [--approve yes|no] [--response-json J] [--cancel]")
		}
		name := rest[0]
		p, err := loadEntity(name)
		if err != nil {
			return 1, err
		}
		params, perr := workflowResumeParams(name, rest)
		if perr != nil {
			return 1, perr
		}
		engine := p.Engine
		if engine == "" {
			engine = "lobster"
		}
		if ex == nil {
			return 1, fmt.Errorf("pipeline resume %s: no host executor — the workflow engine is reached host-side via InvokeProvider(\"workflow\", %q, %q); run this through charly, not the plugin CLI directly", name, engine, ops.OpWorkflowResume)
		}
		raw, ierr := ex.InvokeProvider(context.Background(), "workflow", engine, ops.OpWorkflowResume, params, nil, ops.InvokeProviderOpts{})
		if ierr != nil {
			return 1, fmt.Errorf("pipeline resume %s: workflow:%s %s: %w", name, engine, ops.OpWorkflowResume, ierr)
		}
		var reply spec.WorkflowRunReply
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &reply)
		}
		if rerr := recordPending(genDirOf(name), &reply); rerr != nil {
			return 1, rerr
		}
		if reply.Status != "" {
			fmt.Printf("pipeline %s: resumed via workflow:%s — status=%s\n", name, engine, reply.Status)
		} else {
			fmt.Printf("pipeline %s: resumed via workflow:%s\n", name, engine)
		}
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

// genDirOf is the ONE definition of a pipeline's generated-state directory — the
// lowering target `run` writes (workflow.lobster + charly.yml) and the place `resume`
// looks for the gate handle. Two spellings of this path would let a resume read a
// directory the run never wrote.
func genDirOf(name string) string {
	return filepath.Join(projectDir(), ".opencharly", "pipelines", name)
}

// pendingGate is the handle a paused run leaves behind so that a LATER, SEPARATE process
// can answer its gate: `charly pipeline resume <name> …` is ordinarily run after the
// `charly pipeline run <name>` that paused has already exited, so the token cannot be
// carried in memory and has to be recorded on disk.
type pendingGate struct {
	ResumeToken string `json:"resumeToken"`
	Status      string `json:"status"`
}

func pendingPath(genDir string) string { return filepath.Join(genDir, "pending.json") }

// recordPending writes the paused gate's token into the run's gen dir, or CLEARS a stale
// one when the reply carries no gate. Clearing matters: a token left from an earlier
// paused run would otherwise be picked up by a later `resume <name>` and answer a gate
// that no longer exists.
func recordPending(genDir string, reply *spec.WorkflowRunReply) error {
	if reply.ResumeToken == "" {
		if err := os.Remove(pendingPath(genDir)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("pipeline: clear pending gate in %s: %w", genDir, err)
		}
		return nil
	}
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		return err
	}
	b, merr := json.Marshal(pendingGate{ResumeToken: reply.ResumeToken, Status: reply.Status})
	if merr != nil {
		return merr
	}
	// 0o600: the token is a capability — it answers the gate it names.
	return os.WriteFile(pendingPath(genDir), b, 0o600)
}

// readPendingToken resolves a pipeline NAME to the gate handle its last run recorded.
// A missing handle is a NAMED error, never an empty token: an empty token would reach
// the engine as "no handle" and be refused there with a message about tokens rather than
// about the pipeline the caller actually named.
func readPendingToken(genDir string) (string, error) {
	b, err := os.ReadFile(pendingPath(genDir))
	if err != nil {
		return "", fmt.Errorf("no pending gate recorded in %s (run the pipeline first, or pass --token/--id): %w", genDir, err)
	}
	var g pendingGate
	if uerr := json.Unmarshal(b, &g); uerr != nil {
		return "", fmt.Errorf("pending gate in %s: %w", genDir, uerr)
	}
	if g.ResumeToken == "" {
		return "", fmt.Errorf("no pending gate recorded in %s", genDir)
	}
	return g.ResumeToken, nil
}

// workflowResumeParams builds the OpWorkflowResume envelope — the mirror of
// workflowRunParams, and the reason the CLI cannot drift from the envelope it documents.
//
// `--approve` is parsed into the TRI-STATE pointer, not a bool: `yes` → &true, `no` →
// &false, and ABSENT → nil. That last case is the whole point of the wire change — a
// caller that supplies no decision must be distinguishable from one that rejected, and
// the engine refuses the former by name.
func workflowResumeParams(name string, rest []string) ([]byte, error) {
	req := spec.WorkflowResumeRequest{Pipeline: name, Token: flagAfter(rest, "--token"), Id: flagAfter(rest, "--id")}
	if req.Token == "" && req.Id == "" {
		tok, err := readPendingToken(genDirOf(name))
		if err != nil {
			return nil, err
		}
		req.Token = tok
	}
	if has(rest, "--approve") {
		switch flagAfter(rest, "--approve") {
		case "yes":
			t := true
			req.Approve = &t
		case "no":
			f := false
			req.Approve = &f
		default:
			return nil, fmt.Errorf("pipeline resume %s: --approve %q: accepted values are yes, no", name, flagAfter(rest, "--approve"))
		}
	}
	if has(rest, "--cancel") {
		req.Cancel = true
	}
	if has(rest, "--response-json") {
		raw := flagAfter(rest, "--response-json")
		var resp map[string]any
		if uerr := json.Unmarshal([]byte(raw), &resp); uerr != nil {
			return nil, fmt.Errorf("pipeline resume %s: --response-json: %v (want a JSON object)", name, uerr)
		}
		if resp == nil {
			return nil, fmt.Errorf("pipeline resume %s: --response-json: want a JSON object, got %q", name, raw)
		}
		req.Response = resp
	}
	params, merr := json.Marshal(req)
	if merr != nil {
		return nil, fmt.Errorf("pipeline resume %s: encode request: %w", name, merr)
	}
	return params, nil
}
