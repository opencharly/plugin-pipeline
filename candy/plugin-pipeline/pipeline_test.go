package pluginpipeline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

func TestProbeMediaGate(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "cast"), []byte(strings.Repeat("a", 300)), 0o644)
	ok, _ := runProbe("media_gate", map[string]any{"dir": dir, "files": []any{"cast"}, "min": map[string]any{"cast": float64(200)}})
	if !ok {
		t.Fatalf("media_gate should pass with a 300B cast")
	}
	ok, _ = runProbe("media_gate", map[string]any{"dir": dir, "files": []any{"cast", "mp4"}, "min": map[string]any{}})
	if ok {
		t.Fatalf("media_gate should fail on a missing file")
	}
}

func TestProbeLockAudit(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "run.log"), []byte("database is locked"), 0o644)
	ok, _ := runProbe("lock_audit", map[string]any{"trees": []any{dir}})
	if ok {
		t.Fatalf("lock_audit must fail with a lock incident")
	}
	ok, _ = runProbe("lock_audit", map[string]any{"trees": []any{t.TempDir()}})
	if !ok {
		t.Fatalf("lock_audit must pass on a clean tree")
	}
}

func TestProbeResolveChannel(t *testing.T) {
	ok, _ := runProbe("resolve_channel", map[string]any{
		"channels": map[string]any{"stable": map[string]any{"golden": "x", "provision": "p"}},
		"default":  "stable",
	})
	if !ok {
		t.Fatalf("resolve_channel should pass")
	}
	ok, _ = runProbe("resolve_channel", map[string]any{"channels": map[string]any{}, "default": "dev"})
	if ok {
		t.Fatalf("resolve_channel should fail for an unknown channel")
	}
}

func TestProbeConfigAudit(t *testing.T) {
	bed := filepath.Join(t.TempDir(), "charly.yml")
	_ = os.WriteFile(bed, []byte("check:\n  plan:\n    - check: apply\n      command: pr-apply 1 abc"), 0o644)
	ok, _ := runProbe("config_audit", map[string]any{"bed": bed})
	if !ok {
		t.Fatalf("config_audit should pass with the pr-apply seam")
	}
}

func TestResolveValue(t *testing.T) {
	rc := &runCtx{pr: "10140", calver: "2026.251.1200", workdir: t.TempDir(), env: map[string]string{"EVAL_REPO": "omacom/omarchy"}}
	if got := rc.resolveValue("pr-$pr"); got != "pr-10140" {
		t.Fatalf("resolveValue = %v", got)
	}
	if got := rc.resolveValue("repo=$env.EVAL_REPO"); got != "repo=omacom/omarchy" {
		t.Fatalf("env ref = %v", got)
	}
}

func TestRunPlanGenerate(t *testing.T) {
	wd := t.TempDir()
	p := params.PipelineInput{
		Stages: []params.Stage{
			{"id": "bed-render", "kind": "generate",
				"template": "# report for pr-$pr", "vars": map[string]any{"pr": "$pr"},
				"out": wd + "/out.md"},
		},
	}
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{}}
	l := newLedger()
	curLedger = l
	res, err := rc.runStage(nil, "generate", "bed-render", p.Stages[0], l)
	if err != nil || res.Status != "ok" {
		t.Fatalf("generate: %v %v", res, err)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "out.md"))
	if string(b) != "# report for pr-7" {
		t.Fatalf("render mismatch: %q", string(b))
	}
}

func TestAgentRuntimeSystemPromptAndTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/chat/completions" {
			t.Errorf("bad path %s", req.URL.Path)
		}
		var cr chatRequest
		if err := json.NewDecoder(req.Body).Decode(&cr); err != nil {
			t.Errorf("decode: %v", err)
		}
		if len(cr.Messages) != 2 || cr.Messages[0].Role != "system" ||
			!strings.Contains(*cr.Messages[0].Content, "EVAL_ORACLE_MARKER") {
			t.Errorf("system prompt not passed verbatim")
		}
		if len(cr.Tools) == 0 {
			t.Errorf("tools not attached")
		}
		resp := map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": "ok"},
		}}}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(resp)
	}))
	defer srv.Close()
	t.Setenv("EVAL_LLM_BASE_URL", srv.URL)
	t.Setenv("EVAL_LLM_MODEL", "mock")
	t.Setenv("EVAL_LLM_API_KEY", "k")
	resp, err := runAgent(t.Context(), "omarchy EVAL_ORACLE_MARKER system", "triage pr 10140", []string{"pr"})
	if err != nil || resp != "ok" {
		t.Fatalf("agent: %v %v", resp, err)
	}
}

func TestCLIValidate(t *testing.T) {
	dir := t.TempDir()
	cfg := "test-plan:\n  pipeline:\n    stages:\n      - id: s\n        kind: gate\n        condition: \"true\"\n"
	if err := os.WriteFile(filepath.Join(dir, "charly.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHARLY_PROJECT_DIR", dir)
	code, err := runCLI([]string{"validate", "test-plan"})
	if err != nil || code != 0 {
		t.Fatalf("validate: %d %v", code, err)
	}
}
