package agent

import (
	"fmt"
	"sort"
	"strings"
)

// ToolReference renders the tools a connected agent gets as the skill pack's
// references/tools.md (generated: see TestToolReferenceIsCurrent).
func ToolReference() string {
	var b strings.Builder
	b.WriteString(`# opsdeck tools

<!-- Generated from the tool definitions by internal/agent/toolsdoc.go.
     Do not edit by hand: OPSDECK_UPDATE_DOCS=1 go test ./internal/agent -run TestToolReference -->

The tools an agent connected to opsdeck gets, for one workspace. A workspace only offers
the tools of what it has (no ` + "`database`" + ` without a Database resource, no ` + "`local_*`" + `
tools unless the user lets agents use this computer, no web tools when web access is off),
plus the tools of the MCP servers it declares (` + "`mcp__<server>__<tool>`" + `).

Whether a call runs at once or waits for the user depends on what it does (read, change,
destructive), the environment (protected ones always ask), the workspace's permission rules
and the access key's mode.

| Tool | What it does |
|---|---|
`)
	all := append(Tools(), LocalTools()...)
	sort.Slice(all, func(i, j int) bool { return all[i].Def.Name < all[j].Def.Name })
	seen := map[string]bool{}
	for _, t := range all {
		n := t.Def.Name
		if clientHidden[n] || seen[n] {
			continue
		}
		seen[n] = true
		fmt.Fprintf(&b, "| `%s` | %s |\n", n, strings.NewReplacer("|", "\\|", "\n", " ").Replace(summary(t.Def.Description)))
	}
	return b.String()
}

// summary is a description's first sentence, at most about 200 characters.
func summary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for i := 20; i+1 < len(s); i++ {
		if s[i] == '.' && s[i+1] == ' ' && !strings.HasSuffix(s[:i], "e.g") && !strings.HasSuffix(s[:i], "i.e") {
			s = s[:i+1]
			break
		}
	}
	if len(s) > 200 {
		cut := strings.LastIndex(s[:200], " ")
		s = s[:cut] + " …"
	}
	return s
}
