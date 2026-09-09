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
	calver  string
	workdir string
	env     map[string]string
	ex      *sdk.Executor  // the host executor (single dial) for the ADE verb dispatch
	report  map[string]any // the entity's report: block (template/schema/bed_template)
	media   map[string]any // the entity's media: block (files/min/dir)
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
			if r, ok := ledgerRef(g[3]); ok && r.Outputs != nil {
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
	r, ok := ledgerRef(g[3])
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

var curLedger = &ledger{results: map[string]*StageResult{}}

func ledgerRef(id string) (*StageResult, bool) {
	r, ok := curLedger.results[id]
	return r, ok
}

// ---- stage execution ------------------------------------------------------

func runPlan(ctx context.Context, p params.PipelineInput, pr, calver, workdir string, ex *sdk.Executor) error {
	if calver == "" {
		calver = currentCalver() // the run's stamp ($calver refs + media dirs)
	}
	rc := &runCtx{pr: pr, calver: calver, workdir: workdir, env: envMap(), ex: ex}
	rc.report = mm(mapOf(p.Report))
	rc.media = mm(p.Media)

	l := newLedger()
	curLedger = l

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
	for _, raw := range p.Stages {
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
			if m, ok := raw["redo"].(map[string]any); ok {
				if tr, ok := m["triggers"].(map[string]any); ok {
					if t, ok := tr[res.Trigger].(string); ok {
						target = t
					}
				}
			}
			redoCount[target]++
			if redoCount[target] >= escalateAfter {
				l.put(&StageResult{ID: id, Status: "escalate", Trigger: res.Trigger, Message: "loop guard: " + target + " re-entered > " + strconv.Itoa(escalateAfter)})
				return fmt.Errorf("LOOP-GUARD: %s re-entered %d times (escalate)", target, redoCount[target])
			}
			if redoCount[target] > maxRedo {
				l.put(&StageResult{ID: id, Status: "escalate", Trigger: res.Trigger})
				return fmt.Errorf("LOOP-GUARD: exceed redo max %d for %s", maxRedo, target)
			}
			// re-run the target stage from the raw stage list (bounded)
			for _, tRaw := range p.Stages {
				if asString(tRaw["id"]) == target {
					res2, err2 := rc.runStage(ctx, asString(tRaw["kind"]), target, tRaw, l)
					res2.Duration = time.Since(start)
					l.put(res2)
					if err2 != nil {
						return fmt.Errorf("[redo %s] FAIL-HARD: %w", target, err2)
					}
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
func currentCalver() string {
	t := time.Now()
	_, w := t.ISOWeek()
	return fmt.Sprintf("%d.%02d.%s", t.Year(), w, t.Format("1504"))
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
	switch kind {
	case "agent":
		out, err := runAgentStage(ctx, rc, raw, l)
		res.Outputs = out
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
			ok, msg, val := runProbeV(v, verbInput)
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
		verdict, summary, aerr := adeVerdict(ctx, rc.pr, rc.workdir)
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
