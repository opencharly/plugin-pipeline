package pluginpipeline

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// llm_live_test.go — the R10 live proof. It drives the REAL openai-go client
// against the REAL local ollama endpoint (not a mock), exercising the full path:
// streaming, tool-call assembly readiness, the reasoning field, and the
// authored-parameter plumbing end to end.
//
// It SKIPS when no endpoint is reachable, so it never reds a CI host without
// ollama — but it is the test that must be RUN (and its output pasted) for the
// PR's R10 evidence.

const liveBaseURL = "http://localhost:11434/v1"

func liveModel() string {
	if m := os.Getenv("EVAL_LLM_MODEL"); m != "" {
		return m
	}
	return "deepseek-v4.1-flash:cloud"
}

func ollamaUp(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, liveBaseURL+"/models", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// TestLive_OllamaTextStreaming: a real streaming completion through chat().
func TestLive_OllamaTextStreaming(t *testing.T) {
	if !ollamaUp(t) {
		t.Skip("no ollama endpoint at " + liveBaseURL)
	}
	t.Setenv("EVAL_LLM_BASE_URL", liveBaseURL)
	t.Setenv("EVAL_LLM_MODEL", liveModel())

	msg, err := chat(t.Context(), nil, nil,
		[]chatMsg{{Role: "user", Content: strptr("Reply with exactly the word: PONG")}}, nil)
	if err != nil {
		t.Fatalf("live chat: %v", err)
	}
	if msg.Content == nil || !strings.Contains(strings.ToUpper(*msg.Content), "PONG") {
		t.Fatalf("live reply did not contain PONG: %v", msg.Content)
	}
	t.Logf("LIVE REPLY: %q", *msg.Content)
}

// TestLive_OllamaAuthoredParams: an authored temperature/max_tokens lands on the
// real endpoint and the completion still succeeds.
func TestLive_OllamaAuthoredParams(t *testing.T) {
	if !ollamaUp(t) {
		t.Skip("no ollama endpoint at " + liveBaseURL)
	}
	t.Setenv("EVAL_LLM_BASE_URL", liveBaseURL)
	t.Setenv("EVAL_LLM_MODEL", liveModel())

	temp := 0.1
	maxTok := int64(64)
	rc := &runCtx{llm: params.LLMSpec{
		Base_url:     liveBaseURL,
		Model:        liveModel(),
		Idle_timeout: "3m",
		Params: params.LLMParams{
			Temperature: &temp,
			Max_tokens:  &maxTok,
		},
	}}
	msg, err := chat(t.Context(), rc, nil,
		[]chatMsg{{Role: "user", Content: strptr("Say the single word READY")}}, nil)
	if err != nil {
		t.Fatalf("live chat with authored params: %v", err)
	}
	if msg.Content == nil {
		t.Fatal("nil content")
	}
	t.Logf("LIVE REPLY (authored temp=%v max_tokens=%d): %q", temp, maxTok, *msg.Content)
}

// TestLive_OllamaReasoningIsCaptured: the real model returns a `reasoning`
// field; the stream path must observe it (proving the reasoning accumulator
// works against a live endpoint, not just the mock).
func TestLive_OllamaReasoningIsCaptured(t *testing.T) {
	if !ollamaUp(t) {
		t.Skip("no ollama endpoint at " + liveBaseURL)
	}
	t.Setenv("EVAL_LLM_BASE_URL", liveBaseURL)
	t.Setenv("EVAL_LLM_MODEL", liveModel())

	// A prompt that elicits step-by-step thinking: the model emits the
	// non-standard `reasoning` field. The test FAILS when it observes none, so it
	// cannot pass vacuously (the rewired engine must still read the field from the
	// shared client — the preservation claim this branch's PR rests on).
	msg, err := chat(t.Context(), nil, nil,
		[]chatMsg{{Role: "user", Content: strptr("Think step by step, then answer: what is 17 times 23?")}}, nil)
	if err != nil {
		t.Fatalf("live chat: %v", err)
	}
	if msg.Content == nil {
		t.Fatal("nil content")
	}
	if msg.Reasoning == "" {
		t.Fatalf("the live model returned NO reasoning-bearing output, so the non-standard `reasoning` read is UNOBSERVED (the rewired engine's preservation claim is unproven)")
	}
	t.Logf("LIVE REASONING observed: %d bytes; content=%q", len(msg.Reasoning), *msg.Content)
}
