package pluginpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
)

// verb_env.go — the LIFTED-VERB contract. Each of the seven workflow stage kinds
// (agent, probe, ade, generate, emit, media, gate) is registered as an ordinary
// `verb:` capability and reachable from any charly plan (`<word>: <input>` sugar),
// not only from a kind:pipeline entity's `stages:`. The stage BODY moves to
// verb_<word>.go; the handler `runVerb<Word>(in params.Pipeline<Word>Input, e
// *verbEnv)` returns the stage's Outputs map and an error, and the CALLER owns
// status/message/trigger bookkeeping (runStage for a plan stage, Invoke for a
// standalone verb dispatch) — one body, two callers (R3).
//
// verbEnv IS THE RUN CONTEXT the bodies consume. The nine declared fields are the
// per-run state a stage reads; the four RUN-CONTEXT fields below them are not
// decorative — each closes a behaviour-preservation gap the D2 nine cannot close:
//
//   - repo:  the agent/PR tools resolve their gh target from rc.repo (tools.go
//     prRef). The staged repo comes from the ENTITY, never the stage body, so a
//     typed-input round trip cannot reproduce it.
//   - ctx:   the agent and ade bodies are cancellable (runAgentTurns -> chat,
//     runAdeBedKit -> the host executor). A context cannot be reconstructed from
//     a params struct.
//   - id:    the agent body names the stage in its USER message ("Stage: <id>")
//     and in its error/log lines; #PipelineAgentInput deliberately has no `id`.
//   - raw:   the ORIGINAL authored stage map, carried for the plan path only.
//     Re-deriving it from the typed input is LOSSY: encoding/json's omitempty is
//     inert for STRUCT-typed fields, so `llm:`/`cache:`/`redo:` always reappear as
//     ZERO objects. stageLLM() returns nil only when the key is ABSENT (it checks
//     `block, ok := raw["llm"]; if !ok || block == nil`), so a re-derived map would
//     give every stage a non-nil zero llm override where today it has none — a
//     silent config-precedence change. When raw is nil (the standalone dispatch)
//     the handler materialises the input with mapOf() instead.
//
// The pipeline path therefore reaches its bodies through the ORIGINAL map; the
// standalone path through the typed input the host supplied. Both go through
// ONE handler per verb.
type verbEnv struct {
	resolve func(string) string
	report  map[string]any
	media   map[string]any
	skills  map[string]any
	llm     params.LLMSpec
	pr      string
	calver  string
	workdir string
	ex      *sdk.Executor
	l       *ledger

	// run-context fields (see the header).
	repo string
	ctx  context.Context
	id   string
	raw  map[string]any
}

// verbEnv fills the env from the live pipeline run. The arm then overrides ctx,
// id and raw for the stage it is about to run.
func (rc *runCtx) verbEnv() *verbEnv {
	if rc == nil {
		return standaloneVerbEnv(nil, "")
	}
	return &verbEnv{
		resolve: rc.resolveRefs,
		report:  rc.report,
		media:   rc.media,
		skills:  rc.skills,
		llm:     rc.llm,
		pr:      rc.pr,
		calver:  rc.calver,
		workdir: rc.workdir,
		ex:      rc.ex,
		l:       rc.ledger,
		repo:    rc.repo,
	}
}

// standaloneVerbEnv is the env for a verb dispatched OUTSIDE a kind:pipeline run
// (a `<word>: <input>` step in any plan). The reference resolver is the IDENTITY
// function: the caller's plan already owns ref resolution, so a lone verb must not
// re-interpret `$pr`/`@stage.output` text. Every map/struct fallback is zero and
// there is no ledger.
func standaloneVerbEnv(ex *sdk.Executor, workdir string) *verbEnv {
	return &verbEnv{
		resolve: func(s string) string { return s },
		workdir: workdir,
		ex:      ex,
	}
}

// ctxOf: the env's context, defaulted — never nil at a call site.
func (e *verbEnv) ctxOf() context.Context {
	if e == nil || e.ctx == nil {
		return context.Background()
	}
	return e.ctx
}

// runCtx rebuilds the run context the stage bodies consume. resolveRefs is wired
// to e.resolve, so the pipeline path delegates to the live run's own resolver
// (env + ledger refs intact) and the standalone path leaves refs verbatim.
func (e *verbEnv) runCtx() *runCtx {
	if e == nil {
		return &runCtx{}
	}
	rc := &runCtx{
		pr:      e.pr,
		repo:    e.repo,
		calver:  e.calver,
		workdir: e.workdir,
		ledger:  e.l,
		ex:      e.ex,
		report:  e.report,
		llm:     e.llm,
		media:   e.media,
		skills:  e.skills,
	}
	if e.resolve != nil {
		rc.resolveOverride = e.resolve
	}
	return rc
}

// stageRaw: the authored stage map for the plan path, else the typed input
// materialised back into the CUE-shaped map the bodies read.
func (e *verbEnv) stageRaw(in any) map[string]any {
	if e != nil && e.raw != nil {
		return e.raw
	}
	return mapOf(in)
}

// decodeStageInput materialises a stage's authored map into its generated
// #Pipeline<Word>Input type — the plan path's half of the ONE handler signature
// (the standalone path decodes the same type off the wire). The typed value is
// the stage's self-describing declaration; the body reads the ORIGINAL map
// (verbEnv.raw) so no omitempty-shaped field is fabricated (see verb_env.go).
func decodeStageInput[In any](raw map[string]any) In {
	var in In
	if raw != nil {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &in)
	}
	return in
}

// stageID: the stage id for messages, from the run context (standalone: the
// input has no id — #PipelineAgentInput and #PipelineGateInput declare none).
func (e *verbEnv) stageID() string {
	if e == nil {
		return ""
	}
	return e.id
}

// verbFail is a verb body's FAILURE CARRIER: the handler returns it when the
// caller must set a stage Message/Trigger that differs from the returned error
// (probe: res.Message is the probe message, the error is the wrapped "probe X
// failed" string; ade: a fail_on verdict is a triggered, NON-error failure).
type verbFail struct {
	msg     string
	trigger string
	err     error
}

func (v *verbFail) Error() string {
	if v.err != nil {
		return v.err.Error()
	}
	return v.msg
}

func (v *verbFail) Unwrap() error { return v.err }

// failOnVerb applies a lifted verb's failure to the stage result, preserving each
// kind's exact status/message/trigger/error contract: a plain error sets
// fail + err.Error(); a *verbFail additionally carries the stage Message/Trigger
// and returns the underlying error (nil for ade's fail_on — a triggered, not
// fail-hard, outcome).
func failOnVerb(res *StageResult, err error) (*StageResult, error) {
	if err == nil {
		return res, nil
	}
	res.Status = "fail"
	res.Message = err.Error()
	var vf *verbFail
	if errors.As(err, &vf) {
		res.Message = vf.msg
		res.Trigger = vf.trigger
		return res, vf.err
	}
	return res, err
}

// invokeLiftedVerb decodes a standalone verb dispatch and returns the verdict
// envelope every charly verb replies with ({"status":"pass"} or
// {"status":"fail","message":…}). It NEVER falls through to the pipeline CLI: an
// unknown word is a named error, so a typo is loud instead of running the wrong
// thing.
func invokeLiftedVerb[In any](ctx context.Context, req *pb.InvokeRequest, run func(In, *verbEnv) (map[string]any, error)) (*pb.InvokeReply, error) {
	var in In
	if pj := req.GetParamsJson(); len(pj) > 0 {
		var wrap struct {
			PluginInput json.RawMessage `json:"plugin_input"`
		}
		if err := json.Unmarshal(pj, &wrap); err != nil {
			return nil, fmt.Errorf("pipeline verb %q: params decode: %w", req.GetReserved(), err)
		}
		if len(wrap.PluginInput) > 0 {
			if err := json.Unmarshal(wrap.PluginInput, &in); err != nil {
				return nil, fmt.Errorf("pipeline verb %q: input decode: %w", req.GetReserved(), err)
			}
		}
	}
	wd, _ := os.Getwd()
	// The executor is OPTIONAL: an agent/generate/gate verb needs none, and the
	// ade verb degrades to its external check-run path when the host has no
	// reverse channel (the SAME contract the CLI holds with ex == nil).
	ex, _ := sdk.ExecutorForInvoke(ctx, req.GetExecutorBrokerId())
	e := standaloneVerbEnv(ex, wd)
	e.ctx = ctx
	if _, err := run(in, e); err != nil {
		return verbReply("fail", err.Error()), nil
	}
	return verbReply("pass", ""), nil
}

func verbReply(status, msg string) *pb.InvokeReply {
	reply := map[string]any{"status": status}
	if msg != "" {
		reply["message"] = msg
	}
	b, _ := json.Marshal(reply)
	return &pb.InvokeReply{ResultJson: b}
}
