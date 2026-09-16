package pluginpipeline

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk/llmkit"
)

// llm_conformance_test.go — the proof that the authored #LLMSpec/#LLMParams
// actually reach the wire, one test per feature the ollama OpenAI-compatible
// API supports (chat, streaming, tools, vision, reasoning, json mode, sampling
// knobs, usage) plus the behaviors the SDK swap MUST NOT regress.

// captureLLM starts a mock OpenAI endpoint, handing the decoded request body to
// assert, and replies with the given SSE writer.
func captureLLM(t *testing.T, reply func(rw http.ResponseWriter), assert func(t *testing.T, body map[string]any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/chat/completions" {
			t.Errorf("bad path %s", req.URL.Path)
		}
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v (%s)", err, raw)
		}
		if assert != nil {
			assert(t, body)
		}
		reply(rw)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runChat drives one chat() call against the mock with the given rc/stage and
// asserts the reply is a text turn.
func runChat(t *testing.T, rc *runCtx, stage *params.StageLLMSpec, srv *httptest.Server, want string) chatMsg {
	t.Helper()
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL)
	msg, err := chat(t.Context(), rc, stage, []chatMsg{{Role: "user", Content: strptr("hi")}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if want != "" && (msg.Content == nil || *msg.Content != want) {
		t.Fatalf("content: got %v, want %q", msg.Content, want)
	}
	return msg
}

// ---- chat / streaming -------------------------------------------------------

// TestConformance_TextStreaming: a plain streaming text completion round-trips,
// and the request is a streaming one (the idle bound depends on SSE).
func TestConformance_TextStreaming(t *testing.T) {
	srv := captureLLM(t, func(rw http.ResponseWriter) { writeSSEContent(rw, "hello world") },
		func(t *testing.T, body map[string]any) {
			if body["stream"] != true {
				t.Errorf("stream must be true, got %v", body["stream"])
			}
			if body["model"] == nil {
				t.Errorf("model must be sent")
			}
		})
	runChat(t, nil, nil, srv, "hello world")
}

// TestConformance_ToolCallRoundTrip: a tool call is assembled from index-keyed
// SSE fragments and dispatched, and the tool result is fed back as a `tool`
// message on the next turn.
func TestConformance_ToolCallRoundTrip(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		turn++
		if turn == 1 {
			writeSSEToolCall(rw, "call_1", "get_pr_meta", `{}`)
			return
		}
		// second turn: the transcript must carry the assistant tool_call and the
		// tool result row.
		msgs, _ := body["messages"].([]any)
		var sawToolResult bool
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if mm["role"] == "tool" {
				sawToolResult = true
				if mm["tool_call_id"] != "call_1" {
					t.Errorf("tool result must carry the call id, got %v", mm["tool_call_id"])
				}
			}
		}
		if !sawToolResult {
			t.Errorf("the second turn must carry the tool result row")
		}
		writeSSEContent(rw, "done")
	}))
	defer srv.Close()
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL)
	t.Setenv("EVAL_LLM_MODEL", "mock")

	resp, err := runAgent(t.Context(), nil, "sys", "go", []string{"pr"})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	if resp != "done" {
		t.Fatalf("resp = %q, want done", resp)
	}
}

// TestConformance_Vision: a user message carrying an image arrives as an
// OpenAI content-parts array with an image_url data URL (ollama supports base64
// data URLs only — no remote image URLs).
func TestConformance_Vision(t *testing.T) {
	srv := captureLLM(t, func(rw http.ResponseWriter) { writeSSEContent(rw, "seen") },
		func(t *testing.T, body map[string]any) {
			msgs, _ := body["messages"].([]any)
			if len(msgs) == 0 {
				t.Fatal("no messages")
			}
			user, _ := msgs[0].(map[string]any)
			parts, ok := user["content"].([]any)
			if !ok {
				t.Fatalf("user content must be a content-parts array, got %T", user["content"])
			}
			var sawText, sawImage bool
			for _, p := range parts {
				pm, _ := p.(map[string]any)
				switch pm["type"] {
				case "text":
					sawText = true
				case "image_url":
					sawImage = true
					iu, _ := pm["image_url"].(map[string]any)
					url, _ := iu["url"].(string)
					if !strings.HasPrefix(url, "data:image/png;base64,") {
						t.Errorf("image must be a base64 data URL, got %q", url)
					}
				}
			}
			if !sawText || !sawImage {
				t.Errorf("content parts must carry text AND image_url (text=%v image=%v)", sawText, sawImage)
			}
		})

	// The vision path goes through the SHARED client's ChatVision (the engine's
	// chatVision is a thin adapter over it), which sends exactly this shape.
	img := llmkit.ImageDataURL("image/png", []byte("PNGDATA"))
	cfg := llmkit.Default()
	cfg.BaseURL = srv.URL
	if _, err := llmkit.ChatVision(t.Context(), cfg, "what is in this screenshot?", []string{img}); err != nil {
		t.Fatalf("vision chat: %v", err)
	}
}

// ---- reasoning / empty completion ------------------------------------------

// TestConformance_ReasoningOnlyIsNamedEmpty: a 200 whose ONLY delta is ollama's
// non-standard `reasoning` field must fail with the NAMED reasoning diagnostic,
// not a bare "empty completion". This is the regression that surfaced as the
// intermittent validate-stage FAIL-HARD.
func TestConformance_ReasoningOnlyIsNamedEmpty(t *testing.T) {
	srv := captureLLM(t, func(rw http.ResponseWriter) { writeSSEReasoningOnly(rw, strings.Repeat("think ", 20)) }, nil)
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL)
	t.Setenv("EVAL_LLM_MODEL", "mock")

	_, err := chat(t.Context(), nil, nil, []chatMsg{{Role: "user", Content: strptr("hi")}}, nil)
	if err == nil {
		t.Fatal("a reasoning-only 200 must fail, not return an empty turn")
	}
	if !strings.Contains(err.Error(), "only") || !strings.Contains(err.Error(), "reasoning") {
		t.Fatalf("the diagnostic must NAME the reasoning output, got: %v", err)
	}
}

// TestConformance_EmptyCompletionErrors: a 200 with neither content nor tool
// calls fails (never a silent empty turn).
func TestConformance_EmptyCompletionErrors(t *testing.T) {
	srv := captureLLM(t, func(rw http.ResponseWriter) { writeSSEEmpty(rw) }, nil)
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL)
	t.Setenv("EVAL_LLM_MODEL", "mock")

	_, err := chat(t.Context(), nil, nil, []chatMsg{{Role: "user", Content: strptr("hi")}}, nil)
	if err == nil {
		t.Fatal("an empty 200 must fail")
	}
	if !strings.Contains(err.Error(), "empty completion") {
		t.Fatalf("want the empty-completion error, got: %v", err)
	}
}

// ---- the full authored parameter surface -----------------------------------

// TestConformance_AllParamsReachTheWire is the central "fully configurable"
// proof: every authored #LLMParams field arrives at the server under its exact
// wire name.
func TestConformance_AllParamsReachTheWire(t *testing.T) {
	temp, topP := 0.3, 0.8
	maxTok, maxComp, seed, topLog := int64(1234), int64(5678), int64(42), int64(3)
	freq, pres := 0.1, 0.2
	parallel, logprobs := true, false

	rc := &runCtx{llm: params.LLMSpec{Params: params.LLMParams{
		Temperature:           &temp,
		Top_p:                 &topP,
		Max_tokens:            &maxTok,
		Max_completion_tokens: &maxComp,
		Frequency_penalty:     &freq,
		Presence_penalty:      &pres,
		Seed:                  &seed,
		Stop:                  []any{"A", "B"},
		Reasoning_effort:      "high",
		Stream_options:        params.StreamOptions{Include_usage: openaiBoolPtr(true)},
		Parallel_tool_calls:   &parallel,
		Logprobs:              &logprobs,
		Top_logprobs:          &topLog,
		User:                  "u-1",
		Metadata:              map[string]string{"k": "v"},
		Logit_bias:            map[string]int64{"5": 1},
		Response_format:       params.ResponseFormat{Type: "json_object"},
		Tool_choice:           "required",
		Extra:                 map[string]any{"custom_knob": 7},
	}}}

	srv := captureLLM(t, func(rw http.ResponseWriter) { writeSSEContent(rw, "ok") },
		func(t *testing.T, body map[string]any) {
			want := map[string]any{
				"temperature":           0.3,
				"top_p":                 0.8,
				"max_tokens":            float64(1234),
				"max_completion_tokens": float64(5678),
				"frequency_penalty":     0.1,
				"presence_penalty":      0.2,
				"seed":                  float64(42),
				"reasoning_effort":      "high",
				"parallel_tool_calls":   true,
				"logprobs":              false,
				"top_logprobs":          float64(3),
				"user":                  "u-1",
				"custom_knob":           float64(7),
			}
			for k, v := range want {
				if body[k] != v {
					t.Errorf("%s: got %#v, want %#v", k, body[k], v)
				}
			}
			if rf, _ := body["response_format"].(map[string]any); rf["type"] != "json_object" {
				t.Errorf("response_format.type: got %v, want json_object", rf)
			}
			if tc := body["tool_choice"]; tc != "required" {
				t.Errorf("tool_choice: got %v, want required", tc)
			}
			if so, _ := body["stream_options"].(map[string]any); so["include_usage"] != true {
				t.Errorf("stream_options.include_usage must reach the wire, got %v", body["stream_options"])
			}
			if stops, _ := body["stop"].([]any); len(stops) != 2 {
				t.Errorf("stop list must reach the wire, got %v", body["stop"])
			}
			if md, _ := body["metadata"].(map[string]any); md["k"] != "v" {
				t.Errorf("metadata must reach the wire, got %v", body["metadata"])
			}
			if lb, _ := body["logit_bias"].(map[string]any); lb["5"] != float64(1) {
				t.Errorf("logit_bias must reach the wire, got %v", body["logit_bias"])
			}
		})
	runChat(t, rc, nil, srv, "ok")
}

// TestConformance_JsonSchemaResponseFormat: a json_schema response_format is
// rendered as the OpenAI json_schema object (not just json_object).
func TestConformance_JsonSchemaResponseFormat(t *testing.T) {
	rc := &runCtx{llm: params.LLMSpec{Params: params.LLMParams{
		Response_format: params.ResponseFormat{
			Type: "json_schema",
			Json_schema: struct {
				Name        string         `json:"name"`
				Description string         `json:"description,omitempty"`
				Schema      map[string]any `json:"schema"`
				Strict      bool           `json:"strict,omitempty"`
			}{Name: "verdict", Description: "d", Schema: map[string]any{"type": "object"}, Strict: true},
		},
	}}}
	srv := captureLLM(t, func(rw http.ResponseWriter) { writeSSEContent(rw, "{}") },
		func(t *testing.T, body map[string]any) {
			rf, _ := body["response_format"].(map[string]any)
			if rf["type"] != "json_schema" {
				t.Fatalf("response_format.type = %v, want json_schema", rf["type"])
			}
			js, _ := rf["json_schema"].(map[string]any)
			if js["name"] != "verdict" || js["strict"] != true {
				t.Errorf("json_schema block = %v, want name=verdict strict=true", js)
			}
			if _, ok := js["schema"].(map[string]any); !ok {
				t.Errorf("json_schema.schema must be an object, got %v", js["schema"])
			}
		})
	runChat(t, rc, nil, srv, "{}")
}

// ---- precedence / isolation ------------------------------------------------

// TestConformance_StageOverridesEntityFieldWise: a stage override wins for the
// field it sets and leaves the entity's other fields intact.
func TestConformance_StageOverridesEntityFieldWise(t *testing.T) {
	temp, topP := 0.9, 0.5
	stageTemp := 0.1
	rc := &runCtx{llm: params.LLMSpec{Model: "entity-model", Params: params.LLMParams{
		Temperature: &temp, Top_p: &topP,
	}}}
	stage := &params.StageLLMSpec{
		Model:  "stage-model",
		Params: params.LLMParams{Temperature: &stageTemp},
	}
	got, err := resolveLLM(rc, stage)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "stage-model" {
		t.Errorf("model: stage must win, got %q", got.Model)
	}
	if got.Params.Temperature == nil || *got.Params.Temperature != 0.1 {
		t.Errorf("temperature: stage must win, got %v", got.Params.Temperature)
	}
	if got.Params.Top_p == nil || *got.Params.Top_p != 0.5 {
		t.Errorf("top_p: the entity value must survive a stage override of another field, got %v", got.Params.Top_p)
	}
}

// TestConformance_EnvBeatsStageAndEntity: the env layer still wins over both
// authored layers (the documented operator override).
func TestConformance_EnvBeatsStageAndEntity(t *testing.T) {
	t.Setenv("EVAL_LLM_MODEL", "env-model")
	rc := &runCtx{llm: params.LLMSpec{Model: "entity-model"}}
	stage := &params.StageLLMSpec{Model: "stage-model"}
	resolved, err := resolveLLM(rc, stage)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Model; got != "env-model" {
		t.Fatalf("model: env must win, got %q", got)
	}
}

// TestConformance_NoOpenAIEnvBleed: a stray operator OPENAI_API_KEY /
// OPENAI_BASE_URL must NOT hijack a lane. The client is built from
// NewChatCompletionService (which applies ONLY the given options), never
// NewClient (which prepends the OPENAI_*-reading defaults).
func TestConformance_NoOpenAIEnvBleed(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "polluted-key")
	t.Setenv("OPENAI_BASE_URL", "http://polluted.invalid/v1")

	var gotAuth, gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotHost = req.Host
		writeSSEContent(rw, "clean")
	}))
	defer srv.Close()
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL)
	// no api_key authored anywhere -> NO Authorization header must be sent.
	msg, err := chat(t.Context(), nil, nil, []chatMsg{{Role: "user", Content: strptr("hi")}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if msg.Content == nil || *msg.Content != "clean" {
		t.Fatalf("content = %v", msg.Content)
	}
	if gotAuth != "" {
		t.Errorf("OPENAI_API_KEY leaked into the request: Authorization=%q", gotAuth)
	}
	if strings.Contains(gotHost, "polluted") {
		t.Errorf("OPENAI_BASE_URL leaked into the request: host=%q", gotHost)
	}
}

// TestConformance_HeadersReachTheWire: authored custom headers are applied.
func TestConformance_HeadersReachTheWire(t *testing.T) {
	var gotReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		gotReferer = req.Header.Get("HTTP-Referer")
		writeSSEContent(rw, "ok")
	}))
	defer srv.Close()
	rc := &runCtx{llm: params.LLMSpec{Headers: map[string]string{"HTTP-Referer": "https://example.test"}}}
	runChat(t, rc, nil, srv, "ok")
	if gotReferer != "https://example.test" {
		t.Errorf("custom header not applied, got %q", gotReferer)
	}
}

// TestConformance_TimeoutAndRetriesAreConfigurable: the connection knobs from
// #LLMSpec resolve as authored.
func TestConformance_TimeoutAndRetriesAreConfigurable(t *testing.T) {
	retries := int64(0)
	rc := &runCtx{llm: params.LLMSpec{
		Timeout:      "90s",
		Idle_timeout: "45s",
		Max_retries:  &retries,
	}}
	got, err := resolveLLM(rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timeout.String() != "1m30s" {
		t.Errorf("timeout: got %s, want 1m30s", got.Timeout)
	}
	if got.IdleTimeout.String() != "45s" {
		t.Errorf("idle_timeout: got %s, want 45s", got.IdleTimeout)
	}
	if got.MaxRetries != 0 {
		t.Errorf("max_retries: got %d, want 0", got.MaxRetries)
	}
}

// TestConformance_ExtraEscapeHatchIsOrdered: the `extra` map reaches the wire as
// documented request fields (the ONLY legal unknown-key path).
func TestConformance_ExtraEscapeHatchIsOrdered(t *testing.T) {
	rc := &runCtx{llm: params.LLMSpec{Params: params.LLMParams{
		Extra: map[string]any{"z_knob": 1, "a_knob": "x"},
	}}}
	srv := captureLLM(t, func(rw http.ResponseWriter) { writeSSEContent(rw, "ok") },
		func(t *testing.T, body map[string]any) {
			if body["a_knob"] != "x" || body["z_knob"] != float64(1) {
				t.Errorf("extra fields must reach the wire, got %v / %v", body["a_knob"], body["z_knob"])
			}
		})
	runChat(t, rc, nil, srv, "ok")
}

// openaiBoolPtr is the test-local bool pointer helper (the generated param
// fields are *bool).
func openaiBoolPtr(b bool) *bool { return &b }

// TestToSpecLLM_PropagatesErrors: the bridge must NEVER silently degrade to a zero
// config — a failed conversion would drop the entire authored llm: block and let
// the lane fall back to defaults (the silent-config-failure class R1 forbids).
// A value json.Marshal cannot encode (a channel) is the honest failure path.
func TestToSpecLLM_PropagatesErrors(t *testing.T) {
	if _, err := toSpecLLM(make(chan int)); err == nil {
		t.Fatal("an unencodable value must return an error, never a zero spec.LLMSpec")
	}
	// the happy path still works for both caller shapes
	e, err := toSpecLLM(params.LLMSpec{Model: "m"})
	if err != nil || e.Model != "m" {
		t.Fatalf("entity bridge: %v model=%q", err, e.Model)
	}
	st, err := toSpecLLM(params.StageLLMSpec{Model: "s"})
	if err != nil || st.Model != "s" {
		t.Fatalf("stage bridge: %v model=%q", err, st.Model)
	}
}
