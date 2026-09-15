package pluginpipeline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// P1 — the bare agent runtime: a direct chat-completions call with a fully
// configurable system prompt (inline), optional skills appended (skills.go),
// and optional tools dispatched back to the engine (tools.go).

type chatMsg struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type toolSchema struct {
	Type     string         `json:"type"`
	Function functionSchema `json:"function"`
}
type functionSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type chatRequest struct {
	Model       string       `json:"model"`
	Messages    []chatMsg    `json:"messages"`
	Temperature float64      `json:"temperature"`
	Tools       []toolSchema `json:"tools,omitempty"`
	ToolChoice  string       `json:"tool_choice,omitempty"`
	// Stream requests SSE (`stream: true`) so response headers arrive at once and
	// chunks flow as the model produces — the precondition for an IDLE bound
	// instead of a whole-generation deadline.
	Stream bool `json:"stream,omitempty"`
}
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   *string    `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// llmConfig resolves the LLM endpoint. Precedence, every layer:
// 1. the ENV overrides (EVAL_LLM_BASE_URL / EVAL_LLM_MODEL / EVAL_LLM_API_KEY)
//   - the operator layer,
//
// 2. the entity's authored llm block - the lane-author layer,
// 3. the built-in default - the LOCAL ollama server (deepseek-v4.1-flash:cloud).
// An empty RESOLVED key means ABSENT: the client sends NO auth header (the
// local ollama needs none) - a missing secret can never zero out other layers.
func llmBaseURL(rc *runCtx) string {
	if v := os.Getenv("EVAL_LLM_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if rc != nil && rc.llm != nil {
		if v, ok := rc.llm["base_url"].(string); ok && v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return "http://localhost:11434/v1"
}
func llmModel(rc *runCtx) string {
	if v := os.Getenv("EVAL_LLM_MODEL"); v != "" {
		return v
	}
	if rc != nil && rc.llm != nil {
		if v, ok := rc.llm["model"].(string); ok && v != "" {
			return v
		}
	}
	return "deepseek-v4.1-flash:cloud"
}
func llmAPIKey(rc *runCtx) string {
	if v := os.Getenv("EVAL_LLM_API_KEY"); v != "" {
		return v
	}
	if rc != nil && rc.llm != nil {
		if v, ok := rc.llm["api_key"].(string); ok {
			return v
		}
	}
	return ""
}
func envMaxTurns() int {
	if v := os.Getenv("EVAL_MAX_TURNS"); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 {
			return n
		}
	}
	return 20
}

func intValue(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, _ := parseInt(t)
		return n
	}
	return 0
}
func parseInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// defaultChatIdleTimeout bounds how long a STREAMING completion may go with NO
// chunk before it is treated as stalled. This is an IDLE bound, not a
// whole-generation deadline: a slow reasoning model that keeps emitting
// (reasoning deltas, tool-call deltas) is making progress and is never cut off,
// while a provider that has genuinely gone silent fails in bounded time.
//
// The predecessor here was `http.Client{Timeout: 5 * time.Minute}` on a
// NON-streaming request — a whole-generation deadline, so a large/slow
// generation was guillotined at 5 minutes with `context deadline exceeded
// (Client.Timeout exceeded while awaiting headers)` even though the provider was
// still working. That is the SAME defect class the org's pr-validator workflow
// RCA'd for plugin-review (non-streaming request under a whole-generation
// deadline); the remedy is the same: stream, and bound the idle gap.
//
// Overridable via EVAL_LLM_IDLE_TIMEOUT (any Go duration, e.g. "10m"); the
// operator can raise it for an unusually slow endpoint.
func chatIdleTimeout() time.Duration {
	if v := os.Getenv("EVAL_LLM_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 3 * time.Minute
}

// chat issues ONE streaming chat-completions call and assembles the assistant
// message from the SSE deltas.
//
// Streaming is LOAD-BEARING, not an optimization: with `stream: true` the
// response headers arrive immediately and chunks flow as the model produces, so
// the bound can be an IDLE bound (no chunk for chatIdleTimeout) instead of a
// whole-generation deadline. Content deltas and tool-call deltas (assembled by
// index — the OpenAI SSE shape) both count as progress.
func chat(ctx context.Context, rc *runCtx, msgs []chatMsg, tools []toolSchema) (chatMsg, error) {
	reqBody := chatRequest{Model: llmModel(rc), Messages: msgs, Temperature: 0.2, Tools: tools, ToolChoice: "auto", Stream: true}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return chatMsg{}, err
	}

	// The request rides a CANCELLABLE CHILD of the caller's ctx, and the IDLE
	// watchdog CANCELS it. This is the mechanism that actually bounds a stalled
	// stream: http.Transport cancels the request on ctx-done by tearing the
	// connection DOWN, which makes the blocked resp.Body.Read return an error.
	// (resp.Body.Close() does NOT work here — in net/http, bodyEOFSignal.Read
	// holds es.mu across the blocking read and bodyEOFSignal.Close blocks on that
	// same mutex, so a Close from the watchdog just queues behind the in-flight
	// Read and unblocks nothing. Validator-caught; see the stall test.)
	idle := chatIdleTimeout()
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	var idleTripped atomic.Bool
	watchdog := time.AfterFunc(idle, func() {
		idleTripped.Store(true)
		cancelRead()
	})
	defer watchdog.Stop()

	req, err := http.NewRequestWithContext(readCtx, http.MethodPost, llmBaseURL(rc)+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return chatMsg{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if k := llmAPIKey(rc); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	req.Header.Set("HTTP-Referer", "https://github.com/opencharly/plugin-pipeline")
	req.Header.Set("X-Title", "plugin-pipeline")
	// No http.Client.Timeout (that would be a whole-generation deadline again):
	// the request is bounded by readCtx — the caller's ctx plus the IDLE watchdog
	// that cancels it on a chunk gap.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if idleTripped.Load() {
			return chatMsg{}, fmt.Errorf("LLM stream stalled: no response for %s (provider unreachable or silent); raise EVAL_LLM_IDLE_TIMEOUT for an unusually slow endpoint", idle)
		}
		return chatMsg{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return chatMsg{}, fmt.Errorf("LLM %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var (
		contentSB strings.Builder
		toolCalls = map[int]*toolCall{} // assembled by index (OpenAI SSE)
		maxIdx    = -1
	)
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 8<<20))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		watchdog.Reset(idle) // progress: a chunk arrived
		line := strings.TrimSpace(scanner.Text())
		const prefix = "data:"
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // a non-JSON keepalive/comment line is not fatal
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != nil {
				contentSB.WriteString(*ch.Delta.Content)
			}
			for _, tc := range ch.Delta.ToolCalls {
				cur := toolCalls[tc.Index]
				if cur == nil {
					cur = &toolCall{}
					toolCalls[tc.Index] = cur
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Type != "" {
					cur.Type = tc.Type
				}
				cur.Function.Name += tc.Function.Name
				cur.Function.Arguments += tc.Function.Arguments
				if tc.Index > maxIdx {
					maxIdx = tc.Index
				}
			}
		}
	}
	// The watchdog cancels readCtx, which the Transport turns into a body-read
	// error; distinguish that IDLE trip from a genuine transport error via the
	// atomic flag. A stall is NEVER silent (R1): even a partial turn is an ERROR,
	// because a truncated completion must not be mistaken for a finished one.
	if idleTripped.Load() {
		return chatMsg{}, fmt.Errorf("LLM stream stalled: no chunk for %s after %d byte(s) of content and %d tool call(s) (provider stopped streaming); raise EVAL_LLM_IDLE_TIMEOUT for an unusually slow endpoint",
			idle, contentSB.Len(), len(toolCalls))
	}
	if err := scanner.Err(); err != nil {
		return chatMsg{}, err
	}
	// A stream that ended with NEITHER content NOR tool calls (a 200 with an empty
	// completion, e.g. a filtered turn) is not a usable assistant message — fail
	// rather than return an empty turn that downstream renders as nothing. This is
	// the streaming-form equivalent of the removed `len(cr.Choices) == 0` guard.
	var contentPtr *string
	if contentSB.Len() > 0 {
		s := contentSB.String()
		contentPtr = &s
	}
	var calls []toolCall
	for i := 0; i <= maxIdx; i++ {
		if tc := toolCalls[i]; tc != nil {
			calls = append(calls, *tc)
		}
	}
	if contentPtr == nil && len(calls) == 0 {
		return chatMsg{}, fmt.Errorf("LLM: empty completion (no content, no tool calls)")
	}
	fmt.Printf("[chat] model=%s turns-so-far=%d content=%.120s tool_calls=%d\n", llmModel(rc), len(msgs), truncate(func() string {
		if contentPtr != nil {
			return *contentPtr
		}
		return ""
	}(), 120), len(calls))
	return chatMsg{Role: "assistant", Content: contentPtr, ToolCalls: calls}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// runAgent — the P1 runtime (standalone CLI + the agent stage); the turn cap
// from the env (the CLI default).
func runAgent(ctx context.Context, rc *runCtx, systemPrompt, prompt string, tools []string) (string, error) {
	return runAgentTurns(ctx, rc, systemPrompt, prompt, tools, 0)
}

// runAgentTurns: the runtime with an EXPLICIT turn cap (0 = the env default) —
// never a per-stage os.Setenv (process-global, raced the batch lanes).
func runAgentTurns(ctx context.Context, rc *runCtx, systemPrompt, prompt string, tools []string, stageMaxTurns int) (string, error) {
	sys := systemPrompt // skills are resolved in runAgentStage against the entity's corpus (skills.go is deleted)
	msgs := []chatMsg{{Role: "system", Content: &sys}, {Role: "user", Content: &prompt}}
	toolSch := buildTools(tools)
	maxTurns := stageMaxTurns
	if maxTurns <= 0 {
		maxTurns = envMaxTurns()
	}
	for turn := 0; turn < maxTurns; turn++ {
		msg, err := chat(ctx, rc, msgs, toolSch)
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
			res := dispatchTool(tc.Function.Name, tc.Function.Arguments, rc)
			fmt.Printf("[tool] %s(%s) -> %.200s\n", tc.Function.Name, tc.Function.Arguments, res)
			if strings.Contains(res, "\"error\"") {
				consecutiveFails++
				if consecutiveFails >= 5 {
					return "", fmt.Errorf("agent: %d consecutive tool failures (last: %s) — the evidence seam is broken; aborting instead of spiraling", consecutiveFails, tc.Function.Name)
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

// runAgentStage: the plan agent stage. The stage's prompt is the SYSTEM
// message; the USER message carries the stage id + the structured ledger facts
// (the prior stage outputs — the agent never has to guess the evidence layout
// to know what happened). The declared skill: refs are SKILL NAMES resolved
// against the entity's skills.corpus; an unresolvable ref FAILS the stage
// informatively (the decorative-ref era is gone). The reply is decoded by the
// TYPED decoder against the declared outputs — a contract violation fails the
// stage with the exact field + expected type (the redo signal is informed).
func runAgentStage(ctx context.Context, rc *runCtx, raw map[string]any, l *ledger) (map[string]any, error) {
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
			p := filepath.Join(corpus, name, "SKILL.md")
			b, err := os.ReadFile(p)
			if err != nil {
				return map[string]any{}, fmt.Errorf("agent stage %s: skill %q not found in the corpus at %s (declared skill: refs must resolve — the decorative-ref era is gone)", id, name, corpus)
			}
			sys += "\n\n--- " + name + " ---\n" + string(b)
		}
	}
	// stage-level turn cap (raw max_turns) overrides the env default; the
	// stage prompt can then enforce the model's own termination.
	mt := 0
	if raw != nil {
		mt = intValue(raw["max_turns"])
	}
	// the user message: the stage id + the structured ledger facts — the agent
	// narrates from facts, never from path-guessing archaeology.
	user := "Run this stage per your instructions.\n\nStage: " + id
	if rc != nil && rc.ledger != nil {
		user += "\n\nLedger facts (the prior stage outputs of THIS run):\n" + rc.ledger.facts()
	}
	resp, err := runAgentTurns(ctx, rc, sys, user, strList(raw["tools"]), mt)
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
	// with the redo-plan trigger (the violation message lands in the ledger
	// facts, so the retried agent sees exactly what was wrong), never a
	// fail-hard and never a silent pass.
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

// redoError: the informed-redo sentinel — a stage returns it to fail with a
// redo trigger instead of fail-harding the lane.
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
