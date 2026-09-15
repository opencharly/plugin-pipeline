package pluginpipeline

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// writeSSEContent emits a minimal SSE chat-completions stream carrying `content`
// as ONE content delta, then the terminal [DONE]. The pipeline's chat() now
// STREAMS (see chatIdleTimeout), so a test mock must speak SSE — a plain JSON
// body would be read as no chunks and the idle watchdog would treat it as a
// stall. This is the shared helper the mock servers use.
func writeSSEContent(rw http.ResponseWriter, content string) {
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(http.StatusOK)
	flusher, _ := rw.(http.Flusher)
	delta := map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"content": content},
	}}}
	b, _ := json.Marshal(delta)
	fmt.Fprintf(rw, "data: %s\n\n", b)
	if flusher != nil {
		flusher.Flush()
	}
	fmt.Fprint(rw, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// writeSSERaw emits an arbitrary raw SSE payload (for tests that need a
// tool-call delta stream or a malformed-chunk case).
func writeSSERaw(rw http.ResponseWriter, payloads ...string) {
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(http.StatusOK)
	flusher, _ := rw.(http.Flusher)
	for _, p := range payloads {
		fmt.Fprintf(rw, "data: %s\n\n", p)
		if flusher != nil {
			flusher.Flush()
		}
	}
	fmt.Fprint(rw, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// writeSSEToolCall emits one tool-call delta (id + name + arguments assembled by
// index), the SSE shape chat() decodes.
func writeSSEToolCall(rw http.ResponseWriter, id, name, args string) {
	deltas := []map[string]any{
		{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": ""}},
		}}}}},
		{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "function": map[string]any{"arguments": args}},
		}}}}},
	}
	payloads := make([]string, 0, len(deltas))
	for _, d := range deltas {
		b, _ := json.Marshal(d)
		payloads = append(payloads, string(b))
	}
	writeSSERaw(rw, payloads...)
}
