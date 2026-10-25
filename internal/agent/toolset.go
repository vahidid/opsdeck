package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// toolNeeds lists the resource kinds a tool works on: it is offered only
// when the workspace has one of them, so the model is not shown (and does
// not pay tokens for) a database tool in a workspace without databases.
var toolNeeds = map[string][]string{
	"exec": {"Host"}, "read_file": {"Host"}, "list_dir": {"Host"}, "write_file": {"Host"}, "edit_file": {"Host"},
	"docker": {"Host"}, "run_script": {"Host"},
	"database":   {"Database"},
	"kubernetes": {"KubernetesCluster"},
	"repo":       {"GitRemote"}, "repo_files": {"GitRemote"}, "repo_write": {"GitRemote"}, "merge_request": {"GitRemote"}, "git_sync": {"GitRemote"},
	"ansible": {"AnsibleProject"}, "ansible_run": {"AnsibleProject"}, "ansible_vault": {"AnsibleProject"},
	"vpn":           workspace.NetworkKinds,
	"http_request":  {"Panel", "Prometheus", "Loki", "Elasticsearch"},
	"metrics_query": {"Prometheus"},
	"logs_query":    {"Loki", "Elasticsearch"},
	"dns":           {"Domain"},
	"deploy":        {"DeployPlatform"},
	"certificate":   {"Certificate"},
}

// webTools need web access (App settings → Web access, per workspace).
var webTools = map[string]string{"web_search": WebSearch, "web_fetch": WebFetch, "software_lookup": WebFetch}

// hasKinds reports which kinds the workspace has.
func hasKinds(w *workspace.Workspace) map[string]bool {
	has := map[string]bool{}
	for _, r := range w.Resources {
		has[r.Kind] = true
	}
	return has
}

// availableTools is the built-in tool set for what the workspace has.
func availableTools(w *workspace.Workspace, web string, subagents bool) []Tool {
	has := hasKinds(w)
	var out []Tool
	for _, t := range Tools() {
		name := t.Def.Name
		if kinds, ok := toolNeeds[name]; ok {
			found := false
			for _, k := range kinds {
				found = found || has[k]
			}
			if !found {
				continue
			}
		}
		if need, ok := webTools[name]; ok && !webAllows(web, need) {
			continue
		}
		if name == "delegate" && !subagents {
			continue
		}
		out = append(out, t)
	}
	return out
}

func toolReadOutput() Tool {
	return Tool{
		Def: llm.Tool{Name: "read_output", Description: "Read more of a long output: the full output of an earlier tool call that was cut (its id, o-…, is named at the end of that result) or a workspace artifact (artifacts/…, e.g. one saved with save_artifact source=command). Read by line range (offset, limit) or find lines with grep (a regular expression; context lines around each match).",
			Schema: obj(map[string]any{
				"id":      str("Output id (o-1234abcd) or artifact path (artifacts/…)"),
				"offset":  num("First line to return, 1-based (default 1)"),
				"limit":   num("Number of lines (default 200, max 2000)"),
				"grep":    str("Optional regular expression: return only matching lines (case-insensitive) with their line numbers"),
				"context": num("grep: lines of context around each match (default 0, max 10)"),
			}, "id")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				ID                     string
				Offset, Limit, Context int
				Grep                   string
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			in.ID = strings.TrimSpace(in.ID)
			var re *regexp.Regexp
			if in.Grep != "" {
				var err error
				if re, err = regexp.Compile("(?i)" + in.Grep); err != nil {
					return nil, fmt.Errorf("invalid grep pattern: %v", err)
				}
			}
			return &Call{Risk: RiskRead, Target: "session", Match: in.ID, Run: func(ctx context.Context) (string, error) {
				var text string
				switch {
				case strings.HasPrefix(in.ID, "artifacts/"):
					s, _, err := env.WS.ReadArtifact(in.ID, workspace.MaxArtifactSize)
					if err != nil {
						return "", err
					}
					text = s
				case env.Outputs != nil:
					s, err := env.Outputs(in.ID)
					if err != nil {
						return "", err
					}
					text = s
				default:
					return "", errors.New("no saved outputs in this session")
				}
				return sliceLines(text, in.Offset, in.Limit, re, in.Context), nil
			}}, nil
		},
	}
}

// sliceLines returns numbered lines of text: a range, or the lines matching
// re with ctx lines around them.
func sliceLines(text string, offset, limit int, re *regexp.Regexp, ctxLines int) string {
	lines := strings.Split(text, "\n")
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 2000)
	var b strings.Builder
	if re != nil {
		ctxLines = max(0, min(ctxLines, 10))
		shown, matches, last := 0, 0, -1
		for i, l := range lines {
			if !re.MatchString(l) {
				continue
			}
			matches++
			from, to := max(0, i-ctxLines), min(len(lines)-1, i+ctxLines)
			if last >= 0 && from > last+1 {
				b.WriteString("--\n")
			}
			for j := max(from, last+1); j <= to; j++ {
				fmt.Fprintf(&b, "%d: %s\n", j+1, lines[j])
				shown++
			}
			last = to
			if shown >= limit {
				fmt.Fprintf(&b, "... (stopped after %d lines; narrow the pattern)\n", shown)
				break
			}
		}
		if matches == 0 {
			return fmt.Sprintf("no line matches (%d lines searched)", len(lines))
		}
		return fmt.Sprintf("%d matching lines of %d:\n%s", matches, len(lines), b.String())
	}
	if offset <= 0 {
		offset = 1
	}
	if offset > len(lines) {
		return fmt.Sprintf("offset %d is past the end (%d lines)", offset, len(lines))
	}
	end := min(len(lines), offset-1+limit)
	for i := offset - 1; i < end; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "... (%d more lines; continue with offset=%d)\n", len(lines)-end, end+1)
	}
	return truncate(b.String(), toolOutputToModel-200)
}
