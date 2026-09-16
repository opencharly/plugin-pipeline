package pluginpipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// llm.go — the ONE OpenAI-compatible client for the engine, built on the
// official github.com/openai/openai-go SDK.
//
// The hand-rolled HTTP client + SSE parser this replaces existed for one
// reason: an IDLE liveness bound (a stalled stream must fail in bounded time
// without guillotining a slow-but-progressing generation). That property is
// LOAD-BEARING and is preserved here (see streamOnce): the SDK supplies the
// wire format, the accumulator, tool-call assembly, vision content parts and
// the full request-parameter surface; the idle watchdog stays ours.
//
// Every knob the hand-rolled client hardcoded (temperature, tool_choice, the
// 8 MiB caps, the 3-minute idle bound) is now AUTHORED in charly.yml through
// #LLMSpec/#LLMParams and resolved by resolveLLM below.

// defaultLLMBaseURL / defaultLLMModel keep a lane runnable with NO authored
// llm: block — the local ollama server is the built-in default layer.
const (
	defaultLLMBaseURL = "http://localhost:11434/v1"
	defaultLLMModel   = "deepseek-v4.1-flash:cloud"
	// defaultIdleTimeout bounds the gap BETWEEN streaming chunks (not the whole
	// generation). A provider that keeps emitting is never cut off; one that has
	// gone silent fails in bounded time.
	defaultIdleTimeout = 3 * time.Minute
)

// llmConfig is the RESOLVED endpoint + request config for one call: every
// layer (env > stage > entity > default) has already been applied.
type llmConfig struct {
	baseURL      string
	model        string
	apiKey       string
	organization string
	project      string
	timeout      time.Duration
	idleTimeout  time.Duration
	maxRetries   int
	headers      map[string]string
	p            params.LLMParams
}

// resolveLLM applies the documented precedence FIELD-WISE: a lower layer fills
// only what the higher layers left unset (env > stage > entity > built-in
// default). stage may be nil (the standalone CLI path).
//
// An empty RESOLVED api_key means ABSENT: no Authorization header is sent (the
// SDK's own guard is `BearerAuth && APIKey != ""`, verified in
// internal/requestconfig), so a missing secret can never zero out a lane — and
// the local ollama needs none.
func resolveLLM(rc *runCtx, stage *params.StageLLMSpec) llmConfig {
	cfg := llmConfig{
		baseURL:     defaultLLMBaseURL,
		model:       defaultLLMModel,
		idleTimeout: defaultIdleTimeout,
		maxRetries:  2,
	}

	// layer 3 — the entity's authored llm: block.
	if rc != nil {
		e := rc.llm
		if e.Base_url != "" {
			cfg.baseURL = e.Base_url
		}
		if e.Model != "" {
			cfg.model = e.Model
		}
		if e.Api_key != "" {
			cfg.apiKey = e.Api_key
		}
		if e.Organization != "" {
			cfg.organization = e.Organization
		}
		if e.Project != "" {
			cfg.project = e.Project
		}
		if d, ok := parseDuration(e.Timeout); ok {
			cfg.timeout = d
		}
		if d, ok := parseDuration(e.Idle_timeout); ok {
			cfg.idleTimeout = d
		}
		if e.Max_retries != nil {
			cfg.maxRetries = int(*e.Max_retries)
		}
		if len(e.Headers) > 0 {
			cfg.headers = map[string]string{}
			for k, v := range e.Headers {
				cfg.headers[k] = v
			}
		}
		cfg.p = e.Params
	}

	// layer 2 — the STAGE-LOCAL override (env still wins below).
	if stage != nil {
		if stage.Model != "" {
			cfg.model = stage.Model
		}
		if stage.Base_url != "" {
			cfg.baseURL = stage.Base_url
		}
		if stage.Api_key != "" {
			cfg.apiKey = stage.Api_key
		}
		cfg.p = mergeLLMParams(cfg.p, stage.Params)
	}

	// layer 1 — the ENV override (the operator layer).
	if v := os.Getenv("EVAL_LLM_BASE_URL"); v != "" {
		cfg.baseURL = v
	}
	if v := os.Getenv("EVAL_LLM_MODEL"); v != "" {
		cfg.model = v
	}
	if v := os.Getenv("EVAL_LLM_API_KEY"); v != "" {
		cfg.apiKey = v
	}
	if d, ok := parseDuration(os.Getenv("EVAL_LLM_IDLE_TIMEOUT")); ok {
		cfg.idleTimeout = d
	}
	if d, ok := parseDuration(os.Getenv("EVAL_LLM_TIMEOUT")); ok {
		cfg.timeout = d
	}
	if v := os.Getenv("EVAL_LLM_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.maxRetries = n
		}
	}

	cfg.baseURL = strings.TrimRight(cfg.baseURL, "/")
	if cfg.timeout < 0 {
		cfg.timeout = 0
	}
	if cfg.idleTimeout <= 0 {
		cfg.idleTimeout = defaultIdleTimeout
	}
	return cfg
}

// mergeLLMParams overlays the stage params onto the entity params FIELD-WISE:
// any field the stage set wins; every other field keeps the entity value. This
// is what makes a stage override a single knob without wiping its siblings.
func mergeLLMParams(base, over params.LLMParams) params.LLMParams {
	if over.Temperature != nil {
		base.Temperature = over.Temperature
	}
	if over.Top_p != nil {
		base.Top_p = over.Top_p
	}
	if over.Max_tokens != nil {
		base.Max_tokens = over.Max_tokens
	}
	if over.Max_completion_tokens != nil {
		base.Max_completion_tokens = over.Max_completion_tokens
	}
	if over.Frequency_penalty != nil {
		base.Frequency_penalty = over.Frequency_penalty
	}
	if over.Presence_penalty != nil {
		base.Presence_penalty = over.Presence_penalty
	}
	if over.Seed != nil {
		base.Seed = over.Seed
	}
	if over.Stop != nil {
		base.Stop = over.Stop
	}
	if over.Reasoning_effort != "" {
		base.Reasoning_effort = over.Reasoning_effort
	}
	if over.Reasoning.Effort != "" {
		base.Reasoning.Effort = over.Reasoning.Effort
	}
	if over.Response_format.Type != "" {
		base.Response_format = over.Response_format
	}
	if over.Stream_options.Include_usage != nil {
		base.Stream_options.Include_usage = over.Stream_options.Include_usage
	}
	if over.Parallel_tool_calls != nil {
		base.Parallel_tool_calls = over.Parallel_tool_calls
	}
	if over.Tool_choice != nil {
		base.Tool_choice = over.Tool_choice
	}
	if over.Logprobs != nil {
		base.Logprobs = over.Logprobs
	}
	if over.Top_logprobs != nil {
		base.Top_logprobs = over.Top_logprobs
	}
	if over.User != "" {
		base.User = over.User
	}
	if len(over.Metadata) > 0 {
		base.Metadata = over.Metadata
	}
	if len(over.Logit_bias) > 0 {
		base.Logit_bias = over.Logit_bias
	}
	if len(over.Extra) > 0 {
		if base.Extra == nil {
			base.Extra = map[string]any{}
		}
		for k, v := range over.Extra {
			base.Extra[k] = v
		}
	}
	return base
}

func parseDuration(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// clientOptions builds the SDK request options for one call.
//
// NO ENV BLEED: the client is NewChatCompletionService, NOT NewClient — it
// applies ONLY the options given here, so a stray operator OPENAI_API_KEY /
// OPENAI_BASE_URL / OPENAI_CUSTOM_HEADERS can never hijack a lane (NewClient
// prepends DefaultClientOptions(), which reads those).
func (c llmConfig) clientOptions() []option.RequestOption {
	opts := []option.RequestOption{
		option.WithBaseURL(c.baseURL),
		option.WithMaxRetries(c.maxRetries),
	}
	// An empty key means ABSENT — pass no key at all rather than an empty one.
	if c.apiKey != "" {
		opts = append(opts, option.WithAPIKey(c.apiKey))
	}
	if c.organization != "" {
		opts = append(opts, option.WithOrganization(c.organization))
	}
	if c.project != "" {
		opts = append(opts, option.WithProject(c.project))
	}
	if c.timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(c.timeout))
	}
	for k, v := range c.headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	return opts
}

// buildParams maps the authored #LLMParams onto the SDK request. EVERY field
// is applied only when set, so an omitted knob is not sent at all and the
// server's own default applies — the engine never injects a value the author
// did not ask for.
func (c llmConfig) buildParams(msgs []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) openai.ChatCompletionNewParams {
	p := openai.ChatCompletionNewParams{
		Model:    c.model,
		Messages: msgs,
	}
	if len(tools) > 0 {
		p.Tools = tools
	}
	if v := c.p.Temperature; v != nil {
		p.Temperature = openai.Float(*v)
	}
	if v := c.p.Top_p; v != nil {
		p.TopP = openai.Float(*v)
	}
	if v := c.p.Max_tokens; v != nil {
		p.MaxTokens = openai.Int(*v)
	}
	if v := c.p.Max_completion_tokens; v != nil {
		p.MaxCompletionTokens = openai.Int(*v)
	}
	if v := c.p.Frequency_penalty; v != nil {
		p.FrequencyPenalty = openai.Float(*v)
	}
	if v := c.p.Presence_penalty; v != nil {
		p.PresencePenalty = openai.Float(*v)
	}
	if v := c.p.Seed; v != nil {
		p.Seed = openai.Int(*v)
	}
	if s := c.p.Stop; s != nil {
		p.Stop = stopUnion(s)
	}
	if v := c.p.Reasoning_effort; v != "" {
		p.ReasoningEffort = shared.ReasoningEffort(v)
	}
	if c.p.Reasoning.Effort != "" {
		p.ReasoningEffort = shared.ReasoningEffort(c.p.Reasoning.Effort)
	}
	if t := c.p.Response_format.Type; t != "" {
		p.ResponseFormat = responseFormatUnion(c.p.Response_format)
	}
	if v := c.p.Stream_options.Include_usage; v != nil {
		p.StreamOptions.IncludeUsage = openai.Bool(*v)
	}
	if v := c.p.Parallel_tool_calls; v != nil {
		p.ParallelToolCalls = openai.Bool(*v)
	}
	if tc := c.p.Tool_choice; tc != nil {
		p.ToolChoice = toolChoiceUnion(tc)
	}
	if v := c.p.Logprobs; v != nil {
		p.Logprobs = openai.Bool(*v)
	}
	if v := c.p.Top_logprobs; v != nil {
		p.TopLogprobs = openai.Int(*v)
	}
	if c.p.User != "" {
		p.User = openai.String(c.p.User)
	}
	if len(c.p.Metadata) > 0 {
		md := shared.Metadata{}
		for k, v := range c.p.Metadata {
			md[k] = v
		}
		p.Metadata = md
	}
	if len(c.p.Logit_bias) > 0 {
		p.LogitBias = c.p.Logit_bias
	}
	return p
}

// extraOptions renders the `extra` escape hatch as sjson-set request options
// — the ONE legal place for an undocumented/unknown request-body key.
func (c llmConfig) extraOptions() []option.RequestOption {
	if len(c.p.Extra) == 0 {
		return nil
	}
	keys := make([]string, 0, len(c.p.Extra))
	for k := range c.p.Extra {
		keys = append(keys, k)
	}
	// deterministic order (a map walk is not)
	sort.Strings(keys)
	opts := make([]option.RequestOption, 0, len(keys))
	for _, k := range keys {
		opts = append(opts, option.WithJSONSet(k, c.p.Extra[k]))
	}
	return opts
}

// stopUnion: #LLMParams.stop is `string | [...string]` (generated as `any`);
// render whichever shape the author wrote.
func stopUnion(v any) openai.ChatCompletionNewParamsStopUnion {
	switch t := v.(type) {
	case string:
		return openai.ChatCompletionNewParamsStopUnion{OfString: openai.String(t)}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return openai.ChatCompletionNewParamsStopUnion{OfStringArray: out}
	case []string:
		return openai.ChatCompletionNewParamsStopUnion{OfStringArray: t}
	}
	return openai.ChatCompletionNewParamsStopUnion{}
}

// responseFormatUnion maps the authored #ResponseFormat onto the SDK union.
func responseFormatUnion(rf params.ResponseFormat) openai.ChatCompletionNewParamsResponseFormatUnion {
	switch rf.Type {
	case "json_object":
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		}
	case "json_schema":
		js := rf.Json_schema
		param := shared.ResponseFormatJSONSchemaParam{
			JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
				Name:   js.Name,
				Schema: js.Schema,
			},
		}
		if js.Description != "" {
			param.JSONSchema.Description = openai.String(js.Description)
		}
		if js.Strict {
			param.JSONSchema.Strict = openai.Bool(true)
		}
		return openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONSchema: &param}
	}
	// "text" (and the zero value): no response_format constraint is sent.
	return openai.ChatCompletionNewParamsResponseFormatUnion{}
}

// toolChoiceUnion maps the authored tool_choice onto the SDK union.
func toolChoiceUnion(v any) openai.ChatCompletionToolChoiceOptionUnionParam {
	if s, ok := v.(string); ok {
		switch s {
		case "none":
			return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("none")}
		case "required":
			return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("required")}
		case "auto":
			return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("auto")}
		}
		return openai.ChatCompletionToolChoiceOptionUnionParam{}
	}
	// the {function: {name}} form
	if m, ok := v.(map[string]any); ok {
		if fn, ok := m["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				return openai.ChatCompletionToolChoiceOptionUnionParam{
					OfFunctionToolChoice: &openai.ChatCompletionNamedToolChoiceParam{
						Function: openai.ChatCompletionNamedToolChoiceFunctionParam{Name: name},
					},
				}
			}
		}
	}
	return openai.ChatCompletionToolChoiceOptionUnionParam{}
}

// chatMsg is the engine's internal transcript row. It is deliberately NOT a
// wire type any more (the SDK owns the wire): Content is a *string so "no
// content" (a pure tool-call turn) is distinguishable from "".
type chatMsg struct {
	Role       string
	Content    *string
	ToolCalls  []chatToolCall
	ToolCallID string
}

// chatToolCall is the engine's internal tool-call row.
type chatToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// toSDKMessages converts the transcript to the SDK's message union.
func toSDKMessages(msgs []chatMsg) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		content := ""
		if m.Content != nil {
			content = *m.Content
		}
		switch m.Role {
		case "system":
			out = append(out, openai.SystemMessage(content))
		case "assistant":
			if len(m.ToolCalls) > 0 {
				// A tool-calling turn: the assistant message carries the calls
				// (content may be empty) so the following tool rows pair up.
				ap := openai.ChatCompletionAssistantMessageParam{
					ToolCalls: toSDKToolCalls(m.ToolCalls),
				}
				if content != "" {
					ap.Content = openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(content)}
				}
				out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &ap})
				continue
			}
			out = append(out, openai.AssistantMessage(content))
		case "tool":
			out = append(out, openai.ToolMessage(content, m.ToolCallID))
		default:
			out = append(out, openai.UserMessage(content))
		}
	}
	return out
}

// toSDKToolCalls converts the engine's tool-call rows to the SDK param union.
func toSDKToolCalls(calls []chatToolCall) []openai.ChatCompletionMessageToolCallUnionParam {
	out := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(calls))
	for _, c := range calls {
		out = append(out, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: c.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      c.Name,
					Arguments: c.Arguments,
				},
			},
		})
	}
	return out
}

// fromSDKTools converts assembled SDK tool calls into the engine's rows.
func fromSDKTools(calls []openai.ChatCompletionMessageToolCallUnion) []chatToolCall {
	out := make([]chatToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, chatToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return out
}

// chat issues ONE streaming chat-completions call and returns the assembled
// assistant turn.
// IDLE BOUND, NOT A WHOLE-GENERATION DEADLINE. Streaming is load-bearing: the
// request rides a cancellable child of the caller's ctx and a watchdog CANCELS
// that child when no chunk arrives for idleTimeout. Cancelling the ctx makes
// the transport tear the connection down, which unblocks the in-flight body
// read with an error — so a silent provider fails in bounded time while a
// slow-but-progressing one is never cut off. (resp.Body.Close() does NOT work
// here: net/http holds a mutex across the blocking read, so a Close from the
// watchdog merely queues behind it. That was RCA'd on the previous client and
// the mechanism is unchanged.)
//
// An empty completion (no content, no tool calls) is an ERROR, never a silent
// empty turn — and the diagnostic now names ollama's non-standard `reasoning`
// field when the model produced ONLY thinking, which is the case that used to
// surface as a bare "empty completion".
func chat(ctx context.Context, rc *runCtx, stage *params.StageLLMSpec, msgs []chatMsg, tools []openai.ChatCompletionToolUnionParam) (chatMsg, error) {
	return chatMessages(ctx, rc, stage, toSDKMessages(msgs), tools)
}

// chatMessages is the shared streaming driver: it takes the SDK message union
// directly, so the text path (chat) and the VISION path (chatVision) share one
// implementation of the idle bound + accumulator + empty-completion guard (R3).
func chatMessages(ctx context.Context, rc *runCtx, stage *params.StageLLMSpec, msgs []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) (chatMsg, error) {
	cfg := resolveLLM(rc, stage)

	idle := cfg.idleTimeout
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	var idleTripped atomic.Bool
	watchdog := time.AfterFunc(idle, func() {
		idleTripped.Store(true)
		cancelRead()
	})
	defer watchdog.Stop()

	svc := openai.NewChatCompletionService(cfg.clientOptions()...)
	reqOpts := cfg.extraOptions()

	stream := svc.NewStreaming(readCtx, cfg.buildParams(msgs, tools), reqOpts...)
	defer stream.Close()

	var acc openai.ChatCompletionAccumulator
	var sawChunk bool
	// reasoning accumulates ollama's NON-STANDARD `reasoning` delta field. The
	// SDK's accumulator has no typed slot for it (it is not part of OpenAI's
	// schema), so it is read per-delta from the raw JSON. Without this, a
	// reasoning-only 200 — the shape that used to surface as a bare "empty
	// completion" — would be misdiagnosed.
	var reasoningSB strings.Builder
	for stream.Next() {
		// progress: a chunk arrived — reset the IDLE bound.
		watchdog.Reset(idle)
		sawChunk = true
		chunk := stream.Current()
		for _, ch := range chunk.Choices {
			// NOTE: an UNKNOWN field is recorded with Valid()==false even when
			// present (respjson marks unexpected types invalid), so presence is
			// tested via a non-empty Raw() — NOT Valid().
			if f, ok := ch.Delta.JSON.ExtraFields["reasoning"]; ok {
				if raw := f.Raw(); raw != "" && raw != "null" {
					var s string
					if json.Unmarshal([]byte(raw), &s) == nil {
						reasoningSB.WriteString(s)
					}
				}
			}
		}
		if !acc.AddChunk(chunk) {
			// The accumulator rejected the chunk (a malformed/oversized index
			// sequence). Never silent (R1).
			return chatMsg{}, fmt.Errorf("LLM: stream chunk rejected by the accumulator (malformed tool-call/content index sequence)")
		}
	}
	// The watchdog cancels readCtx, which the transport turns into a body-read
	// error; distinguish that IDLE trip from a genuine transport error via the
	// atomic flag. A stall is NEVER silent: even a partial turn is an ERROR,
	// because a truncated completion must not be mistaken for a finished one.
	if idleTripped.Load() {
		content := 0
		if len(acc.Choices) > 0 {
			content = len(acc.Choices[0].Message.Content)
		}
		return chatMsg{}, fmt.Errorf("LLM stream stalled: no chunk for %s after %d byte(s) of content, %d byte(s) of reasoning and %d tool call(s) (provider stopped streaming); raise the llm idle_timeout (or EVAL_LLM_IDLE_TIMEOUT) for an unusually slow endpoint",
			idle, content, reasoningSB.Len(), len(acc.Choices))
	}
	if err := stream.Err(); err != nil {
		return chatMsg{}, fmt.Errorf("LLM: %w", err)
	}
	if !sawChunk {
		return chatMsg{}, fmt.Errorf("LLM: empty completion (the provider returned no stream chunks)")
	}

	out := chatMsg{Role: "assistant"}
	if len(acc.Choices) > 0 {
		m := acc.Choices[0].Message
		if m.Content != "" {
			s := m.Content
			out.Content = &s
		}
		out.ToolCalls = fromSDKTools(m.ToolCalls)
	}
	reasoning := reasoningSB.String()
	// A stream that ended with NEITHER content NOR tool calls is not a usable
	// assistant message — fail rather than return an empty turn that downstream
	// renders as nothing. Name the reasoning bytes when that is what we got:
	// a reasoning-only 200 is the real-world shape of this failure.
	if out.Content == nil && len(out.ToolCalls) == 0 {
		if reasoning != "" {
			return chatMsg{}, fmt.Errorf("LLM: empty completion (no content, no tool calls — the model emitted only %d byte(s) of reasoning; set reasoning_effort: none or raise max_tokens)", len(reasoning))
		}
		return chatMsg{}, fmt.Errorf("LLM: empty completion (no content, no tool calls)")
	}
	fmt.Printf("[chat] %s turns-so-far=%d content=%.120s tool_calls=%d reasoning=%d\n",
		cfg, len(msgs), truncate(derefString(out.Content), 120), len(out.ToolCalls), len(reasoning))
	return out, nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// envMaxTurns: the turn cap the standalone CLI path defaults to. A stage's
// authored max_turns overrides it; this is the shared default.
func envMaxTurns() int {
	if v := os.Getenv("EVAL_MAX_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 20
}

// parseInt parses a decimal string; it is the engine's strict integer reader
// (the PR number path and the batch lane-count path both use it).
func parseInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("not a number")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// intValue reads an int from an authored YAML value (float64 from JSON, or a
// bare int/string).
func intValue(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		n, _ := parseInt(t)
		return n
	}
	return 0
}

// stageLLM decodes the stage's authored `llm` override from the raw stage map
// (the CUE-validated form) into its generated type. An absent block is nil —
// resolveLLM then simply has no stage layer.
func stageLLM(raw map[string]any) *params.StageLLMSpec {
	if raw == nil {
		return nil
	}
	block, ok := raw["llm"]
	if !ok || block == nil {
		return nil
	}
	b, err := json.Marshal(block)
	if err != nil {
		return nil
	}
	var out params.StageLLMSpec
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return &out
}

// String renders the resolved endpoint identity without the secret — the
// diagnostic the standalone CLI path prints.
func (c llmConfig) String() string {
	return fmt.Sprintf("model=%s base_url=%s auth=%t", c.model, c.baseURL, c.apiKey != "")
}

// ── Vision ───────────────────────────────────────────────────────────────────
//
// A screenshot produced by a check verb (wl: / vnc: / cdp: / spice: screenshot)
// is validated with a VISION model by sending the image DIRECTLY to the
// OpenAI-compatible endpoint from charly. Ollama's OpenAI layer accepts a
// base64 data URL only (remote image URLs are explicitly unsupported), so
// imageDataURL takes raw bytes and renders `data:<mime>;base64,<...>`.

// imageDataURL renders raw image bytes as an OpenAI image_url data URL.
func imageDataURL(mime string, data []byte) string {
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// visionParts builds a multimodal user message: the prompt text plus one image
// per data URL (or remote URL, which a full OpenAI endpoint accepts even though
// ollama's compat layer does not).
func visionParts(prompt string, images []string) []openai.ChatCompletionContentPartUnionParam {
	parts := []openai.ChatCompletionContentPartUnionParam{
		openai.TextContentPart(prompt),
	}
	for _, img := range images {
		if img == "" {
			continue
		}
		parts = append(parts, openai.ImageContentPart(
			openai.ChatCompletionContentPartImageImageURLParam{URL: img},
		))
	}
	return parts
}

// chatVision sends one multimodal completion: `prompt` plus the given images
// (base64 data URLs), returning the assistant's text. This is the primitive a
// screenshot-vision check verb drives — the same idle bound, accumulator and
// empty-completion guard as the text path (chatMessages).
func chatVision(ctx context.Context, rc *runCtx, stage *params.StageLLMSpec, prompt string, images []string) (string, error) {
	msgs := []openai.ChatCompletionMessageParamUnion{openai.UserMessage(visionParts(prompt, images))}
	msg, err := chatMessages(ctx, rc, stage, msgs, nil)
	if err != nil {
		return "", err
	}
	return derefString(msg.Content), nil
}

// openaiUserPartsChat is the test seam for the vision path: it sends an
// already-built content-parts list.
func openaiUserPartsChat(ctx context.Context, rc *runCtx, parts []any) (chatMsg, error) {
	converted := make([]openai.ChatCompletionContentPartUnionParam, 0, len(parts))
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		switch pm["type"] {
		case "text":
			s, _ := pm["text"].(string)
			converted = append(converted, openai.TextContentPart(s))
		case "image_url":
			iu, _ := pm["image_url"].(map[string]any)
			url, _ := iu["url"].(string)
			converted = append(converted, openai.ImageContentPart(
				openai.ChatCompletionContentPartImageImageURLParam{URL: url},
			))
		}
	}
	return chatMessages(ctx, rc, nil, []openai.ChatCompletionMessageParamUnion{openai.UserMessage(converted)}, nil)
}
