package pluginpipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
)

// verb_env.go — the LIFTED-VERB contract. Each of the seven workflow stage kinds
// (agent, probe, ade, generate, emit, media, gate) is registered as an ordinary
// `verb:` capability and reachable from any charly plan (`<word>: <input>` sugar).
// The stage BODY lives in verb_<word>.go; the handler `runVerb<Word>(in
// params.Pipeline<Word>Input, e *verbEnv)` returns the stage's Outputs map and an
// error, and the CALLER owns status/message/trigger bookkeeping (the verb Invoke
// dispatch in plugin.go) — one body, one caller (R3).
//
// verbEnv IS THE RUN CONTEXT the bodies consume. It carries the per-run state a
// stage reads. `resolve` is the RUN-CONTEXT resolver: a verb reached standalone
// wires it to the IDENTITY function (the caller's plan already owns `$pr`/
// `$calver`/`$workdir`/`@stage` resolution, so a lone verb must not re-interpret
// that text); any other construction leaves it nil and resolveRefs applies the
// built-in grammar. The ENVIRONMENT namespace is NOT gated by it — `$env.NAME` has
// no other resolver in the chain, so refs.go substitutes it unconditionally from
// procEnv.
//
//   - repo:  the agent/PR tools resolve their gh target from rc.repo (tools.go
//     prRef). The staged repo comes from the ENTITY, never the stage body, so a
//     typed-input round trip cannot reproduce it.
//   - procEnv: the PROCESS ENVIRONMENT, the ONLY source a `$env.NAME` data
//     reference reads. `env` beside it is the per-run BOUND map (no writer in the
//     current architecture) — two sources, never merged, because concurrent lanes
//     share the process environment (RCA 2026.252.2210).
//   - ctx:   the agent and ade bodies are cancellable (runAgentTurns -> chat,
//     runAdeBedKit -> the host executor). A context cannot be reconstructed from
//     a params struct.
//   - id:    the agent body names the stage in its USER message ("Stage: <id>").
//     #PipelineAgentInput deliberately has no `id`.
//   - raw:   the ORIGINAL authored stage map, carried for the wire path. The
//     AUTHORED map is the wire's own plugin_input, so `llm:`/`cache:`/`redo:`
//     keep their ABSENCE — encoding/json's omitempty is inert for STRUCT-typed
//     fields, so a re-derived map would give every stage a non-nil zero llm
//     override where today it has none (a silent config-precedence change). When
//     raw is nil the handler materialises the typed input with mapOf() instead.
type verbEnv struct {
	resolve func(string) string
	report  map[string]any
	media   map[string]any
	skills  map[string]any
	llm     params.LLMSpec
	pr      string
	repo    string
	calver  string
	workdir string
	// env is the RUN-CONTEXT map: the per-run values a lane BOUND (the retired
	// executor set PR_NUMBER / PR_HEAD_SHA here). It is NOT the process
	// environment — concurrent lanes share that, and the defect it caused is RCA
	// 2026.252.2210 — which is why `adeLaneHead` reads THIS map while the ref
	// grammar reads procEnv below. Nothing binds a per-lane value in the current
	// architecture (a lane's item is a scoped STEP RESULT), so it stays empty by
	// construction.
	env map[string]string
	// procEnv is the PROCESS environment, and the ONE source the `$env.NAME` /
	// `${env.NAME}` ref grammar reads — the same source the retired form read
	// (envMap() = os.Environ()). standaloneVerbEnv, the only production
	// constructor, supplies it; a verbEnv built without one resolves an env ref to
	// "" and RECORDS the miss, because a resolver wired to a nil map is not a
	// resolver.
	procEnv map[string]string
	// missingEnv records every environment name the ref grammar could not find, in
	// encounter order, duplicates kept. invokeLiftedVerb reports them as ONE named
	// failure, so an unexported variable or a typo is never a silent empty value.
	missingEnv []string
	ex         *sdk.Executor

	// run-context fields (see the header).
	ctx context.Context
	id  string
	raw map[string]any
}

// standaloneVerbEnv is the env for a verb dispatched OUTSIDE any pipeline run
// (a `<word>: <input>` step in any plan). The RUN-CONTEXT resolver is the IDENTITY
// function: the caller's plan already owns `$pr`/`$calver`/`$workdir`/`@stage`
// resolution, so a lone verb must not re-interpret that text.
//
// The PROCESS ENVIRONMENT is a different namespace with no other owner, so it is
// carried here from os.Environ(): the `$env.NAME` data reference reads it
// unconditionally (refs.go:resolveRefs). Supplying it is what makes a data leaf
// resolve at all — a nil map was exactly what made `$env.NAME` invisible.
func standaloneVerbEnv(ex *sdk.Executor, workdir string) *verbEnv {
	return &verbEnv{
		resolve: func(s string) string { return s },
		workdir: workdir,
		procEnv: envMap(),
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

// stageRaw: the authored stage map for the wire path, else the typed input
// materialised back into the CUE-shaped map the bodies read.
func (e *verbEnv) stageRaw(in any) map[string]any {
	if e != nil && e.raw != nil {
		return e.raw
	}
	return mapOf(in)
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

// invokeLiftedVerb decodes a standalone verb dispatch and returns the verdict
// envelope every charly verb replies with ({"status":"pass"} or
// {"status":"fail","message":…}). It NEVER falls through to the pipeline CLI: an
// unknown word is a named error, so a typo is loud instead of running the wrong
// thing.
func invokeLiftedVerb[In any](ctx context.Context, req *pb.InvokeRequest, run func(In, *verbEnv) (map[string]any, error)) (*pb.InvokeReply, error) {
	var in In
	var pluginInput json.RawMessage
	if pj := req.GetParamsJson(); len(pj) > 0 {
		var wrap struct {
			PluginInput json.RawMessage `json:"plugin_input"`
		}
		if err := json.Unmarshal(pj, &wrap); err != nil {
			return nil, fmt.Errorf("pipeline verb %q: params decode: %w", req.GetReserved(), err)
		}
		pluginInput = wrap.PluginInput
		if len(pluginInput) > 0 {
			if err := json.Unmarshal(pluginInput, &in); err != nil {
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
	// The AUTHORED map for this dispatch is the wire's own plugin_input — carry
	// it verbatim instead of reconstructing it from the typed struct, which
	// cannot express ABSENCE for a struct-typed field (encoding/json's omitempty
	// is inert for structs, so `llm:`/`cache:`/`redo:` would be fabricated as
	// zero objects). With raw set from the wire, stageRaw hands the body exactly
	// what was authored. A dispatch that carries NO plugin_input authors nothing:
	// the raw map is EMPTY, not a zero-struct reconstruction.
	e.raw = map[string]any{}
	if len(pluginInput) > 0 {
		var rawMap map[string]any
		if err := json.Unmarshal(pluginInput, &rawMap); err == nil {
			e.raw = rawMap
		}
	}
	if _, err := run(in, e); err != nil {
		if msg := e.unresolvedEnvMessage(err); msg != "" {
			return verbReply("fail", msg), nil
		}
		return verbReply("fail", err.Error()), nil
	}
	if msg := e.unresolvedEnvMessage(nil); msg != "" {
		return verbReply("fail", msg), nil
	}
	return verbReply("pass", ""), nil
}

// unresolvedEnvMessage is the MISSING-VARIABLE contract of the ref grammar: a
// `$env.NAME` / `${env.NAME}` data reference whose name the process environment
// does not carry is a NAMED failure of the whole dispatch, never a silent empty
// value. It is aggregate (every distinct name, once, in encounter order) and it
// keeps the body's own error when there was one, so a real failure is never hidden
// behind the miss. "" means every referenced name resolved.
//
// Empty is the REJECTED alternative on purpose: an empty value is a run-time
// emptiness in place of a load error — a cache key that never matches, an emitted
// record carrying `head: ""` — which is the worse trade, and the same one
// opencharly/sdk#443 refused for the knob form.
func (e *verbEnv) unresolvedEnvMessage(cause error) string {
	if e == nil || len(e.missingEnv) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(e.missingEnv))
	names := make([]string, 0, len(e.missingEnv))
	for _, n := range e.missingEnv {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	msg := fmt.Sprintf("unresolved environment reference(s): %s — a $env.NAME / ${env.NAME} data reference reads the PROCESS environment and these names are not set there",
		strings.Join(names, ", "))
	if cause != nil {
		msg += fmt.Sprintf(" (the stage also failed: %s)", cause.Error())
	}
	return msg
}

func verbReply(status, msg string) *pb.InvokeReply {
	reply := map[string]any{"status": status}
	if msg != "" {
		reply["message"] = msg
	}
	b, _ := json.Marshal(reply)
	return &pb.InvokeReply{ResultJson: b}
}
