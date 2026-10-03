package pluginpipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"gopkg.in/yaml.v3"
)

// P1 — the bare agent runtime: a direct chat-completions call with a fully
// configurable system prompt (inline), optional skills appended, and optional
// tools dispatched back to the engine (tools.go). The wire client is the SHARED
// github.com/opencharly/sdk/llmkit client, reached through the thin adapter in
// llm.go (the client moved to the SDK so the `vision:` check verb speaks the
// endpoint identically — R3); this file owns the turn loop + the skills /
// typed-output contract.
//
// envMaxTurns / intValue / parseInt moved to llm.go with the config resolution
// (envMaxTurns is now the stage's authored max_turns with the same env default).

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// runAgent — the P1 runtime (standalone CLI + the agent stage); the turn cap
// from the env (the CLI default).
func runAgent(ctx context.Context, rc *runCtx, systemPrompt, prompt string, tools []string) (string, error) {
	return runAgentTurns(ctx, rc, systemPrompt, prompt, tools, 0, nil)
}

// runAgentTurns: the runtime with an EXPLICIT turn cap (0 = the env default) —
// never a per-stage os.Setenv (process-global, raced the batch lanes). `stage`
// carries the optional per-stage llm override (env > stage > entity > default).
func runAgentTurns(ctx context.Context, rc *runCtx, systemPrompt, prompt string, tools []string, stageMaxTurns int, stage *params.StageLLMSpec) (string, error) {
	sys := systemPrompt // skills are resolved in runAgentStage against the entity's corpus
	msgs := []chatMsg{{Role: "system", Content: &sys}, {Role: "user", Content: &prompt}}
	toolSch := buildTools(tools)
	maxTurns := stageMaxTurns
	if maxTurns <= 0 {
		maxTurns = envMaxTurns()
	}
	for turn := 0; turn < maxTurns; turn++ {
		msg, err := chat(ctx, rc, stage, msgs, toolSch)
		if err != nil {
			return "", err
		}
		content := ""
		if msg.Content != nil {
			content = *msg.Content
		}
		msgs = append(msgs, msg)
		if len(msg.ToolCalls) == 0 {
			return content, nil
		}
		// graceful-degradation termination: when the model keeps calling tools
		// past the budget, return its last verdict-carrying answer anyway (the
		// stage can still grade) — never fail the whole pipeline for an
		// over-chatty agent.
		if turn == maxTurns-1 {
			// forced termination: the last assistant content is the answer; when
			// the transcript is ALL tool calls (no prose), synthesize a fallback
			// so the stage still completes with a gradeable response.
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == "assistant" && msgs[i].Content != nil && *msgs[i].Content != "" {
					return *msgs[i].Content, nil
				}
			}
			return "[budget exhausted: the agent spent its " + strconv.Itoa(maxTurns) + " turns on tool calls without a prose verdict — grading from the evidence packet is partial; the check-run results remain authoritative]", nil
		}
		// the read-failure circuit breaker: N consecutive tool failures abort
		// the stage informatively — the 0/0/0/0 path-guessing spirals are gone
		// (RCA 2026.252.2250: 952 failed reads burned the report agents' turns).
		consecutiveFails := 0
		for _, tc := range msg.ToolCalls {
			res := dispatchTool(tc.Name, tc.Arguments, rc)
			fmt.Printf("[tool] %s(%s) -> %.200s\n", tc.Name, tc.Arguments, res)
			if strings.Contains(res, "\"error\"") {
				consecutiveFails++
				if consecutiveFails >= 5 {
					return "", fmt.Errorf("agent: %d consecutive tool failures (last: %s) — the evidence seam is broken; aborting instead of spiraling", consecutiveFails, tc.Name)
				}
			} else {
				consecutiveFails = 0
			}
			msgs = append(msgs, chatMsg{Role: "tool", ToolCallID: tc.ID, Content: &res})
		}
	}
	return "", fmt.Errorf("agent: turn budget exhausted")
}

// lastVerdict: scan the transcript backwards for the last non-empty assistant
// content containing a verdict JSON (the graceful-degradation contract).
func lastVerdict(msgs []chatMsg) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && msgs[i].Content != nil {
			c := *msgs[i].Content
			if strings.Contains(c, "\"verdict\"") {
				return c
			}
		}
	}
	return ""
}

// skillCorpus resolves the entity's skills.corpus to the directory holding
// <skill-name>/SKILL.md. The corpus value is REF-RESOLVED ($env.NAME / $workdir /
// ...) so a lane can point at a generated corpus outside its own tree (e.g.
// $env.EVAL_UMBRELLA/marketplace/distros/skills) without a hard-coded path; a
// relative result is then joined with the run workdir.
func skillCorpus(rc *runCtx) string {
	if rc == nil {
		return ""
	}
	corpus := rc.resolveRefs(s(rc.skills["corpus"]))
	if corpus != "" && !filepath.IsAbs(corpus) && rc.workdir != "" {
		corpus = filepath.Join(rc.workdir, corpus)
	}
	return corpus
}

// redoError: the informed-redo sentinel — a stage returns it to fail with a
// redo trigger instead of fail-harding the lane.
// (runAgentStage — the plan `agent` stage body — lives in verb_agent.go, its
// lifted verb: it is registered as verb:agent and reachable from any plan.)
type redoError struct {
	trigger string
	msg     string
}

func (e *redoError) Error() string { return e.msg }

// readAgentCache evaluates the agent stage's `cache:` block: a freshness hit
// reads the declared outputs from the committed plan artifact (source), keyed by
// the file's key_field vs the ref-resolved key. A miss (no cache block, missing
// file, or stale key) returns hit=false and the agent authors normally. A
// present-but-corrupt plan is an error — never a silent re-author — and a hit
// whose committed plan is MISSING a declared output is an error too (a partial
// plan must never render as an empty/nil bed var). Cached values are validated
// against the declared #OutputType, exactly like an agent reply.
func readAgentCache(rc *runCtx, raw map[string]any) (map[string]any, bool, error) {
	spec, _ := raw["cache"].(map[string]any)
	if len(spec) == 0 {
		return nil, false, nil
	}
	if rc == nil {
		// no run context (the standalone CLI path): a cache block cannot resolve
		// its refs — treat as a miss rather than panicking on a nil rc.
		return nil, false, nil
	}
	path := rc.resolveRefs(s(spec["path"]))
	key := rc.resolveRefs(s(spec["key"]))
	if path == "" && key == "" {
		// NO cache contract: #CacheSpec REQUIRES path AND key, so a block with
		// both empty is not something an author writes — it is the ZERO struct a
		// standalone verb dispatch materialises from its typed input (the input
		// is decoded from the wire; there is no "absent" spelling for a struct
		// field — see verb_env.go). A standalone `agent: {prompt: …}` must run the
		// agent, not fail on a cache block that was never authored. A
		// HALF-specified block (exactly one of the two) still hits the error below.
		return nil, false, nil
	}
	keyField := s(spec["key_field"])
	if keyField == "" {
		keyField = "head"
	}
	source := s(spec["source"])
	if path == "" || key == "" {
		return nil, false, fmt.Errorf("agent cache: path + key required")
	}
	if !filepath.IsAbs(path) && rc.workdir != "" {
		path = filepath.Join(rc.workdir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, nil // no committed plan yet — author fresh
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, false, fmt.Errorf("agent cache %s: unreadable committed plan: %w", path, err)
	}
	if got := s(doc[keyField]); got != key {
		return nil, false, nil // a stale plan (new head) — author fresh
	}
	fields := doc
	if source != "" {
		m, _ := doc[source].(map[string]any)
		if m == nil {
			return nil, false, fmt.Errorf("agent cache %s: source %q is not a mapping", path, source)
		}
		fields = m
	}
	out := map[string]any{"response": "(cache hit: " + path + ")", "cache_hit": true}
	declared, _ := raw["outputs"].(map[string]any)
	for name, spec := range declared {
		v, ok := fields[name]
		if !ok {
			return nil, false, fmt.Errorf("agent cache %s: the committed plan is missing the declared output %q", path, name)
		}
		vv, err := validateTypedValue(v, name, spec)
		if err != nil {
			return nil, false, fmt.Errorf("agent cache %s: output %q: %w", path, name, err)
		}
		out[name] = vv
	}
	return out, true, nil
}

// decodeTypedOutput: extract one declared output from the agent's reply (ONE
// JSON object, fence-tolerant) and validate it against the #OutputType spec.
// The quote-unaware extractJSON scanner is GONE — this is the one canonical
// decoder (R3).
func decodeTypedOutput(resp, name string, spec any) (any, error) {
	obj, err := parseReplyObject(resp)
	if err != nil {
		return nil, fmt.Errorf("the reply is not a single JSON object: %w", err)
	}
	val, ok := obj[name]
	if !ok {
		return nil, fmt.Errorf("the reply has no %q field (the declared output contract)", name)
	}
	return validateTypedValue(val, name, spec)
}

// validateTypedValue: validate ONE value against the declared #OutputType spec
// and normalize it. Shared by the agent-reply decoder and the cache-hit reader,
// so a committed plan is held to the SAME contract as a live reply (R3).
func validateTypedValue(val any, name string, spec any) (any, error) {
	m, _ := spec.(map[string]any)
	typ := s(m["type"])
	if typ == "" {
		typ = "string"
	}
	switch typ {
	case "string":
		if _, ok := val.(string); !ok {
			return nil, fmt.Errorf("expected a string, got %T", val)
		}
		return val, nil
	case "int":
		switch v := val.(type) {
		case float64:
			return int(v), nil
		case string:
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("expected an int, got %q", v)
			}
			return n, nil
		default:
			return nil, fmt.Errorf("expected an int, got %T", val)
		}
	case "bool":
		if _, ok := val.(bool); !ok {
			return nil, fmt.Errorf("expected a bool, got %T", val)
		}
		return val, nil
	case "enum":
		vs, _ := val.(string)
		allowed := strList(m["enum"])
		for _, a := range allowed {
			if vs == a {
				return vs, nil
			}
		}
		return nil, fmt.Errorf("expected one of %v, got %q", allowed, vs)
	case "string_list":
		arr, ok := val.([]any)
		if !ok {
			return nil, fmt.Errorf("expected a list of strings, got %T", val)
		}
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			es, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("expected a list of strings, got a %T element", e)
			}
			out = append(out, es)
		}
		return out, nil
	case "object":
		// any JSON object/array — validated as parseable JSON (the checks[],
		// the plan-json map, the nested structures). An ARRAY value's elements
		// must be objects: the checks contract is [{id, what, assertion,
		// knownRed}] — a bare-string simplification is a contract violation.
		if arr, ok := val.([]any); ok {
			for _, e := range arr {
				if _, ok := e.(map[string]any); !ok {
					return nil, fmt.Errorf("expected an array of objects (e.g. [{id, what, assertion, knownRed}]), got a %T element", e)
				}
			}
		}
		return val, nil
	default:
		return nil, fmt.Errorf("unknown output type %q", typ)
	}
}

// parseReplyObject: the reply is ONE JSON object; a fence-tolerant parse
// (the model may wrap the object in prose or fences — the object is the
// contract, the prose is noise).
func parseReplyObject(resp string) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(resp), &obj); err == nil {
		return obj, nil
	}
	start := strings.Index(resp, "{")
	if start < 0 {
		return nil, fmt.Errorf("no JSON object found in the reply")
	}
	depth := 0
	for i := start; i < len(resp); i++ {
		switch resp[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				if json.Unmarshal([]byte(resp[start:i+1]), &obj) == nil {
					return obj, nil
				}
				return nil, fmt.Errorf("the reply's JSON object does not parse")
			}
		}
	}
	return nil, fmt.Errorf("unterminated JSON object in the reply")
}
