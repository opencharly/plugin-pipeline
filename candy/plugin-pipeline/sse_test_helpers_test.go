package pluginpipeline

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// sse_test_helpers_test.go — the OpenAI-compatible mock the agent tests drive.
//
// The engine's wire client is now the official openai-go SDK, so a mock server
// must speak the SSE shape the SDK's decoder expects (a `data:` line per JSON
// chunk, terminated by `data: [DONE]`). These helpers emit that shape; every
// payload carries the fields the SDK requires (id/object/created/model/choices)
// so a chunk is never rejected as malformed.
//
// The captured request body is what the conformance tests assert on — the
// authored #LLMParams are proven by what actually arrived at the server.

// sseChunk builds one SDK-shaped chat-completion chunk carrying a delta.
func sseChunk(delta map[string]any) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 1700000000,
		"model":   "test-model",
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": nil,
		}},
	}
}

// writeSSEContent emits a minimal SSE chat-completions stream carrying `content`
// as ONE content delta, then the terminal [DONE].
func writeSSEContent(rw http.ResponseWriter, content string) {
	sseStart(rw)
	writeSSEChunk(rw, sseChunk(map[string]any{"role": "assistant", "content": content}))
	sseDone(rw)
}

// writeSSEReasoningOnly emits a stream whose ONLY delta is ollama's
// non-standard `reasoning` field — the real-world reasoning-model 200 that must
// be reported as a named empty completion, not a bare one.
func writeSSEReasoningOnly(rw http.ResponseWriter, reasoning string) {
	sseStart(rw)
	writeSSEChunk(rw, sseChunk(map[string]any{"role": "assistant", "reasoning": reasoning}))
	sseDone(rw)
}

// writeSSEEmpty emits a 200 SSE stream with NO content and NO tool calls at all
// (a filtered/empty turn).
func writeSSEEmpty(rw http.ResponseWriter) {
	sseStart(rw)
	writeSSEChunk(rw, sseChunk(map[string]any{"role": "assistant"}))
	sseDone(rw)
}

// writeSSERaw emits an arbitrary raw SSE payload (for tests that need a
// tool-call delta stream or a malformed-chunk case).
func writeSSERaw(rw http.ResponseWriter, payloads ...string) {
	sseStart(rw)
	for _, p := range payloads {
		fmt.Fprintf(rw, "data: %s\n\n", p)
		flush(rw)
	}
	sseDone(rw)
}

// writeSSEToolCall emits one tool-call delta (id + name + arguments assembled
// by index) — the SSE shape the SDK's accumulator decodes.
func writeSSEToolCall(rw http.ResponseWriter, id, name, args string) {
	deltas := []map[string]any{
		sseChunk(map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"index": 0, "id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": ""}},
		}}),
		sseChunk(map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "function": map[string]any{"arguments": args}},
		}}),
	}
	payloads := make([]string, 0, len(deltas))
	for _, d := range deltas {
		b, _ := json.Marshal(d)
		payloads = append(payloads, string(b))
	}
	writeSSERaw(rw, payloads...)
}

// writeSSEContentThenToolCall emits prose followed by a tool call — the shape a
// model uses when it narrates and then calls a tool.
func writeSSEContentThenToolCall(rw http.ResponseWriter, content, id, name, args string) {
	deltas := []map[string]any{
		sseChunk(map[string]any{"role": "assistant", "content": content}),
		sseChunk(map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": args}},
		}}),
	}
	payloads := make([]string, 0, len(deltas))
	for _, d := range deltas {
		b, _ := json.Marshal(d)
		payloads = append(payloads, string(b))
	}
	writeSSERaw(rw, payloads...)
}

func sseStart(rw http.ResponseWriter) {
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(http.StatusOK)
	flush(rw)
}

func sseDone(rw http.ResponseWriter) {
	fmt.Fprint(rw, "data: [DONE]\n\n")
	flush(rw)
}

func writeSSEChunk(rw http.ResponseWriter, chunk map[string]any) {
	b, _ := json.Marshal(chunk)
	fmt.Fprintf(rw, "data: %s\n\n", b)
	flush(rw)
}

func flush(rw http.ResponseWriter) {
	if f, ok := rw.(http.Flusher); ok {
		f.Flush()
	}
}
