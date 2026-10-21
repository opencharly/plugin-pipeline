package pluginpipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
)

// env_refs_test.go — the environment-reference resolver, at the layer that owns it.
//
// A `$env.NAME` / `${env.NAME}` in a DATA field of a lifted verb is resolved by THIS
// package: `workflowkit.Lower` moves the verb body into the generated charly.yml as a
// `task:` entity and leaves only `run: <charly> … task …` in the .lobster, so the engine
// never sees the leaf, and the plan's own `${VAR}` expander is uppercase-only and never
// matches `env.` (kit.TestVarRefPattern). Nothing else in the chain can resolve it, which
// is why the grammar must be complete here and why the ONE production constructor must
// carry a map.
//
// Every case below drives the PRODUCTION dispatch (`NewProvider().Invoke` → the OpRun arm
// → invokeLiftedVerb), never a hand-built verbEnv: the whole defect was that the grammar
// is green under a hand-supplied map and dead in production.

// verbVerdict is the envelope invokeLiftedVerb replies with.
type verbVerdict struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// dispatchVerb runs one authored `<word>: <input>` step through the real provider
// dispatch. A transport error fails the test: the cases here assert BODY verdicts.
func dispatchVerb(t *testing.T, word string, input map[string]any) verbVerdict {
	t.Helper()
	pj, err := json.Marshal(map[string]any{"plugin_input": input})
	if err != nil {
		t.Fatalf("marshal plugin_input: %v", err)
	}
	reply, err := NewProvider().Invoke(context.Background(), &pb.InvokeRequest{
		Op:         sdk.OpRun,
		Reserved:   word,
		ParamsJson: pj,
	})
	if err != nil {
		t.Fatalf("verb:%s dispatch: %v", word, err)
	}
	var v verbVerdict
	if err := json.Unmarshal(reply.GetResultJson(), &v); err != nil {
		t.Fatalf("verb:%s verdict is not JSON (%q): %v", word, reply.GetResultJson(), err)
	}
	return v
}

// TestEnvRefGrammarAdmitsBothSpellings locks the GRAMMAR at the one constructor the
// dispatcher uses. Both spellings name the same namespace and must read the same source:
// `${env.NAME}` is what plugin-migrate's reshape emits (`reshape_pipeline_lobster.go:609`)
// and the brace form every other ref uses, `$env.NAME` is what a pre-cutover document says.
func TestEnvRefGrammarAdmitsBothSpellings(t *testing.T) {
	const name = "PIPELINE_PROBE_ENV"
	t.Setenv(name, "approve")
	rc := standaloneVerbEnv(nil, t.TempDir()) // the ONLY production verbEnv constructor

	cases := []struct{ in, want string }{
		{"${env." + name + "}", "approve"},         // the reshape's spelling
		{"$env." + name, "approve"},                // the retired spelling
		{"x-${env." + name + "}-y", "x-approve-y"}, // embedded, not whole-string
		{"eval/pr-" + "1" + "/$env." + name, "eval/pr-1/approve"},
	}
	for _, tc := range cases {
		if got := rc.resolveRefs(tc.in); got != tc.want {
			t.Errorf("resolveRefs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestEnvDataRefResolvesThroughTheVerbDispatch is the case the chain measured: a data
// string naming an environment value must resolve to that value in the production
// dispatch. The gate verb's `condition` is a DATA field (ref-resolved, then evaluated by
// evalCondition), so a resolved value makes an equal condition PASS and an unresolved one
// leaves the literal `${env.…}` on the right-hand side and FAILS.
//
// Both spellings, because plugin-migrate's output is the brace form and a hand-authored
// pre-cutover entity is the bare one.
func TestEnvDataRefResolvesThroughTheVerbDispatch(t *testing.T) {
	const name = "PIPELINE_PROBE_ENV"
	t.Setenv(name, "approve")

	for _, spelling := range []string{"approve == ${env." + name + "}", "approve == $env." + name} {
		v := dispatchVerb(t, "gate", map[string]any{"condition": spelling})
		if v.Status != "pass" {
			t.Errorf("condition %q: status = %q (%q), want pass — the env value did not reach the condition, so the literal stayed", spelling, v.Status, v.Message)
		}
	}
}

// TestMissingEnvNameIsANamedError locks the MISSING-variable contract: a name the process
// environment does not carry is a NAMED error that says so, never a silent empty value.
//
// Empty is the wrong answer deliberately: an empty value is a run-time emptiness in place
// of a load error (it is how a cache key stops matching and an emitted record ships with
// an empty `head:`, both silently). The report must name the variable, so that the author
// reads the cause rather than a downstream symptom.
func TestMissingEnvNameIsANamedError(t *testing.T) {
	const missing = "PIPELINE_PROBE_ABSENT_9E3F1C"
	if err := os.Unsetenv(missing); err != nil {
		t.Fatalf("unset %s: %v", missing, err)
	}
	if _, present := os.LookupEnv(missing); present {
		t.Fatalf("%s is set in this environment; the case needs an absent name", missing)
	}

	v := dispatchVerb(t, "gate", map[string]any{"condition": "approve == ${env." + missing + "}"})
	if v.Status != "fail" {
		t.Fatalf("status = %q (%q), want fail — an absent name must never resolve", v.Status, v.Message)
	}
	// The dispatch must LEAD with the miss. The condition's own verdict cannot be the
	// discriminator here: pre-change the literal `${env.…}` sits inside the condition
	// text, so the downstream message NAMES the variable too — only the shape of the
	// report tells a resolved-empty from a reported miss.
	if want := "unresolved environment reference(s): " + missing; !strings.HasPrefix(v.Message, want) {
		t.Errorf("message %q does not open with %q — a missing name is the CAUSE, and it must be reported as one rather than as a downstream condition verdict", v.Message, want)
	}
}

// TestAuthoredValidatorRefusesAnEnvRef locks the SHELL boundary. A `validate:` field is
// handed to `bash -c`, so a value substituted into it would become SCRIPT TEXT — an
// injection vector. The shell has its own safe mechanism: `${NAME}` reads the inherited
// environment without the value ever being parsed as code. So the ref is REFUSED here,
// with the replacement named, rather than spliced.
//
// BITES: pre-change nothing substituted the ref, so `bash -c 'echo $env.X'` ran (an unset
// `$env`, then the literal `.X`) and the stage PASSED.
func TestAuthoredValidatorRefusesAnEnvRef(t *testing.T) {
	const name = "PIPELINE_PROBE_ENV"
	t.Setenv(name, "approve")
	out := filepath.Join(t.TempDir(), "gen.txt")

	for _, spelling := range []string{"echo $env." + name, "echo ${env." + name + "}"} {
		v := dispatchVerb(t, "generate", map[string]any{
			"template": "x", "out": out,
			"validate": spelling,
		})
		if v.Status != "fail" {
			t.Fatalf("validate %q: status = %q (%q), want fail — an env ref must never become shell script text", spelling, v.Status, v.Message)
		}
		if !strings.Contains(v.Message, "$ref:"+name) && !strings.Contains(v.Message, "${"+name+"}") {
			t.Errorf("validate %q: message %q does not name the replacement ${%s}", spelling, v.Message, name)
		}
	}
}

// TestAuthoredValidatorReadsTheShellOwnEnvironment is the control for the refusal above:
// the replacement it names must actually WORK, and it must carry a value that would be
// code if it were spliced. The payload contains a `;` and a `touch`; the script reads it
// through `${NAME}`, so bash expands a VARIABLE (never re-parsing metacharacters) and the
// touch must not happen.
func TestAuthoredValidatorReadsTheShellOwnEnvironment(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "pwned")
	t.Setenv("PIPELINE_PROBE_PAYLOAD", "a; touch "+sentinel)
	out := filepath.Join(t.TempDir(), "gen.txt")

	v := dispatchVerb(t, "generate", map[string]any{
		"template": "x", "out": out,
		"validate": `test -n "${PIPELINE_PROBE_PAYLOAD}"`,
	})
	if v.Status != "pass" {
		t.Fatalf("status = %q (%q), want pass — ${NAME} must read the inherited environment", v.Status, v.Message)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("the payload was EXECUTED (%s exists): a value must never become script text", sentinel)
	}
}

// envRefSpellings is a compile-time guard that the resolver's two spellings are exercised
// through the typed inputs too: a body reading a ref out of its typed field (not the raw
// wire map) sees the same grammar.
func TestEnvRefResolvesFromATypedInput(t *testing.T) {
	const name = "PIPELINE_PROBE_ENV"
	t.Setenv(name, "approve")

	var got string
	run := func(in params.PipelineGateInput, e *verbEnv) (map[string]any, error) {
		got = e.resolveRefs(in.Condition)
		return nil, nil
	}
	pj, _ := json.Marshal(map[string]any{"plugin_input": map[string]any{"condition": "${env." + name + "} == approve"}})
	if _, err := invokeLiftedVerb(context.Background(), &pb.InvokeRequest{Op: sdk.OpRun, Reserved: "gate", ParamsJson: pj}, run); err != nil {
		t.Fatalf("invokeLiftedVerb: %v", err)
	}
	if got != "approve == approve" {
		t.Fatalf("typed-input ref = %q, want %q", got, "approve == approve")
	}
}
