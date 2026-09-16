package pluginpipeline

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

func startLLM(t *testing.T, seen *map[string]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var parsed struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &parsed)
		(*seen)["path"] = req.URL.Path
		(*seen)["model"] = parsed.Model
		(*seen)["auth"] = req.Header.Get("Authorization")
		writeSSEContent(rw, "ok")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestEntityLLMPrecedence: the ENV overrides beat the entity block, layer by
// layer (the operator layer wins over the lane-author layer).
func TestEntityLLMPrecedence(t *testing.T) {
	seen := map[string]string{}
	srv := startLLM(t, &seen)
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL) // the endpoint must be the test server (reachability)
	t.Setenv("EVAL_LLM_MODEL", "env-model")
	t.Setenv("EVAL_LLM_API_KEY", "env-key")
	rc := &runCtx{llm: params.LLMSpec{Model: "entity-model", Api_key: "entity-key"}}
	resp, err := runAgent(t.Context(), rc, "system", "run", []string{})
	if err != nil || resp != "ok" {
		t.Fatalf("agent: %v %v", resp, err)
	}
	if seen["model"] != "env-model" {
		t.Errorf("model: got %q - the ENV override must win", seen["model"])
	}
	if seen["auth"] != "Bearer env-key" {
		t.Errorf("auth: got %q - the ENV override must win", seen["auth"])
	}
}

// TestEntityLLMAbsentKey: with NO env key and NO entity api_key, the resolved
// key is ABSENT - the client sends NO auth header (B12 regression guard: the
// pre-fix client ALWAYS sent the header and 401'd against keyless endpoints).
func TestEntityLLMAbsentKey(t *testing.T) {
	seen := map[string]string{}
	srv := startLLM(t, &seen)
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL)
	t.Setenv("EVAL_LLM_MODEL", "entity-model") // the entity-layer model via the env passthrough
	rc := &runCtx{llm: params.LLMSpec{}}       // no api_key anywhere
	resp, err := runAgent(t.Context(), rc, "system", "run", []string{})
	if err != nil || resp != "ok" {
		t.Fatalf("agent: %v %v", resp, err)
	}
	if _, ok := seen["auth"]; ok && seen["auth"] != "" {
		t.Errorf("auth: got %q - an absent resolved key must send NO header", seen["auth"])
	}
}

// TestLLMDefaultModel: with no env override and no entity llm block, the
// built-in default (the LOCAL ollama server model) applies. FAILS without the
// deepseek-v4.1-flash:cloud default (the V4.0->V4.1 cutover).
func TestLLMDefaultModel(t *testing.T) {
	got := resolveLLM(nil, nil)
	if got.Model != "deepseek-v4.1-flash:cloud" {
		t.Errorf("default model: got %q, want \"deepseek-v4.1-flash:cloud\"", got.Model)
	}
	if got.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("default base_url: got %q, want http://localhost:11434/v1", got.BaseURL)
	}
	if got.APIKey != "" {
		t.Errorf("default api_key must be ABSENT, got %q", got.APIKey)
	}
}

// TestChatStreamsAndAssemblesToolCalls pins the STREAMING contract: chat() must
// set stream:true, read SSE deltas, and assemble a tool call from index-keyed
// delta fragments (id/name in the first, arguments in the second).
//
// This is the fix for the whole-generation 5-minute deadline that guillotined a
// slow-but-progressing reasoning model ("context deadline exceeded while awaiting
// headers" on a NON-streaming request). A non-streaming mock would make this
// test's SSE payload read as zero chunks and the call would stall-fail — so the
// test FAILS if chat() reverts to a non-streaming request.
func TestChatStreamsAndAssemblesToolCalls(t *testing.T) {
	var sawStream bool
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		sawStream, _ = body["stream"].(bool)
		writeSSEToolCall(rw, "call_1", "run_outcomes", `{"bed":"check-x"}`)
	}))
	defer srv.Close()
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL)
	t.Setenv("EVAL_LLM_MODEL", "mock")

	msg, err := chat(t.Context(), nil, nil, []chatMsg{{Role: "user", Content: strptr("hi")}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !sawStream {
		t.Fatal("chat must send stream:true — the idle bound requires SSE")
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls: got %d, want 1 (assembled from SSE deltas)", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "run_outcomes" || tc.Arguments != `{"bed":"check-x"}` {
		t.Errorf("assembled tool call = %+v, want id/name/args from the delta fragments", tc)
	}
}

func strptr(s string) *string { return &s }

// TestChatIdleWatchdogBoundsAStall pins the IDLE bound: a provider that streams
// NO chunks past EVAL_LLM_IDLE_TIMEOUT must fail in bounded time with the stall
// error, not block until the caller's ctx fires. It validates the mechanism the
// fix uses — the watchdog CANCELS the request's context, and the Transport's
// ctx-done teardown makes the blocked body Read return. (It does NOT close the
// body: resp.Body.Close() from another goroutine blocks behind the in-flight
// Read in net/http and would unblock nothing.) Mutation-verified: an inert
// watchdog makes this test HANG.
func TestChatIdleWatchdogBoundsAStall(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		if f, ok := rw.(http.Flusher); ok {
			f.Flush()
		}
		// Stream NOTHING (no chunks) until the test releases the handler — the
		// stall case. Without the idle watchdog the client blocks here forever.
		<-release
	}))
	defer srv.Close()
	defer close(release)
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL)
	t.Setenv("EVAL_LLM_MODEL", "mock")
	t.Setenv("EVAL_LLM_IDLE_TIMEOUT", "150ms")

	start := time.Now()
	_, err := chat(t.Context(), nil, nil, []chatMsg{{Role: "user", Content: strptr("hi")}}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a stalled stream must fail, got nil error")
	}
	if !strings.Contains(err.Error(), "stream stalled") {
		t.Fatalf("want the stall error, got: %v", err)
	}
	// Bounded: well under the 10s the handler would otherwise hold.
	if elapsed > 5*time.Second {
		t.Fatalf("idle watchdog did not bound the stall: %s", elapsed)
	}
}
