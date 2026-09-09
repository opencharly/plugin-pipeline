package pluginpipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk"
)

// =============================================================================
// command:pipeline (P5) — the generic executor: read a kind:pipeline entity,
// run its stages in order, keep a ledger, enforce redo bounds + FAIL-HARD,
// evaluate references through the typed grammar ($pr / $calver / $workdir /
// $env.NAME / @stage.output).
// =============================================================================

// env overrides (the config contract, Section 3.1):
func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// per-run builtins (filled by runPlan):
type runCtx struct {
	pr      string
	repo    string // the eval TARGET repo (the pr tools' gh target) — authored on the entity
	calver  string
	workdir string
	env     map[string]string
	ledger  *ledger        // PER-RUN stage ledger — never shared (RCA 2026.252.2210: the package-global curLedger raced concurrent batch lanes)
	ex      *sdk.Executor  // the host executor (single dial) for the ADE verb dispatch
	report  map[string]any // the entity's report: block (template/schema/bed_template)
	llm     map[string]any // the entity's llm block (base_url/model/api_key)
	media   map[string]any // the entity's media: block (files/min/dir)
	skills  map[string]any // the entity's skills: block (corpus) — the agent-stage skill corpus
}

// StageResult is one ledger row.
type StageResult struct {
	ID       string
	Kind     string
	Status   string // ok | fail | skipped | escalate
	Outputs  map[string]any
	Duration time.Duration
	Trigger  string // redo-plan | redo-run | redo-read | escalate | ""
	Message  string
}

type ledger struct {
	results map[string]*StageResult
	order   []string
}

func newLedger() *ledger { return &ledger{results: map[string]*StageResult{}} }

func (l *ledger) put(r *StageResult) {
	if _, ok := l.results[r.ID]; !ok {
		l.order = append(l.order, r.ID)
	}
	l.results[r.ID] = r
}

// facts: the structured ledger facts for the agent user-message injection —
// every prior stage's outputs rendered compactly. The agent narrates from
// facts; it never has to guess the evidence layout to know what happened.
func (l *ledger) facts() string {
	if l == nil {
		return "(no ledger)"
	}
	var sb strings.Builder
	for _, id := range l.order {
		r := l.results[id]
		if r == nil {
			continue
		}
		sb.WriteString("- " + id + " [" + r.Kind + " " + r.Status + "]")
		if r.Message != "" {
			sb.WriteString(": " + truncate(r.Message, 300))
		}
		sb.WriteString("\n")
		for k, v := range r.Outputs {
			if k == "response" {
				continue
			}
			b, _ := json.Marshal(v)
			sb.WriteString("    " + k + " = " + truncate(string(b), 400) + "\n")
		}
	}
	return sb.String()
}

// ---- the reference grammar ----------------------------------------------
var refRe = regexp.MustCompile(`\$(pr|calver|workdir)|\$env\.([A-Z0-9_]+)|@([A-Za-z0-9_-]+)\.([A-Za-z0-9_-]+)(\.([A-Za-z0-9_-]+))?`)

// resolveRefs replaces $pr/$calver/$workdir/$env.NAME/@stage.output(.field) in a
// string. Values are typed for stage inputs (see resolveValue).
func (rc *runCtx) resolveRefs(s string) string {
	return refRe.ReplaceAllStringFunc(s, func(m string) string {
		g := refRe.FindStringSubmatch(m)
		switch {
		case g[1] != "":
			switch g[1] {
			case "pr":
				return rc.pr
			case "calver":
				return rc.calver
			case "workdir":
				return rc.workdir
			}
		case g[2] != "":
			if v, ok := rc.env[g[2]]; ok {
				return v
			}
			return ""
		case g[3] != "":
			if r, ok := rc.ledgerRef(g[3]); ok && r.Outputs != nil {
				v, _ := r.Outputs[g[4]]
				if g[6] != "" {
					if m, ok := v.(map[string]any); ok {
						if f, ok2 := m[g[6]]; ok2 {
							if f == nil {
								return "" // a missing field renders EMPTY, never \"null\"
							}
							return scalar(f)
						}
					}
					return ""
				}
				return scalar(v)
			}
		}
		return ""
	})
}

// typedRef resolves @stage.output(.field) to its TYPED ledger value (not a
// scalar) — powers array-valued vars like the oracle checks.
func (rc *runCtx) typedRef(s string) (any, bool) {
	g := refRe.FindStringSubmatch(s)
	if g == nil || g[3] == "" {
		return nil, false
	}
	r, ok := rc.ledgerRef(g[3])
	if !ok || r.Outputs == nil {
		return nil, false
	}
	v, ok := r.Outputs[g[4]]
	if !ok {
		return nil, false
	}
	if g[6] != "" {
		if m, ok := v.(map[string]any); ok {
			f, found := m[g[6]]
			return f, found
		}
		return nil, false
	}
	return v, true
}

// resolveValue returns a typed value for YAML inputs (strings are ref-resolved;
// maps/slices are resolved recursively — never shell text).
func (rc *runCtx) resolveValue(v any) any {
	switch t := v.(type) {
	case string:
		// typed stage refs (@stage.output or @stage.output.field) keep their
		// TYPE (e.g. the oracle checks array) — generic strings go through
		// resolveRefs (scalar). @github refs stay literal.
		if strings.HasPrefix(t, "@") && !strings.HasPrefix(t, "@github") {
			if tv, ok := rc.typedRef(t); ok {
				return tv
			}
		}
		return rc.resolveRefs(t)
	case map[string]any:
		m := map[string]any{}
		for k, x := range t {
			m[k] = rc.resolveValue(x)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = rc.resolveValue(x)
		}
		return out
	default:
		return v
	}
}

func scalar(v any) string {
	if v == nil {
		return "" // nil never renders \"null\" in a template
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func (rc *runCtx) ledgerRef(id string) (*StageResult, bool) {
	if rc == nil || rc.ledger == nil {
		return nil, false
	}
	r, ok := rc.ledger.results[id]
	return r, ok
}

// ---- stage execution ------------------------------------------------------

func runPlan(ctx context.Context, p params.PipelineInput, pr, calver, workdir string, ex *sdk.Executor) error {
	return runPlanL(ctx, p, pr, calver, workdir, ex, newLedger())
}

// runPlanL: the run body with the ledger EXPLICIT (per-run state, RCA
// 2026.252.2210 — the batch lanes each own their ledger; nothing is global).
func runPlanL(ctx context.Context, p params.PipelineInput, pr, calver, workdir string, ex *sdk.Executor, l *ledger) error {
	if calver == "" {
		calver = currentCalver() // the run's stamp ($calver refs + media dirs)
	}
	rc := &runCtx{pr: pr, repo: p.Repo, calver: calver, workdir: workdir, env: envMap(), ex: ex, ledger: l}
	// the lane identity is bound PER-RUN here — never via os.Setenv, which is
	// process-global and raced the concurrent batch lanes (RCA 2026.252.2210).
	// $env.PR_NUMBER resolves to THIS lane's PR; $env.PR_HEAD_SHA to this
	// lane's head (an operator-set PR_HEAD_SHA still wins — a deliberate pin).
	if pr != "" {
		rc.env["PR_NUMBER"] = pr
		if _, ok := rc.env["PR_HEAD_SHA"]; !ok {
			rc.env["PR_HEAD_SHA"] = headSHA(pr, rc.repo)
		}
	}
	rc.report = mm(mapOf(p.Report))
	rc.llm = mm(p.Llm)
	rc.media = mm(p.Media)
	rc.skills = mm(mapOf(p.Skills))

	maxRedo := int(p.Redo.Max)
	if maxRedo <= 0 {
		maxRedo = 2
	}
	escalateAfter := int(p.Redo.Escalate_after)
	if escalateAfter <= 0 {
		escalateAfter = 3
	}
	redoCount := map[string]int{}
	// gates first (deterministic, fail-hard)
	if err := runGates(rc, p.Gates); err != nil {
		return err
	}
	// the redo loop: an indexed walk so a trigger can RESTART the chain from
	// the target stage (re-running the target AND every stage after it) — the
	// redo edges are sound: redo-plan re-runs bed-render -> control ->
	// config-audit -> eval -> ..., never a stale inline re-run.
	for i := 0; i < len(p.Stages); i++ {
		raw := p.Stages[i]
		// decode the discriminator
		kind := asString(raw["kind"])
		id := asString(raw["id"])
		if kind == "" || id == "" {
			return fmt.Errorf("pipeline: stage missing kind/id")
		}
		start := time.Now()
		res, err := rc.runStage(ctx, kind, id, raw, l)
		fmt.Printf("[stage %s] %s%s\n", id, res.Status, statusSuffix(res))
		res.Duration = time.Since(start)
		if res.ID == "" {
			res.ID = id
		}
		l.put(res)
		if err != nil {
			// FAIL-HARD (with the ledger for evidence)
			if workdir != "" {
				_ = dumpLedger(l, filepath.Join(workdir, "stage-findings.yml"))
			}
			return fmt.Errorf("[stage %s] FAIL-HARD: %w", id, err)
		}
		if res.Status == "escalate" {
			return fmt.Errorf("[stage %s] LOOP-GUARD: escalated (redo entrances > %d)", id, escalateAfter)
		}
		if res.Trigger != "" {
			target := res.Trigger
			// the per-stage redo budget (the stage's redo.max/escalate_after)
			// overrides the entity defaults — the informed agent retry can be
			// generous (cheap) while the VM redo edges stay tight.
			stageMax, stageEsc := maxRedo, escalateAfter
			if m, ok := raw["redo"].(map[string]any); ok {
				if tr, ok := m["triggers"].(map[string]any); ok {
					if t, ok := tr[res.Trigger].(string); ok {
						target = t
					}
				}
				if mx, ok := m["max"].(int); ok && mx > 0 {
					stageMax = mx
				}
				if es, ok := m["escalate_after"].(int); ok && es > 0 {
					stageEsc = es
				}
			}
			redoCount[target]++
			if redoCount[target] >= stageEsc {
				l.put(&StageResult{ID: id, Status: "escalate", Trigger: res.Trigger, Message: "loop guard: " + target + " re-entered > " + strconv.Itoa(stageEsc)})
				return fmt.Errorf("LOOP-GUARD: %s re-entered %d times (escalate)", target, redoCount[target])
			}
			if redoCount[target] > stageMax {
				l.put(&StageResult{ID: id, Status: "escalate", Trigger: res.Trigger})
				return fmt.Errorf("LOOP-GUARD: exceed redo max %d for %s", stageMax, target)
			}
			// RESTART the chain from the target stage (bounded by the redo
			// counters above) — the target and every stage after it re-run.
			for j, tRaw := range p.Stages {
				if asString(tRaw["id"]) == target {
					i = j - 1 // the loop's i++ lands on the target
					break
				}
			}
		}
	}
	if workdir != "" {
		_ = dumpLedger(l, filepath.Join(workdir, "stage-findings.yml"))
	}
	return nil
}

func statusSuffix(res *StageResult) string {
	if res.Message != "" {
		return " — " + res.Message
	}
	return ""
}

// currentCalver: the charly-style calver stamp (year.week.hhmm) for a run.
// currentCalver: the ONE calver source — YYYY.DDD.HHMM (the day of year),
// the SAME scheme the org's check-run stamps its run dirs with. The
// week-number derivation (YYYY.WW.HHMM) is GONE: the media/report calver and
// the .check run-dir calver were two different schemes in one lane, so a
// fresh report referenced week-numbered dirs while the runs lived under
// day-numbered ones (RCA 2026-09-10).
func currentCalver() string {
	t := time.Now()
	return fmt.Sprintf("%d.%03d.%s", t.Year(), t.YearDay(), t.Format("1504"))
}

// evalCond evaluates a simple stage condition of the form @stage.output == VALUE
// against the ledger (the skip_when contract).
// evalCond: the skip_when contract — supports == and != with @stage refs and
// $env.NAME refs (the publish gate's designed skip: EVAL_PUBLISH unset -> the
// run reports the stage skipped, never failed — the R10 completes at ZERO
// failures; validator finding R10/B12).
func (rc *runCtx) evalCond(cond string) bool {
	// the OR grammar: "A || B" — either side true (the AND form is not
	// needed; the skip_when contract is a single gate).
	if parts := strings.Split(cond, " || "); len(parts) > 1 {
		for _, p := range parts {
			if rc.evalCond(strings.TrimSpace(p)) {
				return true
			}
		}
		return false
	}
	eq := strings.SplitN(cond, " == ", 2)
	neq := strings.SplitN(cond, " != ", 2)
	var ref, want string
	negate := false
	switch {
	case len(eq) == 2:
		ref, want = eq[0], eq[1]
	case len(neq) == 2:
		ref, want, negate = neq[0], neq[1], true
	default:
		return false
	}
	ref = strings.TrimSpace(ref)
	want = strings.Trim(strings.TrimSpace(want), "\"")
	// the $env fallback: resolveRefs only substitutes the env when the ref is
	// exactly an env ref (typedRef handles the @stage refs)
	val := ""
	if tv, ok := rc.typedRef(ref); ok {
		val = fmt.Sprint(tv)
	} else {
		val = rc.resolveRefs(ref)
	}
	match := val == want
	if negate {
		return !match
	}
	return match
}

func mapOf(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func envMap() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (rc *runCtx) runStage(ctx context.Context, kind, id string, raw map[string]any, l *ledger) (*StageResult, error) {
	res := &StageResult{ID: id, Kind: kind, Status: "ok"}
	// skip_when: a stage-level condition (e.g. "@eval.verdict == FAIL") that
	// records the stage as skipped instead of failing - the report still
	// renders for a FAILED eval (the media gate is only meaningful on a
	// passing run). Applies to every stage kind that declares it.
	if sw := asString(raw["skip_when"]); sw != "" {
		if rc.evalCond(sw) {
			res.Status = "skipped"
			res.Message = "skipped: " + sw
			return res, nil
		}
	}
	switch kind {
	case "agent":
		out, err := runAgentStage(ctx, rc, raw, l)
		res.Outputs = out
		if re, ok := err.(*redoError); ok {
			res.Status = "fail"
			res.Trigger = re.trigger
			res.Message = re.msg
			return res, nil
		}
		return res, err
	case "probe":
		verbs := strList(raw["verbs"])
		input := rc.resolveValue(anyMap(raw["input"]))
		inputMap, _ := input.(map[string]any)
		// path-valued inputs (bed/dir) are workdir-relative: root them at the
		// run workdir (the pipeline may run from a different cwd, e.g. the eval
		// lane's beds live in the eval-omarchy worktree).
		if inputMap != nil && rc.workdir != "" {
			for _, k := range []string{"bed", "dir"} {
				if v := s(inputMap[k]); v != "" && !filepath.IsAbs(v) {
					inputMap[k] = filepath.Join(rc.workdir, v)
				}
			}
		}
		for _, v := range verbs {
			// per-verb scoped inputs: the entity may nest each verb's input under
			// its name (media_gate: {...}) — use that when present.
			verbInput := inputMap
			if ni, ok := inputMap[v].(map[string]any); ok {
				verbInput = ni
			}
			// the offline fixture mode (plan §2.3): a fixture: true input runs the
			// probe against canned results - no live infra - so any pipeline's
			// probes are testable offline.
			if verbInput == nil {
				verbInput = map[string]any{}
			}
			if f, _ := verbInput["fixture"].(bool); f {
				if res.Outputs == nil {
					res.Outputs = map[string]any{}
				}
				res.Outputs[v] = "pass"
				continue
			}
			ok, msg, val := runProbeV(v, verbInput, rc)
			if !ok {
				res.Status = "fail"
				res.Message = msg
				res.Trigger = triggerOnFail(raw, "redo-plan")
				return res, fmt.Errorf("probe %s failed: %s", v, msg)
			}
			if res.Outputs == nil {
				res.Outputs = map[string]any{}
			}
			res.Outputs[v] = "pass"
			if val != nil {
				res.Outputs["value"] = val
			}
		}
		// expose the probe VALUE: the declared outputs map to it (e.g. resolve_channel
		// -> channel), and map values spread as named outputs (golden/provision).
		for _, o := range strList(raw["outputs"]) {
			if val, found := res.Outputs["value"]; found && o == "channel" {
				res.Outputs[o] = val
			}
		}
		if val, found := res.Outputs["value"]; found {
			if m, ok := val.(map[string]any); ok {
				for k, v := range m {
					res.Outputs[k] = v
				}
			} else {
				res.Outputs["channel"] = val
			}
		}
		delete(res.Outputs, "value")
		return res, nil
	case "ade":
		// the org-wide ADE: run the rendered ORACLE bed through the host's compiled-in
		// check-run ONCE (the org R10 machinery + its ADE agent-check grading + the
		// --var per-PR passthrough). The deterministic exit contract maps to the
		// report verdict. (RCA: the external CLI dispatch has no reverse-channel
		// executor - see ade.go header.)
		var verdict, summary string
		var aerr error
		// the DECLARED bed (resolved) — the control bed runs the same ADE
		// machinery on its own entity; the hardcoded -vm name is gone.
		bed := rc.resolveRefs(asString(raw["bed"]))
		if rc.ex != nil {
			// the compiled-in placement: drive the bed's plan in-process (no charly spawn)
			verdict, summary, _, aerr = runAdeBedKit(ctx, rc.pr, bed, rc.workdir, rc.ex)
		} else {
			// the un-compiled placement: the external charly check-run fallback
			verdict, summary, aerr = adeVerdict(ctx, rc.pr, bed, rc.workdir)
		}
		if aerr != nil {
			res.Status = "fail"
			res.Message = aerr.Error()
			return res, aerr
		}
		if res.Outputs == nil {
			res.Outputs = map[string]any{}
		}
		res.Outputs["verdict"] = verdict
		res.Outputs["summary"] = summary
		res.Message = summary
		// fail_on: the declared verdicts are LANE DEFECTS, not eval outcomes —
		// the stage fails with the redo trigger (the SETUP_DEFECT path) instead
		// of flowing a worthless verdict downstream. The verdict is a GATE now.
		for _, fv := range strList(raw["fail_on"]) {
			if verdict == fv {
				res.Status = "fail"
				res.Trigger = "setup-defect"
				return res, nil
			}
		}
		return res, nil
	case "generate":
		if err := rc.runGenerate(raw); err != nil {
			res.Status = "fail"
			res.Message = err.Error()
			return res, err
		}
		return res, nil
	case "media":
		if err := rc.runMedia(raw, l); err != nil {
			res.Status = "fail"
			res.Message = err.Error()
			return res, err
		}
		return res, nil
	case "gate":
		if ok, msg := evalCondition(rc.resolveRefs(asString(raw["condition"]))); !ok {
			res.Status = "fail"
			res.Message = msg
			return res, fmt.Errorf("gate %s: %s", id, msg)
		}
		return res, nil
	case "command": // EXTERNAL processes ONLY (never charly)
		cmd := exec.CommandContext(ctx, "sh", "-c", rc.resolveRefs(asString(raw["command"])))
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			res.Status = "fail"
			res.Message = strings.TrimSpace(string(out))
			return res, fmt.Errorf("command %s: %w", id, err)
		}
		res.Outputs = map[string]any{"stdout": string(out)}
		return res, nil
	}
	return res, fmt.Errorf("unknown stage kind %q", kind)
}

func strList(v any) []string {
	if x, ok := v.([]any); ok {
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
func anyMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func triggerOnFail(raw map[string]any, def string) string {
	if m, ok := raw["redo"].(map[string]any); ok {
		if v, ok := m["on_fail"]; ok {
			switch t := v.(type) {
			case string:
				return t
			case []any:
				if len(t) > 0 {
					if s, ok := t[0].(string); ok {
						return s
					}
				}
			}
		}
	}
	return def
}

func runGates(rc *runCtx, gates []string) error {
	for _, g := range gates {
		ok, msg := runProbe(g, map[string]any{})
		if !ok {
			return fmt.Errorf("gate %s: %s", g, msg)
		}
	}
	return nil
}

func dumpLedger(l *ledger, path string) error {
	sort.Strings(l.order)
	var sb strings.Builder
	for _, id := range l.order {
		r := l.results[id]
		sb.WriteString(fmt.Sprintf("- stage: %s\n  kind: %s\n  status: %s\n  trigger: %s\n  message: %q\n",
			r.ID, r.Kind, r.Status, r.Trigger, r.Message))
		if len(r.Outputs) > 0 {
			b, _ := json.Marshal(r.Outputs)
			sb.WriteString("  outputs: " + string(b) + "\n")
		}
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// runCheckRun invokes the EXISTING check executor (charly check run <bed>) —
// the canonical bed runner, orchestrated by this engine. exit code 2 = step fail.
func runCheckRun(ctx context.Context, bed, workdir string) int {
	// the charly that runs the pipeline: CHARLY_BIN override (the runner pins the
	// released binary; a dev/worktree charly on PATH resolves beds differently).
	charlyBin := os.Getenv("CHARLY_BIN")
	if charlyBin == "" {
		charlyBin = "charly"
	}
	args := []string{"check", "run", bed}
	if workdir != "" {
		args = append([]string{"-C", workdir}, args...)
	}
	cmd := exec.CommandContext(ctx, charlyBin, args...)
	cmd.Env = os.Environ()
	_ = cmd.Run()
	return cmd.ProcessState.ExitCode()
}

func teardownBed(bed string) error {
	o, _ := exec.Command("charly", "check", "stop", bed).CombinedOutput()
	_ = o
	o2, _ := exec.Command("charly", "vm", "destroy", bed, "--if-exists").CombinedOutput()
	_ = o2
	return nil
}

// evalCondition: a minimal condition evaluator: "A == B && C in [X, Y]".
func evalCondition(cond string) (bool, string) {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true, ""
	}
	for _, part := range strings.Split(cond, "&&") {
		part = strings.TrimSpace(part)
		switch {
		case strings.Contains(part, " in ["):
			lhs := strings.TrimSpace(strings.SplitN(part, " in [", 2)[0])
			rest := strings.SplitN(part, " in [", 2)[1]
			rest = strings.TrimSuffix(rest, "]")
			rest = strings.TrimSuffix(rest, "]")
			ok := false
			for _, cand := range strings.Split(rest, ",") {
				if strings.TrimSpace(lhs) == strings.TrimSpace(strings.TrimSpace(cand)) {
					ok = true
				}
			}
			if !ok {
				return false, "condition false: " + part
			}
		case strings.Contains(part, "=="):
			kv := strings.SplitN(part, "==", 2)
			if strings.TrimSpace(kv[0]) != strings.TrimSpace(strings.Trim(kv[1], "\"")) {
				return false, "condition false: " + part
			}
		}
	}
	return true, ""
}

var _ = io.Discard
