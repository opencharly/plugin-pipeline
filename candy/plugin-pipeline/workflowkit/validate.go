package workflowkit

import (
	"errors"
	"fmt"
	"strings"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	sdkworkflowkit "github.com/opencharly/sdk/workflowkit"
	"github.com/opencharly/spec/spec"
)

// validate.go — the workflow IR validator. It is the ONE gate every engine
// (plugin-lobster today, any future engine) runs before lowering: the authored
// `kind: pipeline` body must be well-formed, self-consistent, and expressible in
// lobster's grammar, so a workflow never fails halfway through a live run.

// execArms are the mutually exclusive execution arms of a step/sub-step. Exactly one
// must be set: a step that sets none does nothing, a step that sets two is ambiguous.
//
// `for_each` and `lanes` are the two LOOP arms, listed together: `for_each` walks its
// collection IN ORDER (upstream's own semantics), while `lanes` runs the same body
// through a pool of `batch_size` CONCURRENT lanes. They are distinct arms, never
// aliases — a step cannot be sequential and concurrent at once — so both are listed
// here and the XOR refuses a step that sets both.
//
// `steps:` is deliberately NOT here: it is the loop BODY, not an arm — `spec` groups
// it with the other loop companions ("--- for_each / lanes companions (meaningful only
// alongside for_each or lanes) ---", PipelineStepBase). Counting it as an arm made every
// valid for_each step set TWO arms and be rejected. A `steps:` with neither loop arm is
// caught by the stray-companion rule below instead.
var execArms = []string{"run", "pipeline", "workflow", "parallel", "for_each", "lanes", "input", "plan", "charly", "approval"}

// waitModes is the closed `parallel.wait` vocabulary (upstream lobster).
var waitModes = []string{"", "all", "any"}

// ValidatePipeline validates ONE authored pipeline: the exec-arm XOR, the co-field
// rules, unique step ids (parallel branches and for_each `steps:` included), step
// references to EARLIER steps only IN THE SCOPE THAT HOLDS THEM, the lobster
// expression grammar on every `when:`, and `item_var` ≠ `index_var`. It returns a joined
// error naming every problem found (never just the first), so an author fixes one round.
//
// "In the scope that holds them" is the whole point of this gate being a model of the
// engine rather than a stricter cousin: a top-level step reads the pipeline's earlier
// steps, a `parallel:` branch reads the pipeline's earlier steps (its siblings are
// CONCURRENT, so none of them has run yet), and a LOOP BODY reads its own earlier
// sub-steps on top of the enclosing set (validateLoopBody).
func ValidatePipeline(p *params.Pipeline) error {
	if p == nil {
		return errors.New("pipeline is nil")
	}
	var errs []error
	seen := map[string]string{} // id -> where declared, for the uniqueness diagnostic
	defined := map[string]bool{}
	knobs := knobNames(p.Config)

	for i := range p.Steps {
		s := &p.Steps[i]
		where := fmt.Sprintf("steps[%d]", i)
		if s.Id == "" {
			errs = append(errs, fmt.Errorf("%s: id is required", where))
		} else {
			where = fmt.Sprintf("step %q", s.Id)
			if prev, dup := seen[s.Id]; dup {
				errs = append(errs, fmt.Errorf("%s: duplicate step id (already declared at %s)", where, prev))
			} else {
				seen[s.Id] = where
			}
		}
		errs = append(errs, validateStepBase(stepView{
			id: s.Id, when: s.When, stdin: s.Stdin, forEach: s.ForEach, lanes: s.Lanes,
			itemVar: s.ItemVar, indexVar: s.IndexVar, batchSize: s.BatchSize, pauseMs: s.PauseMs,
			run: s.Run, pipeline: s.Pipeline, workflow: s.Workflow, charly: len(s.Charly) > 0,
			plan: len(s.Plan) > 0, planRefs: planRefNames(s.Plan), steps: len(s.Steps) > 0, input: nonZeroInput(s.Input),
			approval: s.Approval != nil, parallelBranches: len(s.Parallel.Branches),
			parallelWait: s.Parallel.Wait, refsAmong: defined, knobs: knobs,
		}, where)...)

		for j := range s.Parallel.Branches {
			b := &s.Parallel.Branches[j]
			bwhere := fmt.Sprintf("%s.parallel.branches[%d]", where, j)
			if b.Id == "" {
				errs = append(errs, fmt.Errorf("%s: id is required", bwhere))
			} else {
				bwhere = fmt.Sprintf("%s branch %q", where, b.Id)
				if prev, dup := seen[b.Id]; dup {
					errs = append(errs, fmt.Errorf("%s: duplicate step id (already declared at %s)", bwhere, prev))
				} else {
					seen[b.Id] = bwhere
				}
			}
			// A branch keeps the TOP-LEVEL rule: `parallel:` branches are CONCURRENT, so no
			// branch has observed a sibling when it is validated, and the engine evaluates
			// each branch's `when:` against the scope as it stood before the fork.
			errs = append(errs, validateStepBase(stepView{
				id: b.Id, when: b.When, stdin: b.Stdin,
				run: b.Run, pipeline: b.Pipeline, plan: len(b.Plan) > 0, planRefs: planRefNames(b.Plan), charly: len(b.Charly) > 0,
				steps: false, input: false, approval: false, refsAmong: defined, knobs: knobs,
			}, bwhere)...)
		}

		// The loop BODY gets its OWN namespace — the one the engine gives it (validateLoopBody).
		errs = append(errs, validateLoopBody(s.Steps, where, defined, knobs, seen)...)

		if s.Id != "" {
			defined[s.Id] = true
		}
	}

	return errors.Join(errs...)
}

// validateLoopBody validates a loop arm's `steps:` body (for_each's and lanes' ONE body
// shape, spec's #PipelineSubStep) against the body's OWN namespace: a COPY of the
// enclosing earlier-step set, extended with each sub-step as it is validated, in document
// order.
//
// That is not a relaxation — it is the scope the engine already runs the body under.
// plugin-lobster's `runSubSteps` runs a body against a per-item COPY of the enclosing
// result set (`results.scoped`) and does `scope[sub.Id] = res` after each sub-step, so
// sub-step N reads every sub-step before it. Validating the body against the PIPELINE's
// top-level ids only made the gate stricter than the engine and refused every multi-stage
// loop body — the shape every faithful migration of a pipeline-level `lanes:` fan-out
// produces (opencharly/sdk#441).
//
// The rule is unchanged, earlier-only, and it stops at the body's edge on purpose: the
// per-item scope never merges back into the run's, so a sub-step id stays invisible to the
// top level (a later `$sub.*` is still an error), and a sub-step naming ITSELF or a LATER
// sibling is still an error. `seen` is the document-wide id-uniqueness map, shared with
// the top level exactly as before.
func validateLoopBody(subs []params.PipelineSubStep, where string, enclosing map[string]bool, knobs map[string]bool, seen map[string]string) []error {
	if len(subs) == 0 {
		return nil
	}
	// A COPY: a sub-step id must not leak into the pipeline's scope.
	body := make(map[string]bool, len(enclosing)+len(subs))
	for id := range enclosing {
		body[id] = true
	}
	var errs []error
	for j := range subs {
		b := &subs[j]
		bwhere := fmt.Sprintf("%s.steps[%d]", where, j)
		if b.Id == "" {
			errs = append(errs, fmt.Errorf("%s: id is required", bwhere))
		} else {
			bwhere = fmt.Sprintf("%s sub-step %q", where, b.Id)
			if prev, dup := seen[b.Id]; dup {
				errs = append(errs, fmt.Errorf("%s: duplicate step id (already declared at %s)", bwhere, prev))
			} else {
				seen[b.Id] = bwhere
			}
		}
		errs = append(errs, validateStepBase(stepView{
			id: b.Id, when: b.When, stdin: b.Stdin,
			run: b.Run, pipeline: b.Pipeline, plan: len(b.Plan) > 0, planRefs: planRefNames(b.Plan), charly: len(b.Charly) > 0,
			steps: false, input: false, approval: false, refsAmong: body, knobs: knobs,
		}, bwhere)...)
		// AFTER its own validation: a self-reference (`$b.*` inside `b`) stays an error.
		if b.Id != "" {
			body[b.Id] = true
		}
	}
	return errs
}

// stepView is the common shape of PipelineStep / PipelineSubStep for validation, so
// the rules live once (R3) instead of being copy-pasted per type.
type stepView struct {
	id, when, stdin, forEach, lanes string
	itemVar, indexVar               string
	batchSize, pauseMs              int64
	run, pipeline, workflow         string
	charly                          bool
	plan, steps, input, approval    bool
	planRefs                        []string
	parallelBranches                int
	parallelWait                    string
	refsAmong                       map[string]bool
	knobs                           map[string]bool
}

func validateStepBase(v stepView, where string) []error {
	var errs []error

	// exactly one exec arm.
	arms := map[string]bool{
		"run":      v.run != "",
		"pipeline": v.pipeline != "",
		"workflow": v.workflow != "",
		"parallel": v.parallelBranches > 0,
		"for_each": v.forEach != "",
		"lanes":    v.lanes != "",
		"input":    v.input,
		"plan":     v.plan,
		"charly":   v.charly,
		"approval": v.approval,
	}
	var set []string
	for _, a := range execArms {
		if arms[a] {
			set = append(set, a)
		}
	}
	switch len(set) {
	case 1:
	case 0:
		errs = append(errs, fmt.Errorf("%s: no execution arm — set exactly one of %s", where, strings.Join(execArms, "/")))
	default:
		errs = append(errs, fmt.Errorf("%s: %d execution arms set (%s) — set exactly one of %s", where, len(set), strings.Join(set, ", "), strings.Join(execArms, "/")))
	}

	// co-fields: the loop arms' shared body/binding and their distinct pacing knobs.
	errs = append(errs, validateLoopCoFields(v, where)...)
	if v.parallelBranches == 0 && v.parallelWait != "" {
		errs = append(errs, fmt.Errorf("%s: parallel.wait is only meaningful with non-empty parallel branches", where))
	}
	if v.parallelWait != "" && !contains(waitModes, v.parallelWait) {
		errs = append(errs, fmt.Errorf("%s: parallel.wait %q is not one of all/any", where, v.parallelWait))
	}
	if v.input && v.approval {
		errs = append(errs, fmt.Errorf("%s: input and approval are mutually exclusive", where))
	}

	// a plan: step's own `$ref`s are the SAME earlier-step-only rule. The plan is a
	// TEMPLATE, not an expression, so no expression replacement is offered here.
	errs = append(errs, checkRefNames(where+".plan", v.planRefs, v.refsAmong, v.knobs, false)...)

	// the lobster expression grammar + earlier-step-only references.
	if v.when != "" {
		if err := sdkworkflowkit.Validate(v.when); err != nil {
			errs = append(errs, fmt.Errorf("%s: when: %w", where, err))
		}
		// `when:` is the ONE expression site, so it is the one place the `${env.NAME}`
		// replacement is offered (see refReplacement).
		errs = append(errs, checkEarlierRefs(where+".when", v.when, v.refsAmong, v.knobs, true)...)
	}
	errs = append(errs, checkEarlierRefs(where+".stdin", v.stdin, v.refsAmong, v.knobs, false)...)
	if v.forEach != "" {
		errs = append(errs, checkEarlierRefs(where+".for_each", v.forEach, v.refsAmong, v.knobs, false)...)
	}
	// The lanes collection is the same kind of expression as for_each's — a runtime
	// collection naming an EARLIER step — so it gets the same rule. Without it the gate
	// would admit `lanes: $later.items` and the engine would fail at run time.
	if v.lanes != "" {
		errs = append(errs, checkEarlierRefs(where+".lanes", v.lanes, v.refsAmong, v.knobs, false)...)
	}
	return errs
}

// validateLoopCoFields enforces the shape the two LOOP arms share and the pacing knobs
// they do NOT share. `for_each` and `lanes` both carry a `steps:` body, `item_var` and
// `index_var` — the rules for those live once here (R2), so a rule added to one arm
// cannot fork from the other. Only the PACING differs, and each pacing rule names the
// arm it belongs to: `batch_size` is `lanes`' REQUIRED pool width (`for_each` may carry
// it as upstream's hint and ignores it), and `pause_ms` is `for_each`'s pause BETWEEN
// sequential iterations, which a lane pool does not have.
func validateLoopCoFields(v stepView, where string) []error {
	var errs []error

	if (v.forEach != "" || v.lanes != "") && !v.steps {
		errs = append(errs, fmt.Errorf("%s: for_each/lanes requires a non-empty steps list", where))
	}
	if v.forEach == "" && v.lanes == "" {
		var strays []string
		if v.steps {
			strays = append(strays, "steps")
		}
		if v.itemVar != "" {
			strays = append(strays, "item_var")
		}
		if v.indexVar != "" {
			strays = append(strays, "index_var")
		}
		if v.batchSize != 0 {
			strays = append(strays, "batch_size")
		}
		if len(strays) > 0 {
			errs = append(errs, fmt.Errorf("%s: %s only apply alongside for_each/lanes", where, strings.Join(strays, "/")))
		}
	}
	if v.lanes != "" {
		// The width is the WHOLE point of this arm, and a default would be a number the
		// author never wrote (an unbounded pool is the spawn-per-item `lanes` replaces),
		// so it is required rather than defaulted.
		if v.batchSize < 1 {
			errs = append(errs, fmt.Errorf("%s: lanes requires batch_size >= 1 (the pool width)", where))
		}
		// A pause BETWEEN sequential iterations is `for_each`'s pacing knob. A lane pool
		// paces by width, so honouring a pause here would mean silently doing neither.
		if v.pauseMs != 0 {
			errs = append(errs, fmt.Errorf("%s: lanes cannot define pause_ms — a lane pool paces by batch_size, not by a pause between iterations", where))
		}
	} else if v.pauseMs != 0 && v.forEach == "" {
		errs = append(errs, fmt.Errorf("%s: pause_ms only applies alongside for_each", where))
	}
	if v.itemVar != "" && v.itemVar == v.indexVar {
		errs = append(errs, fmt.Errorf("%s: item_var and index_var must differ (both %q)", where, v.itemVar))
	}
	return errs
}

// checkEarlierRefs enforces that every step reference in s names a step declared
// EARLIER in the pipeline — lobster resolves a `$ref` from already-finished steps
// only, so a forward (or self) reference is a load-time error, not a runtime one.
func checkEarlierRefs(where, s string, defined, knobs map[string]bool, expression bool) []error {
	return checkRefNames(where, sdkworkflowkit.RefNames(s), defined, knobs, expression)
}

// checkRefNames is the ONE earlier-step rule, for a list of reference ids. It is shared by
// the expression/template sites (`when:`, `stdin:`, `for_each:`, `lanes:`) and the `plan:`
// walker, so a rule added to one cannot fork from the other (R2).
func checkRefNames(where string, names []string, defined, knobs map[string]bool, expression bool) []error {
	var errs []error
	for _, name := range names {
		if defined[name] {
			continue
		}
		if hint := refReplacement(name, knobs, expression); hint != "" {
			errs = append(errs, fmt.Errorf("%s: reference $%s.* %s", where, name, hint))
			continue
		}
		errs = append(errs, fmt.Errorf("%s: reference $%s.* names a step that is not declared earlier", where, name))
	}
	return errs
}

// refReplacement names the REPLACEMENT for a `$<name>.*` reference whose name is not a step,
// or returns "" when the reference is simply an undeclared step. Two retired namespaces
// still survive in reshaped documents (opencharly/sdk#437) and each has a DIFFERENT
// disposition, so the gate must not answer both with the one message that sends the author
// looking for a step to declare:
//
//   - an entity-level KNOB block (`report`, `media`, `skills`, …) is now `config:` DATA.
//     The cutover deleted the POPULATOR that made a knob readable from a step (it was
//     `rc.report = mm(mapOf(p.Report))` in plugin-pipeline's retired executor.go) and kept
//     the readers, so there is NOTHING to restore: a `$<knob>.<field>` that got past this
//     gate would render the LITERAL TEXT at run time, since the reader it feeds is a dead
//     arm with a nil map — trading a load error for a run-time emptiness, which is worse.
//     The message therefore names the replacement: author the field's VALUE where it is
//     used. (plugin-pipeline#37 records the mechanism.)
//   - `env` is the RETIRED entity-env spelling. Its replacement `${env.NAME}` IS in the
//     lobster grammar (expr.go), so the message names it — the old spelling is refused (its
//     value would have to be spliced into the expression as text) while the reader is
//     pointed at the form that works.
//
// `expression` gates the env hint: `${env.NAME}` is readable only where an EXPRESSION is
// evaluated, so it is not offered at the template sites (`plan:`, `stdin:`, `for_each:`,
// `lanes:`), where pointing at it would be the same kind of half-truth this gate exists to
// avoid.
func refReplacement(name string, knobs map[string]bool, expression bool) string {
	if knobs[name] {
		return fmt.Sprintf("reads the entity-level config knob %q, not a step — config: is authored data "+
			"with no expression form, and the reader that once made a knob visible to a step was retired "+
			"with the populator, so nothing would resolve this at run time. Author the knob's field VALUE "+
			"where it is used instead", name)
	}
	if name == "env" && expression {
		return "is the retired entity-env spelling — write ${env.NAME}, the form the lobster grammar " +
			"accepts, to read the process environment"
	}
	return ""
}

// knobNames is the set of entity-level KNOB blocks a pipeline carries. They live in
// `config:` — spec's own comment names them ("the former pipeline knobs
// (llm/media/report/skills/channels/gates/repo/redo/agent), carried opaquely") — so the set
// is read off the document rather than hard-coded, and a knob a future engine does not know
// is still classified correctly.
func knobNames(cfg map[string]any) map[string]bool {
	if len(cfg) == 0 {
		return nil
	}
	names := make(map[string]bool, len(cfg))
	for name := range cfg {
		names[name] = true
	}
	return names
}

// ValidateWorkflowCycles detects a cycle in a workflow→workflow reference graph
// (`name -> the workflow names its steps reach`). It is the graph primitive behind
// the "workflow: cycles" rule: a pipeline that reaches itself transitively can never
// terminate. Returns the cycle chain (`a -> b -> a`).
func ValidateWorkflowCycles(refs map[string][]string) error {
	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	state := map[string]int{}
	var stack []string

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case onStack:
			return fmt.Errorf("workflow cycle: %s", strings.Join(append(stack, name), " -> "))
		case done:
			return nil
		}
		state[name] = onStack
		stack = append(stack, name)
		for _, next := range refs[name] {
			if err := visit(next); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return nil
	}
	for name := range refs {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// planRefNames returns the distinct `$step.path` references a plan's steps carry, in
// document order (the plan is marshalled and walked, so EVERY field is covered without
// enumerating the Op vocabulary here).
func planRefNames(plan []spec.Step) []string {
	if len(plan) == 0 {
		return nil
	}
	n, err := marshalNode(plan)
	if err != nil {
		return nil
	}
	return sdkworkflowkit.RefIDs(sdkworkflowkit.PlanRefs(n))
}

func nonZeroInput(i params.PipelineInput) bool {
	return i.Prompt != "" || len(i.ResponseSchema) > 0 || len(i.Defaults) > 0
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
