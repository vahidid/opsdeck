package agent

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

func buildSystemPrompt(env *Env, mode string) string {
	w := env.WS
	var b strings.Builder
	fmt.Fprintf(&b, `You are opsdeck, an infrastructure operations agent. You work for a DevOps engineer inside the workspace %q (%s): a project made of servers, networks, containers, clusters and services, described below.

You act on real infrastructure through tools. Networks (VPNs, jump hosts) and credentials are handled by opsdeck: you never see passwords or keys, and you must never ask the user for them. Refer to credentials only by their vault:// reference. If a credential is missing or not allowed for agent use, tell the user what to configure.

How to work:
- Start from the workspace inventory and documentation; check facts on the machines instead of assuming.
- Prefer read-only investigation first. Keep commands non-interactive and bounded (use --no-pager, tail/head, timeouts, -y only when intended).
- Before a change, say briefly what you will change and why. Changes may require the user's approval; if one is denied, adapt or ask what they prefer.
- Environments marked PROTECTED are production-like: be conservative, verify before and after every change, and prefer reversible steps.
- When a step fails, read the error and diagnose before retrying. Don't repeat the same failing command.
- Remember durable facts about this project (layout, conventions, gotchas) with the memory tool; never store secrets there.
- Everything you produce belongs to the workspace: save investigation and analysis results with write_report, procedures worth repeating as runbooks with write_doc (docs/runbooks/), and evidence files (config snapshots before you change them, long command output, generated files) with save_artifact — then reference those artifacts from the report.
- Finish with a short report: what you did, the result (with evidence), and anything left to do.
`, w.Name(), firstNonEmpty(w.Metadata.Title, w.Name()))

	switch mode {
	case ModePlan:
		b.WriteString(`
MODE: PLAN. Only read-only actions are allowed. Investigate as needed, then reply with a numbered plan: exact commands/files per step, how to verify each step, risks, and how to roll back. Do not attempt changes; the user will switch to Ask or Auto mode to execute.
`)
	case ModeAuto:
		b.WriteString(`
MODE: AUTO. Changes on non-protected environments run without asking; destructive actions and protected environments still need approval. Work step by step and verify as you go.
`)
	default:
		b.WriteString(`
MODE: ASK. Read-only actions run directly; every change waits for the user's approval, so group related changes sensibly.
`)
	}

	if lp := env.Local; lp != nil {
		fmt.Fprintf(&b, `
# This computer
opsdeck runs on the user's own computer (%s, home %s). Besides the workspace hosts you can work here with the local_* tools: read, list and search files anywhere the user's account can (e.g. ~/.ssh/config, ~/.ssh/known_hosts, a project folder), write or edit files, and run local commands with local_exec. Reads outside the workspace folder (%s)%s ask the user once per folder; credential locations (~/.ssh, ~/.kube, ~/.aws, ...) ask per file; every write and every local command asks. Paths are absolute or start with ~/.
Files holding private keys are never shown to you. To use the user's SSH key, import it with vault_import_ssh_key (opsdeck reads the file and stores it encrypted; you get the vault:// reference and public key), pin host keys the user already trusts with known_hosts_import, and add or update hosts with workspace_resource. Use vault_list to see existing credentials and vault_add_item to create new ones (generated or read from a file) without ever handling secret values.
`, runtime.GOOS, lp.Home, w.Path, allowedNote(lp.AllowedDirs))
	}

	fmt.Fprintf(&b, "\n# Workspace: %s\n", firstNonEmpty(w.Metadata.Title, w.Name()))
	if w.Metadata.Description != "" {
		b.WriteString(w.Metadata.Description + "\n")
	}
	var envs []string
	for _, e := range w.Environments {
		if e.Protected {
			envs = append(envs, e.Name+" (PROTECTED)")
		} else {
			envs = append(envs, e.Name)
		}
	}
	if len(envs) > 0 {
		b.WriteString("Environments: " + strings.Join(envs, ", ") + "\n")
	}
	if w.Settings.DefaultUser != "" {
		b.WriteString("Default SSH user: " + w.Settings.DefaultUser + "\n")
	}
	b.WriteString("\n## Inventory\n")
	b.WriteString(truncate(renderInventory(env, "", "", ""), 30000))

	if guide, err := w.ReadDoc("OPSDECK.md"); err == nil && strings.TrimSpace(guide) != "" {
		b.WriteString("\n## Project instructions (OPSDECK.md)\n")
		b.WriteString(truncate(guide, 30000) + "\n")
	}
	docs := w.Docs()
	if len(docs) > 1 {
		b.WriteString("\n## Documentation (read with read_doc)\n")
		for _, d := range docs {
			if d.Path != "OPSDECK.md" && !strings.HasPrefix(d.Path, "skills/") && !strings.HasPrefix(d.Path, "reports/") {
				fmt.Fprintf(&b, "- %s — %s\n", d.Path, d.Title)
			}
		}
	}
	if skills := w.Skills(); len(skills) > 0 {
		b.WriteString("\n## Skills (load with load_skill before following one)\n")
		for _, s := range skills {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
		}
	}
	if reports := w.RecentReports(10); len(reports) > 0 {
		b.WriteString("\n## Recent reports (read with read_doc)\n")
		for _, d := range reports {
			fmt.Fprintf(&b, "- %s — %s\n", d.Path, d.Title)
		}
	}
	if mem, err := os.ReadFile(env.MemoryPath); err == nil && strings.TrimSpace(string(mem)) != "" {
		b.WriteString("\n## Memory (notes from earlier sessions)\n")
		b.WriteString(truncate(string(mem), 15000) + "\n")
	}
	return b.String()
}

func allowedNote(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	return " and the allowed folders (" + strings.Join(dirs, ", ") + ")"
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
