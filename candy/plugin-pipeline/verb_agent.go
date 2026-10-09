package pluginpipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// verb_agent.go — the lifted `agent` verb. The plan stage body (runAgentStage)
// MOVED here from agent.go when the stage kind became a reachable verb: it is
// registered as {Class: "verb", Word: "agent", InputDef: "#PipelineAgentInput"}
// and dispatched by Invoke (a `<word>: <input>` step in any plan, or the
// lowering's generated task). ONE body, one caller (R3).

// runVerbAgent is the verb handler: it materialises the stage map + run context
// from the env and runs the agent body. Outputs and the *redoError sentinel pass
// through unwrapped (the caller's status/trigger bookkeeping depends on it).
func runVerbAgent(in params.PipelineAgentInput, e *verbEnv) (map[string]any, error) {
	raw := e.stageRaw(in)
	if id := e.stageID(); id != "" {
		raw["id"] = id
	}
	rc := e
	return runAgentStage(e.ctxOf(), rc, raw)
}

// runAgentStage: the plan agent stage. The stage's prompt is the SYSTEM
// message; the USER message carries the stage id. The declared skill: refs
// are SKILL NAMES resolved
// against the entity's skills.corpus; an unresolvable ref FAILS the stage
// informatively (the decorative-ref era is gone). The reply is decoded by the
// TYPED decoder against the declared outputs — a contract violation fails the
// stage with the exact field + expected type (the redo signal is informed).
func runAgentStage(ctx context.Context, rc *verbEnv, raw map[string]any) (map[string]any, error) {
	id := asString(raw["id"])
	// the COMMITTED-plan cache: on a freshness hit the declared outputs are READ
	// from the artifact and the agent NEVER runs — the render-once-per-<key>
	// primitive (e.g. per pr@sha) that reuses a committed plan instead of
	// re-authoring it every run. A miss falls through to the normal authoring.
	if out, hit, err := readAgentCache(rc, raw); err != nil {
		return map[string]any{}, err
	} else if hit {
		fmt.Printf("[agent %s] cache hit — outputs read from the committed plan (agent not run)\n", id)
		return out, nil
	}
	sys := asString(raw["prompt"])
	if rc != nil {
		sys = rc.resolveRefs(sys)
	}
	// skills: resolve the stage's skill: refs against the entity's corpus.
	// A declared skill that does not resolve is a LANE DEFECT — the stage
	// fails informatively instead of running the agent unskilled.
	if rc != nil {
		corpus := skillCorpus(rc)
		for _, name := range strList(raw["skill"]) {
			bundle, berr := skillBundle(corpus, name)
			if berr != nil {
				return map[string]any{}, fmt.Errorf("agent stage %s: %w", id, berr)
			}
			sys += bundle
		}
	}
	// stage-level turn cap (raw max_turns) overrides the env default; the
	// stage prompt can then enforce the model's own termination.
	mt := 0
	if raw != nil {
		mt = intValue(raw["max_turns"])
	}
	// the user message: the stage id (the engine holds the step graph, so the
	// agent does not narrate from a fabricated evidence layout).
	user := "Run this stage per your instructions.\n\nStage: " + id
	resp, err := runAgentTurns(ctx, rc, sys, user, strList(raw["tools"]), mt, stageLLM(raw))
	if err != nil {
		return map[string]any{}, err
	}
	// the stage's raw response lands in the run log — the evidence packet must
	// carry what the agent actually said (RCA 2026.252.2210: an unusable response
	// surfaced only as downstream empty renders, with nothing to inspect).
	fmt.Printf("[agent %s] response: %.400s\n", id, resp)
	out := map[string]any{"response": resp}
	// the TYPED decode: each declared output is validated against its
	// #OutputType; a violation is the INFORMED REDO signal — the stage fails
	// with the redo-plan trigger (the violation message names the exact field,
	// so the retry is informed), never a fail-hard and never a silent pass.
	declared, _ := raw["outputs"].(map[string]any)
	for name, spec := range declared {
		val, err := decodeTypedOutput(resp, name, spec)
		if err != nil {
			return map[string]any{}, &redoError{trigger: "redo-plan", msg: fmt.Sprintf("output %q: %v", name, err)}
		}
		out[name] = val
	}
	return out, nil
}

// skillBundle returns the system-prompt body for ONE declared skill: its SKILL.md and every
// references/*.md beside it, in sorted order.
//
// The references split is PART OF THE SKILL, not an optional extra. The contract is "an entry SKILL.md
// plus sibling references/*.md files in the same skill directory, loaded on demand by path" — and a
// PIPELINE STAGE has no harness to load them on demand, so the path never arrives and progressive
// disclosure silently becomes no disclosure at all. Sorted so one skill yields one system prompt, run
// after run (opencharly/plugin-pipeline#36).
func skillBundle(corpus, name string) (string, error) {
	dir := filepath.Join(corpus, name)
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("skill %q not found in the corpus at %s (declared skill: refs must resolve — the decorative-ref era is gone)", name, corpus)
	}
	out := "\n\n--- " + name + " ---\n" + string(b)
	refs, gerr := filepath.Glob(filepath.Join(dir, "references", "*.md"))
	if gerr != nil {
		return "", fmt.Errorf("skill %q: references glob: %w", name, gerr)
	}
	sort.Strings(refs)
	for _, r := range refs {
		rb, rerr := os.ReadFile(r)
		if rerr != nil {
			return "", fmt.Errorf("skill %q: reference %s: %w", name, filepath.Base(r), rerr)
		}
		out += "\n\n--- " + name + "/references/" + filepath.Base(r) + " ---\n" + string(rb)
	}
	return out, nil
}
