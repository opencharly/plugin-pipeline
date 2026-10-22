package pluginpipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
)

// sdk_pin_test.go — the SDK PIN is the grammar this plugin JUDGES with, so the pin
// carries a test of its own here (opencharly/plugin-pipeline#55).
//
// The defect this file stands over: `sdk#443` restored `${env.NAME}` inside a `when:`
// and the restore reached every IN-PROCESS caller; this plugin VALIDATES out of
// process, so the expression a consumer's manifest is judged by is `go.mod`'s `sdk`
// version, not the host binary's. While this pin lagged (`v0.2026294.1003`) every
// `${env.…}` guard was a parse error at kind decode — `step "publish": when:
// unexpected token Bracket("{")` — for every consumer, whatever the host believed.
//
// `OpLoad` is the site that failure arrives at: `plugin.go`'s kind-decode arm calls
// `workflowkit.ValidatePipeline`, which is the same gate `box validate` reaches for a
// host-value-gated `kind: pipeline`. Driving the PROVIDER DISPATCH (never a hand-built
// document) is what makes the pin, rather than a copied implementation, the thing
// under test — a downgrade of this pin fails the case below on the next build.

// envWhenGuard is the migrated publish gate verbatim from the org's only real
// `kind: pipeline` consumer (opencharly/eval-omarchy, `eval-pr-plan`): an env value AND
// a `ref(...)` call in ONE expression. Both call names must be admitted, which is why
// the fixture is the consumer's own text and not a minimal `${env.X} == y`.
const envWhenGuard = `!(${env.EVAL_PUBLISH} != approve || ref("cold-read", "json.verdict") == NO_VALIDATION)`

// invokeKindLoad runs the plugin's kind-decode arm — the gate a consumer's manifest
// meets — and returns its error.
func invokeKindLoad(t *testing.T, body map[string]any) error {
	t.Helper()
	pj, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal entity body: %v", err)
	}
	_, err = NewProvider().Invoke(context.Background(), &pb.InvokeRequest{
		Op:         sdk.OpLoad,
		ParamsJson: pj,
	})
	return err
}

// TestPinnedSDKAcceptsTheEnvGuardInsideWhen is the pin's coverage: it FAILS on
// `sdk v0.2026294.1003` with `unexpected token Bracket("{")` and passes on the restored
// grammar. The earlier step is declared because `ref("cold-read", …)` names it — the
// expression is not accepted by being skipped.
func TestPinnedSDKAcceptsTheEnvGuardInsideWhen(t *testing.T) {
	err := invokeKindLoad(t, map[string]any{
		"description": "the migrated publish gate",
		"steps": []any{
			map[string]any{"id": "cold-read", "run": "true"},
			map[string]any{"id": "publish", "when": envWhenGuard, "run": "true"},
		},
	})
	if err != nil {
		t.Fatalf("a ${env.NAME} inside a when: must validate against this repo's pinned sdk: %v — the pin does not carry sdk#443's restore", err)
	}
}

// TestPinnedSDKRefusesTheBareEnvNamespace is the control: the restore admits ONE call
// form (`env("NAME")`, rewritten from the brace spelling), never the retired `env.NAME`
// member access. Without this case the test above would also pass on a grammar widened
// into a namespace, which is a different change with a run-time failure of its own.
func TestPinnedSDKRefusesTheBareEnvNamespace(t *testing.T) {
	err := invokeKindLoad(t, map[string]any{
		"description": "the retired env spelling",
		"steps": []any{
			map[string]any{"id": "publish", "when": "env.EVAL_PUBLISH == approve", "run": "true"},
		},
	})
	if err == nil {
		t.Fatal("a bare `env.NAME` member access must stay refused — the grammar admits env(\"NAME\"), not a namespace")
	}
	if !strings.Contains(err.Error(), "has no field EVAL_PUBLISH") {
		t.Fatalf("the refusal must NAME the member access, got: %v", err)
	}
}
