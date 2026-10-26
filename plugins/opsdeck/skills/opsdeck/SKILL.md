---
name: opsdeck
description: Work on real infrastructure through opsdeck's MCP tools (servers over SSH, VPNs, Docker, Kubernetes, databases, logs, metrics, DNS, certificates, deployments). Use when the user asks about their servers, hosts, production or staging, an outage or slowness, containers or pods, a deploy, VPN access, logs, DNS records or TLS certificates, and the opsdeck MCP server is connected (its tools have opsdeck in their names).
---

# Working with opsdeck

opsdeck is the user's infrastructure workbench. Its MCP server gives you the tools of one
**workspace** (one project: its hosts, networks, clusters, databases, panels, docs).
opsdeck holds every credential and uses it for you: you never see, need or ask for a
password, key or token. Every call is checked against the workspace's permission rules,
secrets are redacted from what you get back, and everything lands in the audit log.

## Rules

1. **Look before you touch.** Start with `inventory` (what exists, how it is reached,
   which environment) and the workspace's docs (`read_doc`, `knowledge`). Read the current
   state (status, logs, config) before you propose a change.
2. **Changes may wait for the user.** Depending on the access key's mode, a change waits
   until the user approves it in the opsdeck app; on a **protected** environment (usually
   `prod`) it always does. The call simply takes longer. Tell the user what you are about
   to change and why *before* you call, so they can approve with confidence.
3. **A denied call is an answer.** Its result says why. Don't retry it unchanged; adjust
   the plan or ask the user.
4. **In read-only mode** (plan) every change is refused: investigate and propose.
5. **Never put secrets into arguments or files.** Refer to vault items
   (`vault://<scope>/<item>`, see `vault_list`); to store a new secret use `vault_add_item`,
   which lets the user type it into opsdeck, not into the chat.
6. **Prefer the specific tool** (`docker`, `kubernetes`, `database`, `logs_query`,
   `metrics_query`, `vpn`, `dns`) over raw `exec`: they are safer, their risk is classified
   precisely, and the user sees better approval cards.
7. **Leave a trace.** After an investigation or a change, save the findings with
   `write_report`, and durable facts about the project with `memory`.

If a call fails with *opsdeck is locked*, ask the user to unlock the opsdeck app. If the
MCP server can't be reached, opsdeck isn't running: ask the user to start it.

## Tools by task

| Task | Tools |
|---|---|
| What is there | `inventory`, `workspace_resource` (get / add / change a resource), `topology`, `read_doc`, `knowledge`, `load_skill` |
| A host | `exec` (shell over SSH, through VPNs and jump hosts automatically), `read_file`, `list_dir`, `write_file`, `edit_file`, `run_script`, `script` |
| Containers | `docker` (containers, compose, logs, stats, restart, pull) |
| Kubernetes | `kubernetes` (get, describe, logs, events, apply, rollout, scale, helm) |
| Databases | `database` (queries; writes ask) |
| Logs and metrics | `logs_query` (Loki, Elasticsearch), `metrics_query` (Prometheus), `http_request` (a panel's API) |
| Network | `net_check` (tcp, http, dns, tls), `vpn` (status, connect, disconnect), `wait_for` |
| DNS and TLS | `dns`, `certificate` |
| Deploy | `deploy` (Portainer, Argo CD, Komodo), `repo`, `repo_files`, `repo_write`, `merge_request`, `git_sync`, `ansible`, `ansible_run` |
| Long commands | `exec` with `background=true`, then `tasks` and `read_output` |
| Keep results | `write_report`, `save_artifact`, `memory`, `write_doc` |

## Typical flows

**"The site returns 502"** — `inventory` → find the entry point and the app behind it →
`docker` (or `kubernetes`) status and logs → `logs_query` around the time it started →
form a hypothesis and confirm it (config, DNS, a dependency with `net_check`) → explain the
cause and propose the fix → apply it (it waits for approval) → verify (`net_check`, logs)
→ `write_report`.

**Health check** — for each host of the environment: disk, memory, load, failed units
(`exec` with read-only commands), containers that restart, certificates that expire soon
(`certificate`), then one short summary with what needs attention first.

**Deploy a new version** — read how this workspace deploys (`read_doc`, `knowledge`,
`deploy` or `repo`), show the diff of what will change, apply it on staging first when
there is one, verify, then production.

See [references/tools.md](references/tools.md) for every tool and its risk.
