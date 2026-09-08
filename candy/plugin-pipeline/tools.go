package pluginpipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// tools.go — the agent tool catalog. Tools are capability REFERENCES declared per
// stage; buildTools renders the function-tool JSON; dispatchTool executes calls.
// PR tools -> the read-only gh API (canonical verb:pr operations, R3); probes and
// ledger/read -> engine-native. JSON outputs are built via json.Marshal (no
// hand-quoted literals).

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }

type toolFn struct{ name, desc string }

func fn(name, desc string) toolSchema {
	return toolSchema{Type: "function", Function: functionSchema{
		Name: name, Description: desc,
		Parameters: json.RawMessage(jsonStr(map[string]any{"type": "object", "properties": map[string]any{}})),
	}}
}

var toolCatalog = map[string][]toolSchema{
	"pr": {
		fn("get_pr_diff", "CURRENT unified diff (head vs base) of the PR."),
		fn("get_pr_commits", "Commit history of the PR (sha, message, author)."),
		fn("get_pr_thread", "CURRENT live issue body (authoritative) plus all prior comments."),
		fn("get_pr_meta", "PR metadata: title, state, mergeable, head/base sha, file counts."),
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
		fn("stage_output", "Read a prior stage output from the ledger."),
		fn("read_summary", "Read a run summary.yml from the evidence tree."),
	},
	"read": {
		fn("read_file", "Read a file from the evidence/media dirs."),
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

// dispatchTool executes one tool call.
func dispatchTool(name, arguments string) string {
	switch name {
	case "get_pr_diff", "get_pr_commits", "get_pr_thread", "get_pr_meta":
		return prTool(name)
	case "media_gate", "lock_audit", "evidence_audit", "config_audit", "resolve_channel",
		"head_freshness", "sequencing", "golden_present", "lanes_ok":
		ok, msg := runProbe(name, map[string]any{})
		if !ok {
			return toolFail(msg)
		}
		return jsonStr(map[string]string{"status": "pass"})
	case "read_file":
		return readFileTool(arguments)
	}
	return jsonStr(map[string]string{"error": "unknown tool"})
}

// prTool: the four PR tools via gh api (read-only).
func prTool(name string) string {
	repo := os.Getenv("EVAL_REPO")
	if repo == "" {
		repo = os.Getenv("GITHUB_REPOSITORY")
	}
	pr := os.Getenv("PR_NUMBER")
	if pr == "" {
		pr = "0"
	}
	switch name {
	case "get_pr_diff":
		out, err := exec.Command("gh", "api", "-H", "Accept: application/vnd.github.diff",
			fmt.Sprintf("/repos/%s/pulls/%s", repo, pr)).Output()
		if err != nil {
			return jsonStr(map[string]string{"error": "gh diff failed"})
		}
		return truncate(string(out), 96*1024)
	case "get_pr_commits":
		out, err := exec.Command("gh", "api", fmt.Sprintf("/repos/%s/pulls/%s/commits?per_page=100", repo, pr)).Output()
		if err != nil {
			return jsonStr(map[string]string{"error": "gh commits failed"})
		}
		return string(out)
	case "get_pr_thread":
		b, _ := exec.Command("gh", "api", fmt.Sprintf("/repos/%s/issues/%s", repo, pr)).Output()
		c, _ := exec.Command("gh", "api", fmt.Sprintf("/repos/%s/issues/%s/comments?per_page=100", repo, pr)).Output()
		return fmt.Sprintf("%s", jsonStr(map[string]any{"current_body": json.RawMessage(b), "comments": json.RawMessage(c)}))
	case "get_pr_meta":
		out, err := exec.Command("gh", "api", fmt.Sprintf("/repos/%s/pulls/%s", repo, pr)).Output()
		if err != nil {
			return jsonStr(map[string]string{"error": "gh meta failed"})
		}
		return string(out)
	}
	return "{}"
}

func readFileTool(arguments string) string {
	var in map[string]any
	_ = json.Unmarshal([]byte(arguments), &in)
	path, _ := in["path"].(string)
	if path == "" || strings.Contains(path, "..") {
		return jsonStr(map[string]string{"error": "path required and must not traverse"})
	}
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return jsonStr(map[string]string{"error": "read failed"})
	}
	return string(b)
}
