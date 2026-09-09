package pluginpipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/opencharly/plugin-gh/candy/plugin-gh/gh"
	"gopkg.in/yaml.v3"
)

// tools.go — the agent tool catalog. Tools are capability REFERENCES declared per
// stage; buildTools renders the function-tool JSON; dispatchTool executes calls.
// PR tools -> ghkit (github.com/opencharly/plugin-gh/gh) — the CANONICAL GitHub
// client (R3, RCA 2026.252.2233: the hand-rolled exec.Command("gh") subprocesses
// swallowed gh's stderr and depended on the ambient env that never reaches the
// executor subprocess). The gh subprocess is GONE — the client talks to
// api.github.com directly with an explicit token contract.

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }

type toolFn struct{ name, desc string }

func fn(name, desc string) toolSchema {
	return fnArgs(name, desc, map[string]any{}, []string{})
}

// fnArgs: a tool with a REAL parameter schema — the model must be able to pass
// the stage/path it targets (RCA 2026.252.2250: the ledger tools dispatched
// but the empty property schema meant every call arrived arg-less).
func fnArgs(name, desc string, props map[string]any, required []string) toolSchema {
	return toolSchema{Type: "function", Function: functionSchema{
		Name: name, Description: desc,
		Parameters: json.RawMessage(jsonStr(map[string]any{"type": "object", "properties": props, "required": required})),
	}}
}

var toolCatalog = map[string][]toolSchema{
	"pr": {
		fn("get_pr_diff", "CURRENT unified diff (head vs base) of the PR."),
		fn("get_pr_commits", "Commit history of the PR (sha, message, author)."),
		fn("get_pr_thread", "CURRENT live issue body (authoritative) plus all prior comments."),
		fn("get_pr_meta", "PR metadata: title, state, draft, mergeable, head/base refs, file count."),
	},
	"pipeline": {
		fn("media_gate", "Assert the media artifacts exist with the min sizes."),
		fn("lock_audit", "Assert zero write-lock incidents in the run trees."),
		fn("evidence_audit", "Audit media and locks for the evidence packet."),
		fn("config_audit", "CONFIG AUDIT: the oracle bed against the lane rules."),
		fn("resolve_channel", "Resolve the PR channel from the channels registry."),
		fn("head_freshness", "Assert the plan head SHA equals the live PR head."),
		fn("sequencing", "The deterministic sequencing gate."),
		fn("golden_present", "Assert the golden disk exists and is unheld."),
		fn("lanes_ok", "Report the concurrency budget."),
	},
	"ledger": {
		fnArgs("stage_output", "Read a prior stage output from THIS RUN's ledger.", map[string]any{"stage": map[string]any{"type": "string", "description": "the stage id (e.g. triage, eval)"}}, []string{"stage"}),
		fnArgs("run_outcomes", "The structured step outcomes of a bed's LATEST check run: every step's id, name, ok/fail/skip, and the run verdict. The tool resolves the latest run dir itself — never guess paths.", map[string]any{"bed": map[string]any{"type": "string", "description": "the bed entity name (e.g. check-omarchy-pr-10115-vm)"}}, []string{"bed"}),
		fn("ledger_facts", "The structured ledger facts of THIS run: every prior stage's outputs (the triage plan, the eval verdict + summary, the control result)."),
	},
}

func buildTools(refs []string) []toolSchema {
	var out []toolSchema
	for _, ref := range refs {
		group, ok := toolCatalog[strings.TrimSpace(ref)]
		if !ok {
			continue
		}
		out = append(out, group...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Function.Name < out[j].Function.Name })
	return out
}

func toolFail(msg string) string { return jsonStr(map[string]string{"status": "fail", "message": msg}) }

// ghClient is a PROCESS-WIDE lazily-built client (http.Client is concurrency-
// safe; the client carries no per-lane state — the race lesson of RCA
// 2026.252.2210 is about per-LANE state, which stays in the runCtx).
var ghOnce sync.Once
var ghClient *gh.Client

func ghc() *gh.Client {
	ghOnce.Do(func() {
		ghClient, _ = gh.New() // a missing token is not fatal: public reads work; auth surfaces at the call
	})
	return ghClient
}

// dispatchTool executes one tool call.
func dispatchTool(name, arguments string, rc *runCtx) string {
	switch name {
	case "get_pr_diff", "get_pr_commits", "get_pr_thread", "get_pr_meta":
		return prTool(name, rc)
	case "media_gate", "lock_audit", "evidence_audit", "config_audit", "resolve_channel",
		"head_freshness", "sequencing", "golden_present", "lanes_ok":
		ok, msg := runProbe(name, map[string]any{})
		if !ok {
			return toolFail(msg)
		}
		return jsonStr(map[string]string{"status": "pass"})
	case "stage_output":
		return stageOutputTool(arguments, rc)
	case "run_outcomes":
		return runOutcomesTool(arguments, rc)
	case "ledger_facts":
		return ledgerFactsTool(rc)
	}
	return jsonStr(map[string]string{"error": "unknown tool"})
}

// prRef resolves the PR identity for the tools: the lane's own runCtx wins
// (per-lane, race-free), the process env is the CLI fallback only. The repo
// is global (the eval target), never per-lane.
func prRef(rc *runCtx) (pr, repo string) {
	// the ENTITY-authored repo wins: the plugin is served as an out-of-process
	// executor subprocess whose env is the executor's declared contract, NOT the
	// operator's shell env (RCA 2026.252.2233: EVAL_REPO never reached the
	// plugin process — the tools gh-failed 404 and the model skipped).
	if rc != nil && rc.repo != "" {
		repo = rc.repo
	} else {
		repo = os.Getenv("EVAL_REPO")
		if repo == "" {
			repo = os.Getenv("GITHUB_REPOSITORY")
		}
	}
	if rc != nil && rc.pr != "" {
		return rc.pr, repo
	}
	pr = os.Getenv("PR_NUMBER")
	if pr == "" {
		pr = "0"
	}
	return pr, repo
}

// prTool: the four PR tools via ghkit — the canonical client. The lane
// identity (pr/repo) comes from the runCtx; every failure carries the REAL
// diagnostic (the HTTP status + body), never a bare "failed".
func prTool(name string, rc *runCtx) string {
	pr, repo := prRef(rc) // prRef returns (pr, repo) — the swapped assignment built /repos/10144/pulls/omacom/omarchy → the 404 mystery (RCA 2026.252.2250)
	p, err := parseInt(pr)
	if err != nil {
		return jsonStr(map[string]string{"error": "pr number invalid: " + pr})
	}
	c := ghc()
	ctx := context.Background()
	switch name {
	case "get_pr_diff":
		d, err := c.PRDiff(ctx, repo, p)
		if err != nil {
			return jsonStr(map[string]any{"error": err.Error()})
		}
		return truncate(d, 96*1024)
	case "get_pr_commits":
		cs, err := c.PRCommits(ctx, repo, p)
		if err != nil {
			return jsonStr(map[string]any{"error": err.Error()})
		}
		return jsonStr(cs)
	case "get_pr_thread":
		th, err := c.PRThread(ctx, repo, p)
		if err != nil {
			return jsonStr(map[string]any{"error": err.Error()})
		}
		return jsonStr(map[string]any{"current_body": th.Body, "comments": th.Comments})
	case "get_pr_meta":
		m, err := c.PRMeta(ctx, repo, p)
		if err != nil {
			return jsonStr(map[string]any{"error": err.Error()})
		}
		return jsonStr(map[string]any{
			"title": m.Title, "state": m.State, "draft": m.Draft, "mergeable": m.Mergeable,
			"head_sha": m.HeadSHA, "base": m.Base, "head": m.Head, "changed_files": m.FileCount,
		})
	}
	return "{}"
}

// headSHA — the live head-sha lookup via ghkit (the gh-subprocess version
// rode the same ambient-env boundary; the client is the canonical path).
func headSHA(pr, repo string) string {
	p, err := parseInt(pr)
	if err != nil {
		return ""
	}
	if repo == "" {
		return ""
	}
	s, err := ghc().HeadSHA(context.Background(), repo, p)
	if err != nil {
		return ""
	}
	return s
}

// stageOutputTool: read a PRIOR stage's output from THIS RUN's ledger — the
// run context, never a global (the RCA 2026.252.2210 ledger-race lesson).
func stageOutputTool(arguments string, rc *runCtx) string {
	var in struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal([]byte(arguments), &in)
	if in.Stage == "" {
		return jsonStr(map[string]string{"error": "stage required"})
	}
	r, ok := rc.ledgerRef(in.Stage)
	if !ok || r.Outputs == nil {
		return jsonStr(map[string]string{"error": "stage " + in.Stage + " not in the ledger"})
	}
	return jsonStr(r.Outputs)
}

// runOutcomesTool: the structured step outcomes of a bed's LATEST check run.
// The tool resolves the latest calver dir itself (.check/<bed>/<calver>/) —
// the agent NEVER guesses paths (the 952-failure era is gone).
func runOutcomesTool(arguments string, rc *runCtx) string {
	var in struct {
		Bed string `json:"bed"`
	}
	_ = json.Unmarshal([]byte(arguments), &in)
	if in.Bed == "" {
		return jsonStr(map[string]string{"error": "bed required"})
	}
	base := filepath.Join(rc.workdir, ".check", in.Bed)
	entries, err := os.ReadDir(base)
	if err != nil {
		return jsonStr(map[string]string{"error": "no check runs for bed " + in.Bed + ": " + err.Error()})
	}
	// the latest calver dir (the run stamp sorts lexically: YYYY.DDD.HHMM).
	latest := ""
	for _, e := range entries {
		if e.IsDir() && e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		return jsonStr(map[string]string{"error": "no run dirs for bed " + in.Bed})
	}
	summaryPath := filepath.Join(base, latest, "summary.yml")
	b, err := os.ReadFile(summaryPath)
	if err != nil {
		return jsonStr(map[string]string{"error": "read failed: " + err.Error()})
	}
	var sum struct {
		Steps []struct {
			Name string `yaml:"name"`
			OK   bool   `yaml:"ok"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(b, &sum); err != nil {
		return jsonStr(map[string]string{"error": "summary parse failed: " + err.Error()})
	}
	steps := make([]map[string]any, 0, len(sum.Steps))
	for _, st := range sum.Steps {
		status := "fail"
		if st.OK {
			status = "ok"
		}
		steps = append(steps, map[string]any{"name": st.Name, "status": status})
	}
	return jsonStr(map[string]any{"bed": in.Bed, "calver": latest, "steps": steps})
}

// ledgerFactsTool: the structured ledger facts of THIS run — the agent's
// context injection, on demand.
func ledgerFactsTool(rc *runCtx) string {
	if rc == nil || rc.ledger == nil {
		return jsonStr(map[string]string{"error": "no ledger"})
	}
	return jsonStr(map[string]string{"facts": rc.ledger.facts()})
}
