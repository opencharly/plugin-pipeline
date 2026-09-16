package pluginpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/openai/openai-go/v3"
	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk/llmkit"
	"github.com/opencharly/spec/spec"
)

// errNotANumber is the sentinel the strict integer reader returns.
var errNotANumber = errors.New("not a number")

// llm.go — the engine's thin adapter over the SHARED SDK client (llmkit).
//
// The OpenAI-compatible client is NOT implemented here any more (R3): it lives in
// github.com/opencharly/sdk/llmkit, the ONE canonical client, shared with the
// `vision:` check verb (candy/plugin-vision) so both speak the endpoint
// identically — the same idle bound, the same non-standard `reasoning` capture,
// the same empty-completion guard, the same no-env-bleed construction, the same
// vision content parts. This file keeps ONLY what is engine-specific:
//
//   - the LAYER RESOLUTION the engine documents (entity block -> stage override ->
//     the EVAL_LLM_* operator env -> the built-in default), expressed against
//     llmkit.Config;
//   - the transcript types (aliases of llmkit's, so the turn loop reads naturally);
//   - the stage-level `llm:` decode and the small integer readers.
//
// The transcript aliases are load-bearing: the agent turn loop appends llmkit
// messages and the tool dispatcher keys off ToolCall{ID,Name,Arguments}, so the
// aliases keep agent.go unchanged while the storage is the shared type.

// chatMsg / chatToolCall are the shared client's transcript types.
type (
	chatMsg      = llmkit.Message
	chatToolCall = llmkit.ToolCall
)

// resolveLLM applies the engine's documented precedence FIELD-WISE: a lower layer
// fills only what the higher layers left unset (env > stage > entity > built-in
// default). stage may be nil (the standalone CLI path). The entity's `llm:` block
// comes from the authored #LLMSpec; the stage's `llm:` is applied over it; the
// EVAL_LLM_* env then wins (the operator layer).
//
// An empty RESOLVED api_key means ABSENT: no Authorization header is sent, so a
// missing secret can never zero out a lane — and the local ollama needs none.
func resolveLLM(rc *runCtx, stage *params.StageLLMSpec) (llmkit.Config, error) {
	cfg := llmkit.Default()
	if rc != nil {
		// layer 3 — the entity's authored llm: block
		e, err := toSpecLLM(rc.llm)
		if err != nil {
			return llmkit.Config{}, fmt.Errorf("llm: entity block: %w", err)
		}
		cfg = cfg.Apply(e)
	}
	if stage != nil { // layer 2 — the stage-local override
		s, err := toSpecLLM(*stage)
		if err != nil {
			return llmkit.Config{}, fmt.Errorf("llm: stage override: %w", err)
		}
		cfg = cfg.Apply(s)
	}
	return cfg.FromEnv(), nil // layer 1 — the operator env
}

// toSpecLLM bridges a generated plugin llm value (the entity's params.LLMSpec or
// a stage's params.StageLLMSpec) onto the contract spec.LLMSpec the shared client
// consumes. The two shapes are the same CUE vocabulary, so the JSON round-trip is
// exact; ONE helper serves both callers (R3 — the value/pointer difference is the
// caller's, not a reason for a second copy).
//
// The error is PROPAGATED, never swallowed: a failed conversion would silently
// drop the entire authored llm: block and let the lane fall back to defaults —
// exactly the silent-config-failure class R1 forbids.
func toSpecLLM(v any) (spec.LLMSpec, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return spec.LLMSpec{}, fmt.Errorf("marshal: %w", err)
	}
	var out spec.LLMSpec
	if err := json.Unmarshal(b, &out); err != nil {
		return spec.LLMSpec{}, fmt.Errorf("unmarshal: %w", err)
	}
	return out, nil
}

// chat issues ONE streaming completion through the shared client.
func chat(ctx context.Context, rc *runCtx, stage *params.StageLLMSpec, msgs []chatMsg, tools []openai.ChatCompletionToolUnionParam) (chatMsg, error) {
	cfg, err := resolveLLM(rc, stage)
	if err != nil {
		return chatMsg{}, err
	}
	out, err := llmkit.Chat(ctx, cfg, llmkit.ToSDKMessages(msgs), tools)
	if err != nil {
		return chatMsg{}, err
	}
	fmt.Printf("[chat] model=%s base_url=%s turns-so-far=%d content=%.120s tool_calls=%d reasoning=%d\n",
		cfg.Model, cfg.BaseURL, len(msgs), truncate(derefString(out.Content), 120), len(out.ToolCalls), len(out.Reasoning))
	return out, nil
}

// chatVision sends one multimodal completion through the shared client.
func chatVision(ctx context.Context, rc *runCtx, stage *params.StageLLMSpec, prompt string, images []string) (string, error) {
	cfg, err := resolveLLM(rc, stage)
	if err != nil {
		return "", err
	}
	return llmkit.ChatVision(ctx, cfg, prompt, images)
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

// parseInt parses a decimal string; the engine's strict integer reader (the PR
// number path and the batch lane-count path both use it).
func parseInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errNotANumber
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errNotANumber
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// intValue reads an int from an authored YAML value (float64 from JSON, or a bare
// int/string).
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
