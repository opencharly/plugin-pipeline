package pluginpipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
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
}
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   *string    `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// llmConfig resolves the LLM endpoint. Precedence: the ENV overrides
// (EVAL_LLM_BASE_URL / EVAL_LLM_MODEL / EVAL_LLM_API_KEY - the operator layer)
// > the entity's authored llm block (the lane-author layer) > the built-in
// default (the LOCAL ollama server with deepseek-v4-flash:cloud). An empty
// key means ABSENT: the client sends NO auth header (local ollama needs none).
func llmBaseURL(rc *runCtx) string {
	if rc != nil && rc.llm != nil {
		if v, ok := rc.llm["base_url"].(string); ok && v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	if v := os.Getenv("EVAL_LLM_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := os.Getenv("AI_REVIEW_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://localhost:11434/v1"
}
func llmModel(rc *runCtx) string {
	if rc != nil && rc.llm != nil {
		if v, ok := rc.llm["model"].(string); ok && v != "" {
			return v
		}
	}
	if v := os.Getenv("EVAL_LLM_MODEL"); v != "" {
		return v
	}
	if v := os.Getenv("AI_REVIEW_MODEL"); v != "" {
		return v
	}
	return "deepseek-v4-flash:cloud"
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
	return os.Getenv("AI_REVIEW_API_KEY")
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

func chat(ctx context.Context, rc *runCtx, msgs []chatMsg, tools []toolSchema) (chatMsg, error) {
	body, err := json.Marshal(chatRequest{Model: llmModel(rc), Messages: msgs, Temperature: 0.2, Tools: tools, ToolChoice: "auto"})
	if err != nil {
		return chatMsg{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, llmBaseURL(rc)+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return chatMsg{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k := llmAPIKey(rc); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	req.Header.Set("HTTP-Referer", "https://github.com/opencharly/plugin-pipeline")
	req.Header.Set("X-Title", "plugin-pipeline")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return chatMsg{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return chatMsg{}, fmt.Errorf("LLM %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return chatMsg{}, err
	}
	if len(cr.Choices) == 0 {
		return chatMsg{}, fmt.Errorf("LLM: no choices")
	}
	m := cr.Choices[0].Message
	return chatMsg{Role: "assistant", Content: m.Content, ToolCalls: m.ToolCalls}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// runAgent — the P1 runtime (standalone CLI + the agent stage).
func runAgent(ctx context.Context, rc *runCtx, systemPrompt, prompt string, tools []string) (string, error) {
	sys := resolveSkills(systemPrompt) // skills appended (skills.go)
	msgs := []chatMsg{{Role: "system", Content: &sys}, {Role: "user", Content: &prompt}}
	toolSch := buildTools(tools)
	for turn := 0; turn < envMaxTurns(); turn++ {
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
		if turn == envMaxTurns()-1 {
			// forced termination: the last assistant content is the answer; when
			// the transcript is ALL tool calls (no prose), synthesize a fallback
			// so the stage still completes with a gradeable response.
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == "assistant" && msgs[i].Content != nil && *msgs[i].Content != "" {
					return *msgs[i].Content, nil
				}
			}
			return "[budget exhausted: the agent spent its " + strconv.Itoa(envMaxTurns()) + " turns on tool calls without a prose verdict — grading from the evidence packet is partial; the check-run results remain authoritative]", nil
		}
		for _, tc := range msg.ToolCalls {
			res := dispatchTool(tc.Function.Name, tc.Function.Arguments)
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

// runAgentStage: the plan agent stage.
func runAgentStage(ctx context.Context, rc *runCtx, raw map[string]any, l *ledger) (map[string]any, error) {
	sys := asString(raw["prompt"])
	if rc != nil {
		sys = rc.resolveRefs(sys)
	}
	// stage-level turn cap (raw max_turns) overrides the env default; the
	// stage prompt can then enforce the model's own termination.
	mt := 0
	if raw != nil {
		mt = intValue(raw["max_turns"])
	}
	if mt > 0 {
		prev := os.Getenv("EVAL_MAX_TURNS")
		_ = os.Setenv("EVAL_MAX_TURNS", strconv.Itoa(mt))
		defer func() { _ = os.Setenv("EVAL_MAX_TURNS", prev) }()
	}
	resp, err := runAgent(ctx, rc, sys, "Run this stage per your instructions.", strList(raw["tools"]))
	if err != nil {
		return map[string]any{}, err
	}
	out := map[string]any{"response": resp}
	for _, name := range strList(raw["outputs"]) {
		out[name] = extractJSON(resp, name)
	}
	return out, nil
}

// extractJSON: pull "key": value from a JSON-ish agent response.
// "plan-json" is the whole-plan special case: the agent's single JSON object
// (the Config-Oracle contract) — return it as a map for @stage.plan-json.field refs.
func extractJSON(resp, key string) any {
	if key == "plan-json" {
		start := strings.Index(resp, "{")
		if start < 0 {
			return resp
		}
		depth := 0
		for i := start; i < len(resp); i++ {
			switch resp[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					var v any
					if err := json.Unmarshal([]byte(resp[start:i+1]), &v); err == nil {
						return v
					}
					return resp
				}
			}
		}
		return resp
	}
	idx := strings.Index(resp, "\""+key+"\"")
	if idx < 0 {
		return resp
	}
	rest := resp[idx+len("\""+key+"\""):]
	rest = strings.TrimLeft(rest, " :")
	depth := 0
	for i, r := range rest {
		switch r {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',', '\n':
			if depth == 0 {
				var v any
				_ = json.Unmarshal([]byte(rest[:i]), &v)
				return v
			}
		}
	}
	var v any
	_ = json.Unmarshal([]byte(rest), &v)
	return v
}
