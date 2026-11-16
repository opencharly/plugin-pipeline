package workflowkit

// stage.go — the STAGE model of an authored `kind: pipeline` body (P26 unit 2,
// opencharly/plugin-pipeline#66).
//
// The authored surface is the contract's stage vocabulary (`spec/schema/stage.cue`,
// DESIGN.md D-FLOW-1/2/3). This file owns the three questions the SCHEMA cannot
// answer, because the contract records why each is not a CUE rule:
//
//  1. WHICH ARM a stage set. The contract's arms are four optional payload fields
//     (the arm spread and `or()` both fail that module's composition), so "exactly
//     one" is a validation rule — stageArm below is its ONE implementation.
//  2. THE ORDER the stages run in. `stage:` is a MAP, so declaration order does not
//     survive decoding; the only order the document declares is the graph `after:`
//     expresses. StageOrder orders it topologically (D-FLOW-1: "ordered by `after:`")
//     and a cycle is a LOAD error naming the stages in it — never a silent document
//     order.
//  3. THE DEFAULTS the schema declares. The pipeline body is decoded with yaml.v3
//     into the generated Go structs (entity.go's decodePipelineInput), which does NOT
//     apply CUE defaults — so `concurrency: 1`, `on_error: "fail"`, `retry.interval:
//     "2s"` and `answer.unanswered: "refused"` are applied HERE, once, rather than at
//     each read site.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/opencharly/spec/spec"
)

// Stage arms, as the lowering names them.
const (
	armStep       = "step"
	armPipeline   = "pipeline"
	armDeployment = "deployment"
	armAsk        = "ask"
)

// stageArm reports which of the contract's four arms the stage set, and refuses a
// stage that set none or more than one. It is the ONE arm-exclusivity rule: the
// validator and the lowering both call it, so the two cannot disagree about what a
// well-formed stage is.
func stageArm(s spec.Stage) (string, error) {
	var set []string
	if len(s.Step.Step) > 0 {
		set = append(set, armStep)
	}
	if s.Pipeline.Pipeline != "" {
		set = append(set, armPipeline)
	}
	if len(s.Deployment.Deployment) > 0 {
		set = append(set, armDeployment)
	}
	// `ask` carries no presence bit of its own: its prompt is REQUIRED, so a non-empty
	// prompt IS the arm being set (the contract's own admission).
	if s.Ask.Prompt != "" {
		set = append(set, armAsk)
	}
	switch len(set) {
	case 1:
		return set[0], nil
	case 0:
		return "", fmt.Errorf("sets no arm: exactly one of %s is required", strings.Join(allArms(), ", "))
	default:
		return "", fmt.Errorf("sets %d arms (%s): exactly one of %s is allowed",
			len(set), strings.Join(set, ", "), strings.Join(allArms(), ", "))
	}
}

// allArms lists the arm words for a diagnostic, in the order the contract declares them.
func allArms() []string { return []string{armStep, armPipeline, armDeployment, armAsk} }

// stageConcurrency is the pool width a stage declares, with the schema's default
// (`concurrency: *1`) applied.
func stageConcurrency(s spec.Stage) int64 {
	if s.Concurrency <= 0 {
		return 1
	}
	return s.Concurrency
}

// stageOnError is the failure policy a stage declares, with the schema's default
// (`on_error: *"fail"`) applied.
func stageOnError(s spec.Stage) string {
	if s.On_error == "" {
		return "fail"
	}
	return s.On_error
}

// stageInterval is the retry interval a stage declares, with the schema's default
// (`interval: *"2s"`) applied.
func stageInterval(s spec.Stage) string {
	if s.Retry.Interval == "" {
		return "2s"
	}
	return s.Retry.Interval
}

// stageUnanswered is the outcome an ask takes when nobody answers, with the schema's
// default (`unanswered: *"refused"`) applied.
func stageUnanswered(s spec.Stage) string {
	if s.Ask.Answer.Unanswered == "" {
		return "refused"
	}
	return s.Ask.Answer.Unanswered
}

// StageOrder returns every stage name in the order the graph `after:` declares, so a
// stage that declares `after: [x]` is emitted after x whatever order the document
// declared them in.
//
// The map carries no declaration order (Go map, and a YAML map is unordered by
// design), so the tie-break between stages that do not constrain each other is the
// stage NAME — a stable, reproducible emission rather than map iteration order.
//
// A cycle is an ERROR, and the error NAMES every stage in it: a graph that cannot be
// linearised is a document defect, and a lowering that silently picked one of the
// cyclic orders would run the stages in an order nobody wrote (D-LAW-5, fail loud at
// the source).
func StageOrder(stages map[string]spec.Stage) ([]string, error) {
	if len(stages) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(stages))
	for name := range stages {
		names = append(names, name)
	}
	sort.Strings(names)

	// indegree counts the `after:` edges a stage still waits on; dependents maps a
	// producer to the stages that name it.
	indegree := make(map[string]int, len(names))
	dependents := make(map[string][]string, len(names))
	for _, name := range names {
		indegree[name] = 0
	}
	for _, name := range names {
		for _, producer := range stages[name].After {
			// An `after:` that names no stage is the VALIDATOR's finding (it names the
			// stage and the missing producer); the ordering ignores it so that one
			// defect is reported once, by the gate that explains it.
			if _, ok := stages[string(producer)]; !ok {
				continue
			}
			dependents[string(producer)] = append(dependents[string(producer)], name)
			indegree[name]++
		}
	}

	// Kahn's algorithm, always taking the alphabetically smallest ready stage.
	ready := make([]string, 0, len(names))
	for _, name := range names {
		if indegree[name] == 0 {
			ready = append(ready, name)
		}
	}
	out := make([]string, 0, len(names))
	for len(ready) > 0 {
		sort.Strings(ready)
		name := ready[0]
		ready = ready[1:]
		out = append(out, name)
		for _, dep := range dependents[name] {
			indegree[dep]--
			if indegree[dep] == 0 {
				ready = append(ready, dep)
			}
		}
	}
	if len(out) == len(names) {
		return out, nil
	}

	// The residual graph is exactly the stages that lie on (or hang off) a cycle. Walk
	// it to name the cycle itself, not just the leftovers, so the message points at the
	// document defect.
	cycle := findCycle(stages, indegree)
	return nil, fmt.Errorf("stage cycle: %s", strings.Join(cycle, " -> "))
}

// findCycle walks the stages that still have a non-zero indegree and returns one
// cycle's names, closed onto itself (`a -> b -> a`), so a reader can see the loop.
func findCycle(stages map[string]spec.Stage, indegree map[string]int) []string {
	// next[name] is the alphabetically first producer this stage still waits on — the
	// walk therefore follows edges that are known to be inside the residual graph.
	next := make(map[string]string, len(stages))
	for name, s := range stages {
		if indegree[name] == 0 {
			continue
		}
		producers := make([]string, 0, len(s.After))
		for _, p := range s.After {
			if _, ok := stages[string(p)]; ok && indegree[string(p)] > 0 {
				producers = append(producers, string(p))
			}
		}
		if len(producers) == 0 {
			continue // not actually in a cycle; guarded above
		}
		sort.Strings(producers)
		next[name] = producers[0]
	}
	if len(next) == 0 {
		return []string{"(unresolvable)"}
	}
	start := make([]string, 0, len(next))
	for name := range next {
		start = append(start, name)
	}
	sort.Strings(start)

	// Walk from each candidate until a node repeats; the first repeat closes a cycle.
	seen := map[string]int{}
	path := make([]string, 0, len(next))
	cur := start[0]
	for {
		if at, ok := seen[cur]; ok {
			// `after:` edges point producer -> consumer, so the walk goes BACKWARDS;
			// the cycle reads consumer -> producer here, which is the order the
			// document wrote the edges in once reversed.
			loop := append([]string{}, path[at:]...)
			loop = append(loop, cur)
			return loop
		}
		seen[cur] = len(path)
		path = append(path, cur)
		n, ok := next[cur]
		if !ok {
			return path
		}
		cur = n
	}
}
