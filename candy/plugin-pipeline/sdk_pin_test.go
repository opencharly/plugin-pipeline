package pluginpipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
)

// sdk_pin_test.go — the SDK PIN is the grammar this plugin JUDGES with, so the pin carries a
// test of its own here (opencharly/plugin-pipeline#55).
//
// THE DEFECT THIS FILE STANDS OVER: `sdk#443` restored `${env.NAME}` inside a `when:` and
// the restore reached every IN-PROCESS caller; this plugin VALIDATES out of process, so the
// grammar a consumer's manifest is judged by is `go.mod`'s `sdk` version, not the host
// binary's. While this pin lagged (`v0.2026294.1003`) every `${env.…}` guard was a parse
// error at kind decode, for every consumer, whatever the host believed.
//
// `OpLoad` is the site that failure arrives at: `plugin.go`'s kind-decode arm calls
// `workflowkit.ValidatePipeline`, which is the same gate `box validate` reaches for a
// host-value-gated `kind: pipeline`. Driving the PROVIDER DISPATCH (never a hand-built
// document) is what makes the pin, rather than a copied implementation, the thing under
// test — a downgrade of this pin fails the case below on the next build.
//
// THE TWO `when:` CASES RETIRED WITH THE LOBSTER STEP GRAMMAR (P26 unit 2,
// opencharly/plugin-pipeline#66). They pinned sdk#443's `${env.NAME}` guard THROUGH an
// authored step's `when:` expression, and that surface no longer exists: D-FLOW-1 makes a
// stage's condition "a matcher per parameter (`when:`), never an expression", so the
// contract's `#StageWhen` is a map and the sdk's expression grammar has no authored home in
// this kind. The grammar itself is still covered where it lives (sdk's own tests); what
// retires here is the PROXY, not the grammar — and the proxy's subject, "the pin's gate is
// the one that judges a consumer's manifest", is kept below on the surface that remains.

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

// TestKindLoadGateJudgesWithThePinnedValidator proves the provider dispatch runs THIS pin's
// validator: a stage body the validator refuses must be refused at kind decode, and a valid
// one must be admitted. A downgrade of the pin (or a validator that stopped being called)
// fails one half or the other.
func TestKindLoadGateJudgesWithThePinnedValidator(t *testing.T) {
	valid := map[string]any{
		"description": "the pinned gate admits a stage body",
		"stage": map[string]any{
			"s": map[string]any{
				"step": map[string]any{
					"step": []any{map[string]any{"run": "s", "mkdir": "/tmp/x"}},
				},
			},
		},
	}
	if err := invokeKindLoad(t, valid); err != nil {
		t.Fatalf("the pinned validator refused a well-formed stage body: %v", err)
	}

	invalid := map[string]any{
		"description": "a stage with no arm",
		"stage":       map[string]any{"s": map[string]any{}},
	}
	err := invokeKindLoad(t, invalid)
	if err == nil {
		t.Fatal("the kind-decode gate admitted a stage with no arm — the pinned validator did not run")
	}
	if !strings.Contains(err.Error(), "sets no arm") {
		t.Fatalf("the refusal must be the validator's own (naming the arm rule), got: %v", err)
	}
}
