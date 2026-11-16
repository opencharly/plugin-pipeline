package workflowkit

import (
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/spec/spec"
)

// graph_test.go — the graph's ONE edge model is `after:` (the scheduling constraint the
// lowering applies), so these tests assert the same relation the ordering tests assert,
// read back through each renderer.

func graphPipeline() *params.Pipeline {
	return &params.Pipeline{
		Description: "graph fixture",
		Stage: map[string]spec.Stage{
			"zulu":   stepStage(charlyCmd("first", `echo z`)),
			"middle": ranAfter(stepStage(charlyCmd("second", `echo m`)), "zulu"),
			"alpha":  ranAfter(stepStage(charlyCmd("third", `echo a`)), "middle", "zulu"),
		},
	}
}

func TestGraphEdgesAreTheAfterGraph(t *testing.T) {
	edges := GraphEdges(graphPipeline())
	want := map[string]bool{
		"zulu->middle(after)":  true,
		"middle->alpha(after)": true,
		"zulu->alpha(after)":   true,
	}
	for _, e := range edges {
		key := e.From + "->" + e.To + "(" + e.Relation + ")"
		if !want[key] {
			t.Errorf("unexpected edge %s", key)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Errorf("GraphEdges is missing %v (got %v)", want, edges)
	}
}

func TestGraphNodesAreTheLoweredOrder(t *testing.T) {
	got := strings.Join(GraphNodes(graphPipeline()), ",")
	if got != "zulu,middle,alpha" {
		t.Errorf("GraphNodes = %s, want the lowering's topological order zulu,middle,alpha", got)
	}
}

func TestGraphRenderings(t *testing.T) {
	p := graphPipeline()
	mermaid, err := RenderGraph(p, "mermaid")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(mermaid, "flowchart LR\n") || !strings.Contains(mermaid, "-->|after|") {
		t.Errorf("mermaid render:\n%s", mermaid)
	}
	dotOut, err := RenderGraph(p, "dot")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dotOut, "digraph") || !strings.Contains(dotOut, "after") {
		t.Errorf("dot render:\n%s", dotOut)
	}
	ascii, err := RenderGraph(p, "ascii")
	if err != nil {
		t.Fatal(err)
	}
	// the topological order, each stage followed by what it runs after
	if !strings.HasPrefix(ascii, "zulu\n") {
		t.Errorf("ascii render must start with the first stage:\n%s", ascii)
	}
	if !strings.Contains(ascii, "alpha\n  <- middle (after)\n  <- zulu (after)\n") {
		t.Errorf("ascii render is not the after graph:\n%s", ascii)
	}
	if _, err := RenderGraph(p, "nope"); err == nil {
		t.Error("an unknown format must be refused")
	}
}

// TestGraphOfACycleStillDraws pins the one place a graph exists for a document that cannot
// run: the author fixing a cycle. The validator is what refuses it; the renderer must not
// also explode.
func TestGraphOfACycleStillDraws(t *testing.T) {
	p := &params.Pipeline{Stage: map[string]spec.Stage{
		"a": ranAfter(stepStage(charlyCmd("a", `echo a`)), "b"),
		"b": ranAfter(stepStage(charlyCmd("b", `echo b`)), "a"),
	}}
	if got := strings.Join(GraphNodes(p), ","); got != "a,b" {
		t.Errorf("a cyclic graph falls back to the sorted names, got %s", got)
	}
	if _, err := RenderGraph(p, "ascii"); err != nil {
		t.Errorf("rendering a cyclic graph: %v", err)
	}
}
