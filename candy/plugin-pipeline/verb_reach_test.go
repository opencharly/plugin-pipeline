package pluginpipeline

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
)

// liftedVerbs: the seven workflow stage kinds registered as ordinary `verb:`
// capabilities (the inline `command` kind is deliberately NOT lifted).
var liftedVerbs = []struct {
	word     string
	inputDef string
}{
	{"agent", "#PipelineAgentInput"},
	{"probe", "#PipelineProbeInput"},
	{"ade", "#PipelineAdeInput"},
	{"generate", "#PipelineGenerateInput"},
	{"emit", "#PipelineEmitInput"},
	{"media", "#PipelineMediaInput"},
	{"gate", "#PipelineGateInput"},
}

// routingErrorSubstr is the shape Invoke answers an UNROUTED word with. The
// reachability test keys on it, so it lives in one place.
const routingErrorSubstr = "unsupported word"

// TestLiftedVerbsAreReachable is the ANTI-REREGRESSION lock on the lifted-verb
// cutover: each workflow stage kind must be (a) DECLARED as a verb: capability
// with its frozen input def, and (b) ROUTED — Invoke(OpRun, Reserved=<word>)
// reaches the verb body instead of falling through to the pipeline CLI or the
// named unknown-word error. Before the lift, every one of these words was only a
// `kind: pipeline` stage, so a plan could not reach it.
//
// ROUTING, not the happy path, is the assertion. Three bodies need infrastructure
// a unit test must not require — a live LLM (agent: EVAL_LLM_BASE_URL is pointed
// at a dead port so it fails fast), a rendered host bed (ade), /tmp/pr-<n>
// capture artifacts (media) — so for those the test asserts only that the word
// ROUTES: Invoke returns the verdict envelope (err == nil) and any failure is a
// BODY error, never a routing error. gate and probe get a STRONGER assertion:
// their bodies are deterministic, so the verdict itself is pinned.
//
// BITES: drop one word's routing arm from Invoke and that word reaches the
// `default:` arm's routing error — the "NOT ROUTED" failure below. Drop its
// capability from NewMeta and the Describe assertion fails. The unknown-word
// subtest proves the routing error is the shape this test detects, so the lock
// cannot pass vacuously.
func TestLiftedVerbsAreReachable(t *testing.T) {
	// A dead endpoint: the agent verb must fail FAST (connection refused)
	// instead of hanging on the ambient provider.
	t.Setenv("EVAL_LLM_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("EVAL_LLM_API_KEY", "")
	tmp := t.TempDir()

	// (a) the capabilities are DECLARED, with the frozen input def.
	t.Run("capabilities-declared", func(t *testing.T) {
		caps, err := NewMeta().Describe(context.Background(), &pb.Empty{})
		if err != nil {
			t.Fatalf("Describe: %v", err)
		}
		have := map[string]string{}
		for _, c := range caps.GetProvided() {
			have[c.GetClass()+":"+c.GetWord()] = c.GetInputDef()
		}
		for _, lv := range liftedVerbs {
			def, ok := have["verb:"+lv.word]
			if !ok {
				t.Errorf("verb:%s is not declared in NewMeta's capabilities", lv.word)
				continue
			}
			if def != lv.inputDef {
				t.Errorf("verb:%s InputDef = %q, want %q", lv.word, def, lv.inputDef)
			}
		}
	})

	// (b) each word is ROUTED. The minimal plugin_input per word is chosen to
	// reach the body and fail or pass THERE, never at the router.
	cases := []struct {
		name  string
		word  string
		input map[string]any
		// wantStatus pins the verdict for the deterministic bodies ("" = routing only).
		wantStatus string
	}{
		// gate is fully deterministic: true passes, false fails with its reason.
		{name: "gate-pass", word: "gate", input: map[string]any{"condition": "1 == 1"}, wantStatus: "pass"},
		{name: "gate-fail", word: "gate", input: map[string]any{"condition": "1 == 2"}, wantStatus: "fail"},
		// probe is deterministic: an unknown probe verb is a BODY failure.
		{name: "probe-unknown-verb", word: "probe", input: map[string]any{"verbs": []string{"no_such_probe"}}, wantStatus: "fail"},
		// infra-dependent bodies: routing only (see the doc comment).
		{name: "agent", word: "agent", input: map[string]any{"prompt": "say hi"}},
		{name: "ade", word: "ade", input: map[string]any{"bed": "no-such-bed"}},
		{name: "generate", word: "generate", input: map[string]any{"template": "x", "out": filepath.Join(tmp, "gen.txt")}},
		{name: "emit", word: "emit", input: map[string]any{"schema": "#NoSuchDef", "value": map[string]any{"a": 1}, "out": filepath.Join(tmp, "emit.yml")}},
		{name: "media", word: "media", input: map[string]any{"assemble": true, "dir": filepath.Join(tmp, "media"), "files": []string{"png"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pj, err := json.Marshal(map[string]any{"plugin_input": tc.input})
			if err != nil {
				t.Fatalf("marshal plugin_input: %v", err)
			}
			reply, err := NewProvider().Invoke(context.Background(), &pb.InvokeRequest{
				Op:         sdk.OpRun,
				Reserved:   tc.word,
				ParamsJson: pj,
			})
			if err != nil {
				if strings.Contains(err.Error(), routingErrorSubstr) {
					t.Fatalf("verb:%s NOT ROUTED — Invoke returned the routing error: %v", tc.word, err)
				}
				t.Fatalf("verb:%s routed but failed to decode/dispatch: %v", tc.word, err)
			}
			var verdict struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(reply.GetResultJson(), &verdict); err != nil {
				t.Fatalf("verb:%s: verdict is not JSON (%q): %v", tc.word, reply.GetResultJson(), err)
			}
			if verdict.Status != "pass" && verdict.Status != "fail" {
				t.Fatalf("verb:%s: verdict status = %q, want pass|fail (result %q)", tc.word, verdict.Status, reply.GetResultJson())
			}
			if strings.Contains(strings.ToLower(verdict.Message), routingErrorSubstr) {
				t.Fatalf("verb:%s: body reported a ROUTING error: %q", tc.word, verdict.Message)
			}
			if tc.wantStatus != "" && verdict.Status != tc.wantStatus {
				t.Fatalf("verb:%s: status = %q, want %q (message %q)", tc.word, verdict.Status, tc.wantStatus, verdict.Message)
			}
			t.Logf("verb:%s routed → status=%s message=%q", tc.word, verdict.Status, verdict.Message)
		})
	}

	// The routing error is real and is what the assertions above detect: an
	// unknown word must be REJECTED, never run through the CLI.
	t.Run("unknown-word-is-rejected", func(t *testing.T) {
		_, err := NewProvider().Invoke(context.Background(), &pb.InvokeRequest{
			Op:       sdk.OpRun,
			Reserved: "no-such-lifted-verb",
		})
		if err == nil {
			t.Fatalf("an unknown OpRun word was accepted; want the %q error", routingErrorSubstr)
		}
		if !strings.Contains(err.Error(), routingErrorSubstr) {
			t.Fatalf("unknown word error = %q, want it to contain %q", err.Error(), routingErrorSubstr)
		}
	})
}
