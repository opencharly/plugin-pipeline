package workflowkit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/emicklei/dot"
	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	sdkworkflowkit "github.com/opencharly/sdk/workflowkit"
)

// graph.go — the workflow graph renderers. One edge model, three renderings
// (mermaid / graphviz dot / ascii), so `charly workflow graph --format …` is the
// same graph however it is drawn.

// GraphFormat is the closed renderer vocabulary.
var GraphFormat = []string{"mermaid", "dot", "ascii"}

// GraphEdge is one dataflow edge: `To` consumes the result of `From` via `Relation`
// (when / stdin / for_each / lanes / branch).
type GraphEdge struct {
	From     string
	To       string
	Relation string
}

// GraphEdges derives the pipeline's dataflow edges. A reference in `when:`, `stdin:`,
// `for_each:` or `lanes:` is a dependency (lobster resolves a `$ref` from a FINISHED
// step, so the edge is the real scheduling constraint) — the two loop arms carry the
// same kind of collection expression, so both are edges. Parallel branches and loop
// sub-steps are attached to their parent with a `branch` edge.
func GraphEdges(p *params.Pipeline) []GraphEdge {
	var edges []GraphEdge
	if p == nil {
		return edges
	}
	for i := range p.Steps {
		s := &p.Steps[i]
		for _, rel := range [][2]string{{"when", s.When}, {"stdin", s.Stdin}, {"for_each", s.ForEach}, {"lanes", s.Lanes}} {
			for _, name := range sdkworkflowkit.RefNames(rel[1]) {
				edges = append(edges, GraphEdge{From: name, To: s.Id, Relation: rel[0]})
			}
		}
		for j := range s.Parallel.Branches {
			b := &s.Parallel.Branches[j]
			edges = append(edges, GraphEdge{From: s.Id, To: b.Id, Relation: "branch"})
			for _, rel := range [][2]string{{"when", b.When}, {"stdin", b.Stdin}} {
				for _, name := range sdkworkflowkit.RefNames(rel[1]) {
					edges = append(edges, GraphEdge{From: name, To: b.Id, Relation: rel[0]})
				}
			}
		}
		for j := range s.Steps {
			b := &s.Steps[j]
			edges = append(edges, GraphEdge{From: s.Id, To: b.Id, Relation: "branch"})
			for _, rel := range [][2]string{{"when", b.When}, {"stdin", b.Stdin}} {
				for _, name := range sdkworkflowkit.RefNames(rel[1]) {
					edges = append(edges, GraphEdge{From: name, To: b.Id, Relation: rel[0]})
				}
			}
		}
	}
	return edges
}

// GraphNodes returns every node id in the pipeline (steps, parallel branches and
// for_each sub-steps), in declaration order, deduplicated.
func GraphNodes(p *params.Pipeline) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if p == nil {
		return out
	}
	for i := range p.Steps {
		s := &p.Steps[i]
		add(s.Id)
		for j := range s.Parallel.Branches {
			add(s.Parallel.Branches[j].Id)
		}
		for j := range s.Steps {
			add(s.Steps[j].Id)
		}
	}
	// An edge may name a step that lives outside this pipeline (an import ref); keep
	// it visible rather than silently dropping the edge.
	for _, e := range GraphEdges(p) {
		add(e.From)
		add(e.To)
	}
	return out
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
// friendly form, each node followed by what it consumes.
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

// mermaidID makes a step id safe as a mermaid node id (mermaid ids cannot carry the
// punctuation a step id may).
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
		return "step"
	}
	return b.String()
}
