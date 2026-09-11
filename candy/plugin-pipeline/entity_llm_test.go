package pluginpipeline

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}},
		})
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
	rc := &runCtx{llm: map[string]any{"model": "entity-model", "api_key": "entity-key"}}
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
	rc := &runCtx{llm: map[string]any{}}       // no api_key anywhere
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
	if got := llmModel(nil); got != "deepseek-v4.1-flash:cloud" {
		t.Errorf("llmModel default: got %q, want \"deepseek-v4.1-flash:cloud\"", got)
	}
	if got := llmBaseURL(nil); got != "http://localhost:11434/v1" {
		t.Errorf("llmBaseURL default: got %q, want http://localhost:11434/v1", got)
	}
}
