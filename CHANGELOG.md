# Changelog

All notable changes to opsdeck. Versions follow [semantic versioning](https://semver.org);
each release's section becomes its notes on GitHub.

## [Unreleased]

### Agent

- **Fewer approvals for reads on servers.** The command classifier knows many more read-only
  commands and forms (shell builtins such as `set -e` and `read`, `awk` conditions, `curl -o
  /dev/null`, `dpkg -l`, storage, firewall and certificate status tools, cloud CLIs' `list` and
  `describe`), classifies `docker exec`, `docker compose exec`, `kubectl exec` and `find -exec`
  by the command they run, and evaluates loop variables with their literal values. Unknown
  values are tried with hostile ones, so a read stays a read only if it is one whatever they
  are. In a set of 80 everyday read commands, 47 used to ask for approval; none do now.
- **Permission rules match each command of a script.** An `allow` rule for `exec` covers a
  script only when every command in it that is not a read matches, so `systemctl status*` no
  longer allows `systemctl status x; rm -rf /`. `ask` and `deny` rules apply when any command
  matches.

- **Model lists from the CLI** — Settings asks each local agent which models your login can
  use (Claude Code, Codex, Gemini CLI, opencode and other ACP agents) and shows them with
  their names and descriptions to tick; API providers list their models from the API
  (Anthropic, OpenAI, OpenRouter, Ollama, LM Studio…). The model picker shows these names,
  grouped by provider.
- ACP agents (opencode, Qwen Code, Copilot CLI…) now run with the chosen model; before,
  only Gemini CLI honoured it.

## [0.1.0] — 2026-10-23

The first release: a local workbench and AI agent harness for infrastructure, as a
desktop app for Windows, macOS and Linux and as a CLI that serves the same UI to your
browser.

### Workbench

- **Workspaces** per project — hosts, groups, VPN profiles, jump hosts, web panels, Git
  repositories, databases, Kubernetes clusters, docs and runbooks as plain YAML and
  Markdown, with no secrets in them.
- **Vault** — local and encrypted (Argon2id → XChaCha20-Poly1305): SSH keys, logins with
  TOTP, API tokens, kubeconfigs, VPN configs, certificates.
- **VPNs** — WireGuard, Tailscale/Headscale, NetBird, OpenVPN, AnyConnect/ocserv,
  FortiGate, GlobalProtect and more, in userspace or system mode, with single sign-on.
- **SSH** terminals, remote files, host stats, live logs and port forwards — through VPNs
  and jump hosts automatically.
- **Docker** (containers, Compose, Swarm, volumes), **Kubernetes** (every resource type,
  logs, exec, apply with diff, rollouts, Helm) and **Ansible** (editor with visual views,
  Ansible Vault, runs with live logs).
- **Git repositories** with GitHub/GitLab connectors and merge requests, a **topology**
  map, a **script** library and 20 built-in **skills**.

### Agent

- Works with Anthropic, OpenAI-compatible APIs, OpenRouter, Ollama and LM Studio, or a
  local agent CLI you are signed in to (Claude Code, Codex, Gemini CLI, ACP agents).
- Every tool call classified and approved by mode (Plan / Ask / Auto / Full), secrets
  redacted from everything the model sees, a hash-chained audit journal.

### App

- Desktop app with installers for each system; the sidebar groups sections and opens
  each in its own tab.
- Updates from inside the app: it checks GitHub releases, verifies the download against
  the release checksums, installs it and restarts.
