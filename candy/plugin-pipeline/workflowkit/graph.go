package workflowkit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/emicklei/dot"
	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// graph.go — the workflow graph renderers. One edge model, three renderings
// (mermaid / graphviz dot / ascii), so `charly workflow graph --format …` is the
// same graph however it is drawn.
//
// THE ONE EDGE MODEL IS `after:`. A stage's scheduling constraint is the edge its
// document wrote (D-FLOW-1: "ordered by `after:`"), so the graph this file draws IS
// the ordering the lowering applies — the same relation, read from the same field, not
// a second opinion about what depends on what.

// GraphFormat is the closed renderer vocabulary.
var GraphFormat = []string{"mermaid", "dot", "ascii"}

// GraphEdge is one ordering edge: `To` runs after `From`.
type GraphEdge struct {
	From     string
	To       string
	Relation string
}

// GraphEdges derives the pipeline's ordering edges from every stage's `after:`.
// The edge is the SCHEDULING constraint (the lowering emits stages in topological
// order), so the drawing cannot disagree with the run.
func GraphEdges(p *params.Pipeline) []GraphEdge {
	var edges []GraphEdge
	if p == nil {
		return edges
	}
	for _, name := range StageNames(p) {
		for _, producer := range p.Stage[name].After {
			edges = append(edges, GraphEdge{From: string(producer), To: name, Relation: "after"})
		}
	}
	return edges
}

// GraphNodes returns every stage name in the order the lowering emits them. A cyclic
// document has no such order; it falls back to the sorted names so a graph can still be
// drawn for the author who has to fix it (the validator is what refuses the cycle).
func GraphNodes(p *params.Pipeline) []string {
	if p == nil {
		return nil
	}
	if order, err := StageOrder(p.Stage); err == nil {
		return order
	}
	return StageNames(p)
}

// RenderGraph renders the pipeline graph in one of GraphFormat.
func RenderGraph(p *params.Pipeline, format string) (string, error) {
	if !contains(GraphFormat, format) {
		return "", fmt.Errorf("unknown graph format %q (want %s)", format, strings.Join(GraphFormat, "/"))
	}
	switch format {
	case "mermaid":
		return GraphMermaid(p), nil
	case "dot":
		return GraphDot(p), nil
	default:
		return GraphASCII(p), nil
	}
}

// GraphMermaid renders the graph as a mermaid `flowchart LR` document.
func GraphMermaid(p *params.Pipeline) string {
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	for _, n := range GraphNodes(p) {
		fmt.Fprintf(&b, "  %s[%s]\n", mermaidID(n), n)
	}
	for _, e := range GraphEdges(p) {
		fmt.Fprintf(&b, "  %s -->|%s| %s\n", mermaidID(e.From), e.Relation, mermaidID(e.To))
	}
	return b.String()
}

// GraphDot renders the graph as graphviz dot.
func GraphDot(p *params.Pipeline) string {
	g := dot.NewGraph(dot.Directed)
	nodes := map[string]dot.Node{}
	for _, n := range GraphNodes(p) {
		nodes[n] = g.Node(n)
	}
	for _, e := range GraphEdges(p) {
		from, ok := nodes[e.From]
		if !ok {
			from = g.Node(e.From)
			nodes[e.From] = from
		}
		to, ok := nodes[e.To]
		if !ok {
			to = g.Node(e.To)
			nodes[e.To] = to
		}
		edge := g.Edge(from, to)
		edge.Label(e.Relation)
	}
	return g.String()
}

// GraphASCII renders the graph as an indented dependency listing — the terminal-
// friendly form, each stage followed by what it runs after.
func GraphASCII(p *params.Pipeline) string {
	byTarget := map[string][]GraphEdge{}
	for _, e := range GraphEdges(p) {
		byTarget[e.To] = append(byTarget[e.To], e)
	}
	var b strings.Builder
	for _, n := range GraphNodes(p) {
		fmt.Fprintf(&b, "%s\n", n)
		ins := byTarget[n]
		sort.Slice(ins, func(i, j int) bool {
			if ins[i].From != ins[j].From {
				return ins[i].From < ins[j].From
			}
			return ins[i].Relation < ins[j].Relation
		})
		for _, e := range ins {
			fmt.Fprintf(&b, "  <- %s (%s)\n", e.From, e.Relation)
		}
	}
	return b.String()
}

// mermaidID makes a stage name safe as a mermaid node id (mermaid ids cannot carry the
// punctuation a name may).
func mermaidID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "_%02x_", r)
		}
	}
	if b.Len() == 0 {
		return "stage"
	}
	return b.String()
}

// contains reports whether list holds want (the closed-vocabulary membership test the
// renderers and the CLI share).
func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
