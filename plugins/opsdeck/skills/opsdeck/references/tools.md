# opsdeck tools

<!-- Generated from the tool definitions by internal/agent/toolsdoc.go.
     Do not edit by hand: OPSDECK_UPDATE_DOCS=1 go test ./internal/agent -run TestToolReference -->

The tools an agent connected to opsdeck gets, for one workspace. A workspace only offers
the tools of what it has (no `database` without a Database resource, no `local_*`
tools unless the user lets agents use this computer, no web tools when web access is off),
plus the tools of the MCP servers it declares (`mcp__<server>__<tool>`).

Whether a call runs at once or waits for the user depends on what it does (read, change,
destructive), the environment (protected ones always ask), the workspace's permission rules
and the access key's mode.

| Tool | What it does |
|---|---|
| `ansible` | The workspace's Ansible projects (AnsibleProject: a folder of the workspace, a folder on this computer, or a folder in one of its Git repositories). |
| `ansible_run` | Run Ansible from a workspace project on this computer, with its output streamed to the user's Ansible page: kind=playbook runs ansible-playbook (check=true for a dry run that reports what would … |
| `ansible_vault` | Ansible Vault content of a project. |
| `certificate` | TLS certificates of the workspace (Certificate resources: domains, an ACME CA such as Let's Encrypt, key type). |
| `database` | Work with a workspace database (Database resources: PostgreSQL, MySQL, MariaDB, MongoDB, Redis). |
| `deploy` | The workspace's deployment platforms (DeployPlatform resources: Portainer stacks, Argo CD applications, Komodo stacks and deployments), through the platform's API with its vault credential — you … |
| `dns` | The DNS zones of the workspace's domains (Domain resources, managed at Cloudflare or ArvanCloud through a connected account — you never see its token). |
| `docker` | Manage Docker on a host over SSH (sudo -n is used when the SSH user is not in the docker group). |
| `edit_file` | Replace one exact occurrence of old_string with new_string in a text file on a host (backup kept). |
| `exec` | Run a non-interactive shell command on one or more workspace hosts over SSH (VPN/jump hosts are handled automatically). |
| `git_sync` | Clone a workspace Git repository (a GitRemote resource) onto a host, or update it if already cloned (fetch + fast-forward pull). |
| `http_request` | Call the HTTP API of a workspace web panel or service (Panel, Prometheus, Loki, Elasticsearch resources: Grafana, Alertmanager, Consul, Harbor, Proxmox, Jenkins, Traefik, an app's admin API…) … |
| `inventory` | List the workspace's resources (hosts, groups, networks, panels, clusters, databases, git remotes) with addresses, environments, roles and how each is reached. |
| `knowledge` | Search everything this workspace knows — skills (its own and the built-in library), documentation and runbooks, repository docs, reports of earlier work, memory notes and saved scripts — in one … |
| `known_hosts_import` | Pin SSH host keys for workspace hosts from an OpenSSH known_hosts file on THIS computer (default ~/.ssh/known_hosts; hashed entries are supported). |
| `kubernetes` | Work with a workspace Kubernetes cluster (any resource type, custom resources included; type is a plural, kind, short name or resource.group, e.g. pods, deploy, ingresses, … |
| `list_dir` | List a directory on a host. |
| `load_skill` | Load a skill (a packaged procedure listed in the system prompt: the workspace's own or opsdeck's built-in library) before following it. |
| `local_edit_file` | Replace exact text in a file on THIS computer. |
| `local_exec` | Run a non-interactive shell command on THIS computer (sh -c; cmd /C on Windows), e.g. ssh-keygen -lf, ssh -G <host>, git status in a local project, docker compose config, kubectl config get-contexts. |
| `local_list_dir` | List a folder on THIS computer (type, size, modification time, name). |
| `local_read_file` | Read a text file on THIS computer (the user's machine where opsdeck runs), e.g. ~/.ssh/config, ~/.ssh/known_hosts, a project's docker-compose.yml or a kubeconfig. |
| `local_search` | Search text files under a folder on THIS computer for a regular expression (like grep -rn). |
| `local_write_file` | Create or overwrite a text file on THIS computer (e.g. a generated docker-compose.yml, an ~/.ssh/config entry, a script in the user's project). |
| `logs_query` | Search the logs of a workspace Loki (LogQL, e.g. {app="shop"} \|= "error") or Elasticsearch/OpenSearch (Lucene query string, e.g. service.name:shop AND level:error) through its VPN or jump host. |
| `memory` | Workspace memory: durable notes you keep across sessions (facts learned about this project's infrastructure, conventions, gotchas). |
| `merge_request` | Merge requests (GitLab) / pull requests (GitHub) of a workspace repository, through its hosting API with the repository's token. |
| `metrics_query` | Query a workspace Prometheus (or a compatible API: VictoriaMetrics, Thanos, Mimir) through its VPN or jump host. |
| `net_check` | Network diagnostics. type=tcp: can <target host:port> be reached; type=http: GET a URL (status, latency, TLS expiry); type=dns: resolve a name; type=ping: ICMP ping (only from a host). |
| `read_doc` | Read workspace documentation. |
| `read_file` | Read a text file on a host (via SFTP). |
| `read_output` | Read more of a long output: the full output of an earlier tool call that was cut (its id, o-…, is named at the end of that result) or a workspace artifact (artifacts/…, e.g. one saved with … |
| `repo` | The workspace's Git repositories (GitLab, GitHub; e.g. deployment manifests, compose files, Helm values, Terraform). |
| `repo_files` | Read a Git repository's working copy (or any branch/commit with ref). |
| `repo_write` | Change files in a Git repository's working copy (nothing leaves this computer until you commit and push with the repo tool). |
| `run_script` | Run a saved script (see the Scripts list in the system prompt, or script action=list) on one or more workspace hosts as one unit: the exact approved content, uploaded to a private temporary folder … |
| `save_artifact` | Keep a file as evidence in the workspace (artifacts/<date>/...): source=file copies a text file from a host (e.g. a config snapshot before changing it); source=command saves the FULL output of a … |
| `script` | Saved scripts: reusable shell scripts the user approved (server bootstrap, hardening, agent installs…) that run_script runs on hosts as one unit. |
| `software_lookup` | Look up facts about software versions from public registries: action=eol — support and end-of-life dates of a product's release cycles (endoflife.date: ubuntu, debian, rhel, nginx, postgresql, … |
| `tasks` | The session's background tasks (started with exec or local_exec background=true): list them, read a task's output (the latest lines, or grep), wait for one to finish, or stop one. |
| `topology` | The workspace's infrastructure topology (topology.yaml): zones (data centers, VPCs, subnets, DMZ, offices), components (routers of every kind, switches, firewalls, CDN, WAF, DDoS protection, load … |
| `vault_add_item` | Create a vault item without exposing secrets to you. |
| `vault_import_ssh_key` | Import an SSH private key file from THIS computer (e.g. ~/.ssh/id_ed25519) into the vault. |
| `vault_list` | List the vault items this workspace can use (its own scope and the global scope): reference, type, whether the agent may use it, and non-secret fields such as usernames, URLs and SSH public keys. |
| `vpn` | The workspace's VPNs and proxies (WireGuard, Tailscale/Headscale, NetBird, OpenVPN, OpenConnect for Cisco AnyConnect/ocserv/FortiGate/GlobalProtect/Pulse/F5, openfortivpn, SOCKS). |
| `wait_for` | Wait until something is true instead of sleeping and retrying by hand: condition=tcp (a port accepts connections), http (a URL answers with an expected status and, optionally, a body matching a … |
| `web_fetch` | Read a public web page as Markdown (HTML is converted; JSON and text are returned as they are). |
| `web_search` | Search the public web (vendor documentation, release notes, error messages, CVEs, known issues). |
| `workspace_resource` | Read or change the workspace inventory. |
| `write_doc` | Create or update a workspace documentation file (docs/**.md, skills/*.md or OPSDECK.md), e.g. to record a runbook or a topology note after finishing a task. |
| `write_file` | Write a whole text file on a host (atomic replace; the previous version is backed up next to it as <file>.opsdeck-bak-<time>). |
| `write_report` | Save the result of an investigation, analysis, audit, incident or plan as a new Markdown report in the workspace (reports/<year>/<month>/<date>-<title>.md). |
