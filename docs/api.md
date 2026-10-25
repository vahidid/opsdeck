# Local API (v1)

The server (`opsdeck serve`) listens on `127.0.0.1:7788` by default and serves the web UI
and this JSON API under `/api`. All bodies are JSON. Errors: HTTP 4xx/5xx with
`{"error": "message", "code": "optional_code"}`.

Error codes the UI must handle everywhere:
- `401 unauthorized` → show "open the link printed by `opsdeck serve`" screen.
- `423 vault_locked` → show unlock screen (and `vault_uninitialized` → setup screen).

## Auth

`opsdeck serve` prints `http://127.0.0.1:7788/#token=<token>`. The UI reads the token from
the URL hash, calls `POST /api/auth/login {token}` (sets an HttpOnly cookie), then removes
the hash. `GET /api/auth/check` → `{ok:true}` or 401. Server also checks `Host`/`Origin`.

## Vault

| Method | Path | Body → Response |
|---|---|---|
| GET | `/api/vault/status` | → `{initialized, unlocked, autoLockMinutes}` |
| POST | `/api/vault/init` | `{password}` → status |
| POST | `/api/vault/unlock` | `{password}` → status |
| POST | `/api/vault/lock` | → status |
| POST | `/api/vault/password` | `{oldPassword, newPassword}` |
| GET | `/api/vault/types` | → `[ItemType]` |
| GET | `/api/vault/items?scope=global\|<ws>` | → `[Item]` (secret values never included) |
| POST | `/api/vault/items` | `ItemInput` → `Item` |
| PUT | `/api/vault/items/{id}` | `ItemInput` (a secret field whose `value` is omitted/null keeps old value) → `Item` |
| DELETE | `/api/vault/items/{id}` | |
| POST | `/api/vault/items/{id}/reveal` | `{field}` → `{value}` (audited) |
| GET | `/api/vault/items/{id}/totp` | → `{code, period, remaining}` |
| POST | `/api/vault/ssh/generate` | `{scope, name, algorithm: ed25519\|rsa\|ecdsa, comment, tags}` → `Item` |
| POST | `/api/vault/ssh/import` | `{scope, name, privateKey, passphrase, tags}` → `Item` |
| POST | `/api/vault/password-generator` | `{length, symbols}` → `{password}` |

```ts
type ItemType = { type: string; label: string; fields: {name: string; label: string; secret: boolean; multiline: boolean; required: boolean}[] }
type Field = { name: string; secret: boolean; value?: string; hasValue: boolean }   // value only for non-secret fields
type Item = { id: string; scope: string; type: string; name: string; tags: string[]; notes: string;
              fields: Field[]; policy: {agentUse: boolean; confirmOnUse: boolean};
              ref: string;               // "vault://<scope>/<name>"
              publicKey?: string; fingerprint?: string;   // ssh-key only
              created: string; updated: string; revision: number }
type ItemInput = { scope: string; type: string; name: string; tags?: string[]; notes?: string;
                   fields: {name: string; value?: string|null; secret: boolean}[]; policy?: {...} }
```

Item types: `ssh-key`, `login`, `token`, `kubeconfig`, `vpn`, `certificate`, `file`, `note`.

## Settings & models

| GET/PUT | `/api/settings` | `Settings` |
| GET | `/api/models` | → `[{id: "providerId/model", provider, providerName, model, name, label}]` |
| GET | `/api/agents/detect` | → `[{kind, name, command, args, path, version, login, models}]` agent CLIs found on PATH (`models`: built-in list) |
| POST | `/api/providers/models` | provider config (saved or not) → `{models: [{id, name, description?}], source: "cli"\|"api"\|"builtin", error?}`: the models an agent CLI offers with your login, or an API offers with the key; `builtin` + `error` when it could not be asked |

```ts
type Settings = { autoLockMinutes: number; defaultModel: string;
  providers: { id: string; type: "anthropic"|"openai"; name: string; baseUrl: string;
               apiKeyRef: string;   // vault ref, e.g. vault://global/anthropic-key ; empty for Ollama
               models: string[];
               modelNames?: Record<string, string> }[];   // display names, as listed by the CLI/API
  vpn: { elevate: "auto"|"sudo"|"pkexec"|"none";   // how system-mode VPN clients get root
         browser: string;                           // Chrome/Edge/Chromium for SSO login windows ("" = detect)
         codes: { ntfyUrl: string;                  // ntfy topic the phone forwards one-time codes to
                  ntfyTokenRef: string;             // vault ref of its access token (protected topics)
                  command: string;                  // a local command that prints the code
                  pattern: string } };              // regexp for the code in a message ("" = 4-8 digits)
  localAccess: { disabled: boolean;        // agent access to this computer (default on)
                 allowedDirs: string[];    // readable without asking; "~/..." is expanded
                 autoReadCommands: boolean };  // run read-only local_exec commands without asking
  agent: { autoServerChanges: boolean } }   // false (default): changes on servers ask in every mode
```
Local agent providers use `type: "claude-code" | "codex" | "gemini-cli" | "acp"` with
`command`, `args`, `env` (no base URL or key). Their sessions reach opsdeck's tools through
the MCP bridge at `/mcp/` (streamable HTTP, `Authorization: Bearer <per-turn token>`).

`type: "openai"` = any OpenAI-compatible API (OpenAI, OpenRouter, Ollama `http://localhost:11434/v1`, LM Studio, Gemini compat, DeepSeek, Groq...).

## Workspaces

| Method | Path | |
|---|---|---|
| GET | `/api/workspaces` | → `[WorkspaceSummary]` |
| POST | `/api/workspaces` | `{name, title, description, color}` |
| POST | `/api/workspaces/import` | `{path}` (register an existing folder) |
| POST | `/api/workspaces/{ws}/export` | `{password}` → export file (JSON, encrypted; see workspace.md) |
| POST | `/api/workspaces/import-file` | `{bundle, password}` (`bundle` is the export file) → `{workspace, items, skipped}` |
| GET | `/api/workspaces/{ws}` | → `Workspace` |
| PUT | `/api/workspaces/{ws}` | `Workspace` (name immutable) |
| DELETE | `/api/workspaces/{ws}?purgeVault=1` | |
| GET | `/api/kinds` | → `[KindSchema]` |
| GET | `/api/workspaces/{ws}/resources` | → `[Resource]` |
| POST | `/api/workspaces/{ws}/resources` | `Resource` |
| PUT | `/api/workspaces/{ws}/resources/{kind}/{name}` | `Resource` (may rename) |
| DELETE | `/api/workspaces/{ws}/resources/{kind}/{name}` | |
| GET | `/api/workspaces/{ws}/validate` | → `[{level: "error"\|"warning", resource, message}]` |
| GET | `/api/workspaces/{ws}/topology` | → `{nodes: [{id, kind, name, environment, label}], edges: [{from, to}]}` (edge = "from is reached via to") |
| GET | `/api/workspaces/{ws}/resolve/{kind}/{name}` | → `{path: [{kind, name, label}]}` (outermost hop first) |
| GET | `/api/workspaces/{ws}/docs` | → `[{path, title, size, updated}]` — `OPSDECK.md`, `docs/**.md`, `reports/**.md`, `skills/*.md` |
| GET | `/api/workspaces/{ws}/docs/file?path=` | → `{path, content}` |
| PUT | `/api/workspaces/{ws}/docs/file` | `{path, content}` |
| DELETE | `/api/workspaces/{ws}/docs/file?path=` | |
| GET/PUT | `/api/workspaces/{ws}/memory` | `{content}` (agent memory, markdown) |
| GET | `/api/workspaces/{ws}/artifacts` | → `[{path, size, updated}]` newest first (`artifacts/INDEX.md` first) |
| GET | `/api/workspaces/{ws}/artifacts/file?path=` | → `{path, content, truncated}` |
| DELETE | `/api/workspaces/{ws}/artifacts/file?path=` | |
| GET | `/api/workspaces/{ws}/skills` | → `[{name, description, path}]` |
| GET | `/api/workspaces/{ws}/mcp` | → `[{name, status, error, tools: [{name, description, readOnly}]}]` |

```ts
type WorkspaceSummary = { name; title; description; color; tags: string[]; path; resourceCount: number; hostCount: number }
type Workspace = WorkspaceSummary & {
  environments: { name: string; protected: boolean }[];
  settings: { defaultUser: string; defaultAuth: string; defaultModel: string };
  permissions: { rules: { effect: "allow"|"ask"|"deny"; risk?: "read"|"change"|"destructive"; tool?: string; environment?: string[]; match?: string }[] };
  mcpServers: { name: string; command: string; args: string[]; env: Record<string,string> }[];   // env values may be vault refs
}
type Resource = { kind: string; name: string; environment?: string; tags?: string[]; via?: string;   // "host/bastion" | "network/office-wg"
                  description?: string; docs?: string; spec: Record<string, any> }
type KindSchema = { kind: string; label: string; category: "compute"|"network"|"service"|"cluster"; icon: string;
                    canVia: boolean; fields: { key: string; label: string; type: "string"|"number"|"text"|"secretRef"|"list"|"select"|"resourceRef";
                    required?: boolean; placeholder?: string; help?: string; options?: string[]; refKinds?: string[]; itemTypes?: string[] }[] }
```
Kinds: `Host`, `HostGroup`, network kinds `WireGuard`, `Tailscale`, `NetBird`, `OpenVPN`, `OpenConnect`, `OpenFortiVPN`, `SocksProxy` (see [vpn.md](vpn.md)), `Panel`, `GitRemote`, `Database`, `KubernetesCluster`. Field types: string, number, bool, text, secretRef, list, select, resourceRef (`default` gives the implicit value; `showIf` names a bool field that must be on for the field to show).

## Networks

| GET | `/api/workspaces/{ws}/networks` | → `[{name, kind, mode: "userspace"\|"system"\|"", status: "down"\|"connecting"\|"auth"\|"up"\|"error", error, authUrl, authWindow, code, since, address, interface, server, detail, proxy, lastHandshake, rxBytes, txBytes, missing, usedBy}]`; `code` (status `auth`) = waiting for a one-time code: `{message, retry, since, expires, sources}` |
| POST | `/api/workspaces/{ws}/networks/{name}/up` / `/down` | `up` waits up to ~20 s; slower connects continue in the background (`connecting` / `auth`) |
| POST | `/api/workspaces/{ws}/networks/{name}/code` | `{code}`: answers the network's request for a one-time code (Persian/Arabic digits and spaces are fine) → networks |
| GET | `/api/workspaces/{ws}/networks/{name}/log?lines=N` | → `[{time, text}]` |
| GET | `/api/vpn/drivers` | → `{drivers: [{kind, label, modes, missing: {mode: reason}}], privileged, elevate, os, browser}` |
| GET | `/api/vpn/codes` | → `[{workspace, name, kind, message, retry, since, expires, sources}]`: networks of every workspace waiting for a one-time code (polled by the app's prompt; does not keep the vault unlocked) |
| POST | `/api/vpn/codes/test` | `{ntfyUrl, ntfyTokenRef, command, pattern}` (saved or not) → `{code, source}` of the first message with a code within 90 s |

Networks used by `via` are brought up automatically when needed.

## Git repositories

Under `/api/workspaces/{ws}/repos` (`{repo}` is the GitRemote name). Changes are audited
as `git.*`.

| Method | Path | |
|---|---|---|
| GET | `/api/workspaces/{ws}/repos` | → `[{name, url, web, provider, via, environment, description, docs, auth: "token"\|"deployKey"\|"none", error, status?, last?}]` |
| POST | `/{repo}/check` | → `{ok, defaultBranch, error, provider, canPush, webUrl, apiError}` (Git and API checked separately) |
| POST / DELETE | `/{repo}/clone` | `{branch?}` clone into `repos/<name>` / remove the working copy |
| POST | `/{repo}/fetch`, `/{repo}/pull` | pull: `{rebase?}` (fast-forward only otherwise) → `{output}` |
| GET | `/{repo}/status` | → `{cloned, branch, head, upstream, ahead, behind, default, changes: [{path, orig, status, staged}], busy}` |
| GET | `/{repo}/branches` | → `[{name, remote, current, commit, upstream, track, subject, date}]` |
| POST | `/{repo}/checkout` | `{branch, create?, start?}` |
| GET | `/{repo}/files?ref=` | → `{files, truncated}` (working copy, or a branch/commit) |
| GET / PUT / DELETE | `/{repo}/file?path=&ref=` | → `{path, text, size, binary, truncated}` / `{path, content}` / delete |
| GET | `/{repo}/diff?path=` | uncommitted changes, new files included → `{diff, truncated}` |
| GET | `/{repo}/log?ref=&path=&n=` | → `[{hash, short, author, email, date, subject, refs}]` |
| GET | `/{repo}/commits/{rev}` | a commit with its patch |
| POST | `/{repo}/commit` | `{message, paths?}` (all changes when empty) |
| POST | `/{repo}/push` | `{branch?, force?}` (`--force-with-lease`); sets the upstream |
| POST | `/{repo}/discard` | `{paths?}` (everything when empty) |
| GET | `/{repo}/info` | hosting API → `{provider, defaultBranch, webUrl, canPush}` |
| GET / POST | `/{repo}/mrs?state=open\|merged\|closed\|all` | → `{provider, items: [{id, title, description, state, draft, source, target, author, url, mergeable, checks, created, updated}]}` / `{title, description, source?, target?, draft, removeSource}` |
| GET | `/{repo}/mrs/{id}` | one merge request (GitLab iid / GitHub PR number) |
| POST | `/{repo}/mrs/{id}/comment`, `/{repo}/mrs/{id}/merge` | `{body}` / `{method: merge\|squash\|rebase, removeSource}` |

### Connectors

| Method | Path | |
|---|---|---|
| GET | `/api/git/connections` | → `[{id, provider, url, host, apiUrl, caCert, credential, method: token\|oauth\|gh\|glab, oauthClientId, login, name, created, tokenPage}]` |
| POST | `/api/git/connections/options` | `{provider, url}` → `{url, host, tokenPage, clientId, oauthAppPage, cli: {tool, ready, reason}}` |
| POST | `/api/git/connections` | `{provider: github\|gitlab, url, apiUrl?, caCert?, method: token\|cli, token?}` → connector (token checked against the account; same server = replaced) |
| POST | `/api/git/connections/{id}/test` | → `{ok, login, name, error}` |
| DELETE | `/api/git/connections/{id}` | also deletes its vault item |
| POST | `/api/git/oauth` | `{provider, url, caCert?, clientId}` → `{flow, userCode, verificationUri, verificationUriComplete, expiresIn}`; opsdeck polls the provider |
| GET / DELETE | `/api/git/oauth/{flow}` | → `{status: pending\|done\|error, error, connection}` / cancel |
| GET | `/api/workspaces/{ws}/git/connections/{id}/repos?q=&via=` | → `[{fullName, name, description, httpUrl, sshUrl, webUrl, defaultBranch, private, archived, canPush, updated}]` (through a workspace network with `via`) |

Documentation repositories appear in `GET /api/workspaces/{ws}/docs` as
`repo/<name>/<path>` (read-only).

## DNS connectors and domains

| Method | Path | |
|---|---|---|
| GET | `/api/dns/providers` | → `[{id: cloudflare\|arvancloud, label, apiUrl, tokenPage, dashboard, tokenHelp, tokenPlaceholder, proxy, ttls, comments}]` |
| GET | `/api/dns/connections` | → `[{id, provider, apiUrl, proxy, credential, method, account, zones, created, label, tokenPage, dashboard}]` |
| POST | `/api/dns/connections` | `{provider, token, apiUrl?, proxy?, id?}` → connector (token checked against the provider; `id` reconnects that connector, the same account is replaced) |
| POST | `/api/dns/connections/{id}/test` | → `{ok, account, zones, error}` |
| DELETE | `/api/dns/connections/{id}` | also deletes its vault item |
| GET | `/api/dns/connections/{id}/zones?q=` | → `[{id, name, status, paused, plan, nameServers, currentNameServers, account, modified, url}]` |
| GET | `/api/workspaces/{ws}/domains` | → `[{name, zone, environment, protected, description, connection, provider, account, info: zone, error, proxyLabel, ttls, comments}]` for every Domain resource |
| GET | `/api/workspaces/{ws}/domains/{name}` | one domain (502 with the reason when its connector or zone cannot be reached) |
| GET | `/api/workspaces/{ws}/domains/{name}/records?type=&q=` | → `[{id, type, name, fqdn, content, ttl, priority, proxied, proxiable, comment, locked, modified, raw}]` |
| POST | `/api/workspaces/{ws}/domains/{name}/records` | `{type, name, content, ttl, priority, proxied?, comment?}` → record (400 when the input is invalid) |
| PUT | `/api/workspaces/{ws}/domains/{name}/records/{id}` | same body → record |
| DELETE | `/api/workspaces/{ws}/domains/{name}/records/{id}` | |

Record changes are audited as `dns.record.create`, `dns.record.update` and
`dns.record.delete` on `domain/<name>`; connecting and disconnecting as `dns.connect` and
`dns.disconnect`.

## Certificates (ACME)

| Method | Path | |
|---|---|---|
| GET | `/api/acme/cas` | → `[{id, label, directory, staging, wildcard, note}]` |
| GET | `/api/workspaces/{ws}/certificates` | → `[{name, domains, ca, caLabel, staging, environment, protected, item, issued, info: {subject, domains, issuer, notBefore, notAfter, serial, fingerprint, keyType, chain, daysLeft}, issuedAt, renewDays, autoRenew, due, reason, error, job}]` for every Certificate resource |
| GET | `/api/workspaces/{ws}/certificates/{name}` | the status plus `chain` (the public PEM chain) |
| POST | `/api/workspaces/{ws}/certificates/{name}/issue` | starts an issuance (or returns the running one) → job |
| GET | `/api/workspaces/{ws}/certificates/jobs` | the workspace's jobs of the last two hours, newest first |
| GET | `/api/workspaces/{ws}/certificates/jobs/{id}?since=N&wait=1` | → `{id, cert, state: running\|done\|error, lines (from N), total, error, result: {item, info}, started, ended, actor}`; `wait` holds the answer up to 25 s while the job runs |
| POST | `/api/workspaces/{ws}/certificates/{name}/deploy` | `{host, certPath?, keyPath?, leafPath?, chainPath?, bundlePath?, command?}` → `{written, exitCode, stdout, stderr}` (502 with `error` when a write or the command failed) |

Issuances are audited as `certificate.issue` (started) and `certificate.issued`
(finished; actor `user`, `system` for automatic renewals, or `agent:<session>`), deployments
as `certificate.deploy`.

## Topology

| Method | Path | |
|---|---|---|
| GET | `/api/topology/catalog` | → `{nodeTypes: [{type, label, category, icon, variants, tech}], linkKinds: [{kind, label, category, port}], zoneKinds, deploymentKinds}` |
| GET | `/api/workspaces/{ws}/diagram` | → `{topology: {version, zones, nodes, links, layout}, rev, text}` (`text` = what the agent reads) |
| PUT | `/api/workspaces/{ws}/diagram` | `{topology, rev, force?}` → `{rev, text}`; 409 `conflict` when the file changed since `rev` |
| GET | `/api/workspaces/{ws}/diagram/export?format=md\|yaml` | download: Markdown for an AI (description + YAML) or `topology.yaml` |
| POST | `/api/workspaces/{ws}/diagram/import-inventory` | `{topology}` → `{topology, added}` (not saved) |

`GET /api/workspaces/{ws}/topology` is the older inventory graph (resources and `via`).

## Skills

| Method | Path | |
|---|---|---|
| GET | `/api/workspaces/{ws}/skills` | → `[{name, description, path, category, files, builtin, source, license}]`: the workspace's skills plus the built-in library (unless turned off) |
| GET | `/api/skills/library` | → `{skills, attribution}` |
| GET | `/api/skills/library/{name}?file=` | → `{content}` (SKILL.md or a supporting file) |
| POST | `/api/workspaces/{ws}/skills/{name}/copy` | copies a built-in skill into `skills/<name>/` |

## Scripts

| Method | Path | |
|---|---|---|
| GET | `/api/workspaces/{ws}/scripts` | → `{user, workspace}`: the user's library and the workspace's scripts (without bodies) |
| GET | `/api/workspaces/{ws}/scripts/{scope}/{name}?version=` | → script with `body`, `hash`, `status`, `versions`; `scope` is `user` or `workspace` |
| PUT | `/api/workspaces/{ws}/scripts/{scope}/{name}` | create/update (saved as approved; a change of what runs makes a new version) |
| DELETE | `/api/workspaces/{ws}/scripts/{scope}/{name}` | delete with its versions |
| POST | `/api/workspaces/{ws}/scripts/{scope}/{name}/approve` | `{hash}` — approve the content the user reviewed (409 if it changed) |
| POST | `/api/workspaces/{ws}/scripts/{scope}/{name}/copy` | copy to the other scope (pin into the workspace / save to my library) |
| POST | `/api/workspaces/{ws}/scripts/{scope}/{name}/run` | `{target, params, force, hash}` → `{script, results: [{host, exitCode, skipped, stdout, stderr, durationMs, error, log}]}` |
| GET | `/api/workspaces/{ws}/script-runs?script=&host=&limit=` | run history, newest first |

## Ansible

A vault password that is not at hand answers **409** `{code: "ansible_password", vaultId}`; a
wrong one **422** `{code: "ansible_wrong_password"}`. Requests that need a password accept
`password` (+ `vaultId`) to use it once; `unlock` keeps it in memory. See [ansible.md](ansible.md).

| Method | Path | |
|---|---|---|
| GET | `/api/workspaces/{ws}/ansible` | → projects: `{name, source, repo, path, root, inventories, playbooks, vaultIds, unlocked, remembered, saved, ansible: {installed, version}…}` |
| POST | `/api/workspaces/{ws}/ansible/projects` | `{name, source: folder\|repo, repo, path, inventory, environment, description, via, scaffold}` — add a project (scaffold: create a starter layout) |
| GET | `/api/workspaces/{ws}/ansible/p/{project}` | → the project |
| GET | `…/p/{project}/files` | → `{files: [{path, size, modified, type, encrypted, vaultId, inline}], truncated}` |
| GET / PUT / DELETE | `…/p/{project}/file?path=` | read (vault files as their encrypted text) / `{path, content, create}` write (refused over a vault file) / delete |
| POST | `…/p/{project}/rename` | `{from, to}` |
| GET | `…/p/{project}/search?q=&regex=1` | → `[{path, line, text}]` (vault files are skipped) |
| POST / DELETE | `…/p/{project}/unlock` | `{vaultId, password, save, agentUse}` — check and keep a password (save: also as a vault item referenced by the project) / `?vaultId=` forget |
| POST | `…/p/{project}/vault/{action}` | `view` `{path}` → `{text}`; `save` `{path, text, vaultId}`; `set` `{path, key, value, vaultId}`; `encrypt` `{path, vaultId}`; `decrypt` `{path}`; `rekey` `{path, newPassword, newVaultId}`; `encrypt-string` `{name, value, vaultId}` → `{text}` |
| GET | `…/p/{project}/inventory?inventory=` | → `{graph, groups, children, hosts, generated}` (`inventory=workspace`: generated from the workspace's hosts) |
| GET | `…/p/{project}/inventory/host?inventory=&host=` | → `{vars}` (YAML) |
| GET | `…/p/{project}/modules?refresh=1` | → `[{name, description}]`: the modules ansible-doc knows for the project (cached) |
| GET | `…/p/{project}/modules/doc?name=` | → `{name, short, description, options: [{name, type, elements, required, default, choices, aliases, description}], freeForm, examples}` |
| POST | `…/p/{project}/check` | `{kind: syntax\|list-hosts\|list-tasks\|list-tags, playbook, inventory, limit, tags}` → `{ok, output}` |
| POST | `…/p/{project}/runs` | `{kind: playbook\|adhoc\|galaxy\|lint, playbook, inventory, limit, tags, skipTags, extraVars, check, diff, verbosity, forks, startAt, become, pattern, module, args, requirements, password?, vaultId?}` → the run |
| GET | `/api/workspaces/{ws}/ansible/runs?project=&limit=` | runs, newest first: `{id, project, options, command, actor, started, ended, status, exitCode, recap, durationMs}` |
| GET | `/api/workspaces/{ws}/ansible/runs/{id}` | → the run |
| GET | `/api/workspaces/{ws}/ansible/runs/{id}/log?since=` | SSE: `{line, ts}` per line (live runs; `since` resumes), then `event: status` (the run) and `event: end` |
| GET | `/api/workspaces/{ws}/ansible/runs/{id}/log.txt` | the full log as text |
| POST | `/api/workspaces/{ws}/ansible/runs/{id}/cancel` | stop the run |

## Hosts

| Method | Path | |
|---|---|---|
| POST | `/api/workspaces/{ws}/hosts/{host}/check` | → `{reachable, latencyMs, error, path}` |
| GET | `/api/workspaces/{ws}/hosts/{host}/stats` | → `HostStats` |
| POST | `/api/workspaces/{ws}/hosts/{host}/exec` | `{command, timeoutSec}` → `{stdout, stderr, exitCode, durationMs}` |
| GET | `/api/workspaces/{ws}/hosts/{host}/files?path=` | → `{path, entries: [{name, dir, size, mode, modified}]}` |
| GET | `/api/workspaces/{ws}/hosts/{host}/files/content?path=` | → `{path, content, truncated}` |
| PUT | `/api/workspaces/{ws}/hosts/{host}/files/content` | `{path, content}` |
| DELETE | `/api/workspaces/{ws}/known-hosts?host=` | forget pinned host key |
| WS | `/api/workspaces/{ws}/hosts/{host}/terminal?cols=&rows=` | WebSocket, JSON text frames |

Terminal frames: client→server `{"t":"d","d":"<input>"}`, `{"t":"r","c":cols,"r":rows}`;
server→client `{"t":"d","d":"<output>"}`, `{"t":"x","code":0}` (exit), `{"t":"e","msg":"..."}`, `{"t":"s","msg":"connecting via ..."}`.

```ts
type HostStats = { hostname; os; kernel; uptimeSec: number; load: number[]; cpus: number;
                   memTotal: number; memUsed: number; swapTotal: number; swapUsed: number;
                   disks: { mount; size: number; used: number }[] }   // bytes
```

## Port forwards / panels

| POST | `/api/workspaces/{ws}/forwards` | `{target: "host/app-1" or "panel/grafana", remoteHost?, remotePort}` → `Forward` |
| GET | `/api/forwards` | → `[Forward]` |
| DELETE | `/api/forwards/{id}` | |
| POST | `/api/workspaces/{ws}/panels/{name}/open` | → `{url}` (direct url, or a local forward if the panel has `via`) |

`Forward = { id, workspace, target, remote: "host:port", localPort, url, created }`

## Databases

Under `/api/workspaces/{ws}/databases/{db}` (`{db}` is the Database resource; `?database=`
picks another database on the server). See [databases.md](databases.md).

| Method | Path | |
|---|---|---|
| GET | `/info` | → `{info: {engine (postgres, mysql, mongodb, redis), version, user, database, address}, path, environment, protected, atomic}` (`atomic`: a change script runs as one transaction) |
| GET | `/databases` | → `[name]` (Redis: `"0"`, `"1"`, …) |
| GET | `/tables?match=` | → `[{schema, name, type, rows (estimate, -1 = unknown), bytes, query}]`: tables and views, collections, or Redis keys (`match`: a SCAN pattern, at most 1,000 keys; `schema` is the part before the first `:`). `query` shows its content |
| GET | `/table?schema=&name=` | → `{schema, name, kind (table, collection, key), columns: [{name, type, nullable, default, primary}], indexes: [{name, definition, unique, primary}], properties?: [{name, value}], note?}`. For a collection, fields come from sampled documents (`default` = how many have it); for a key, `properties` |
| POST | `/classify` | `{sql}` → `{risk, reasons, statements, environment, protected, atomic}` |
| POST | `/query` | `{sql, database?, write?, maxRows?}` (`sql` is the script in the engine's language) → `{risk, reasons, results: [{statement, columns?, rows?, truncated?, rowsAffected, durationMs, error?, note?}], database, durationMs, committed, error?}`. Values longer than 65,536 characters are cut; documents and arrays come as JSON. A script that is not a read answers **409** `{code: "not_read", risk, reasons, statements, environment, protected, atomic}` until it is sent with `write: true` (audited as `db.query`). Errors of a statement are part of a 200 answer. |

## Docker (on a Host, over SSH)

All paths start with `/api/workspaces/{ws}/hosts/{host}/docker`. Changes are audited and
keep running when the request is cancelled; long ones return `{output}` (docker's output).
See [docker.md](docker.md).

| Method | Path | |
|---|---|---|
| GET | `/info` | engine version, OS, CPUs, memory, counts, storage, `swarm`, `compose` version, `sudo`, warnings |
| GET | `/df` | `docker system df` rows |
| POST | `/prune` | `{kind}`: containers, images, images-unused, build-cache, networks, volumes, system |
| GET | `/inspect/{kind}/{id}` | raw JSON; kind ∈ container, image, volume, network, service, node |
| GET | `/containers?all=1` | → `[{id, name, image, state, status, health, ports, created, command, mounts, networks, project, service, stack}]` |
| GET | `/containers/{id}/inspect` | raw JSON |
| GET | `/containers/{id}/top` | → `[{pid, user, cpu, mem, started, time, command}]` |
| GET | `/containers/{id}/exec?cols=&rows=&user=&shell=` | WebSocket, same protocol as the host terminal |
| POST | `/containers/{id}/{start\|stop\|restart\|kill\|pause\|unpause\|rm}` | |
| GET | `/stats?ids=` | → `[{id, name, cpu, mem, memPct, net, block, pids}]` |
| GET | `/images` | → `[{id, repository, tag, size, created}]` |
| POST | `/images/pull` | `{ref}` |
| DELETE | `/images?ref=&force=1` | |
| GET | `/networks` | → `[{id, name, driver, scope, internal, builtin, project, stack, created}]` |
| DELETE | `/networks/{id}` | |
| GET | `/volumes` | → `[{name, driver, scope, mountpoint, labels, project, stack, containers, anonymous}]` |
| POST | `/volumes` | `{name}` |
| DELETE | `/volumes/{name}` | |
| GET | `/volumes/{name}/size` | → `{bytes}` |
| GET | `/volumes/{name}/files?path=` | → `{access: direct\|sudo\|container, entries: [{name, type, size, modified, mode, owner}]}` |
| GET | `/volumes/{name}/file?path=` | → `{content, binary, truncated, size}` (first 1 MB) |
| GET | `/volumes/{name}/file?path=&download=1` | the file, streamed |
| PUT | `/volumes/{name}/file` | `{path, content}` |
| POST | `/volumes/{name}/upload?path=` | multipart files, streamed into the folder |
| POST | `/volumes/{name}/mkdir` | `{path, name}` |
| DELETE | `/volumes/{name}/file?path=` | file or folder (recursive) |
| GET | `/compose` | → `[{name, status, configFiles, workingDir}]` |
| POST | `/compose` | `{name, dir, content, start}`: new project (validated) |
| GET | `/compose/{project}` | → `[{id, name, service, image, state, status, health, exitCode, ports}]` |
| GET | `/compose/{project}/files` | → `[{path, kind: compose\|env, exists}]` |
| GET | `/compose/{project}/file?path=` | → `{path, content}` |
| PUT | `/compose/{project}/file` | `{path, content, deploy}`: validated with `docker compose config`, backup kept |
| POST | `/compose/{project}/{action}` | `{services?}`; action ∈ up, recreate, pull, restart, stop, start, down, down-volumes |
| GET | `/swarm` | → `{state, manager, nodeId, nodeAddr, nodes, managers, error}` |
| GET | `/services` | → `[{id, name, mode, replicas, running, desired, image, ports, stack}]` |
| GET | `/services/{name}/tasks` | → `[{id, name, image, node, desiredState, currentState, error, ports}]` |
| POST | `/services/{name}/{scale\|redeploy\|rollback\|image\|rm}` | `{arg}`: replicas or image |
| GET | `/stacks` | → `[{name, services}]` |
| GET | `/stacks/{name}/tasks` | tasks of the stack |
| POST | `/stacks` | `{name, file? \| content?, prune}` |
| DELETE | `/stacks/{name}` | |
| GET | `/nodes` | → `[{id, hostname, status, availability, managerStatus, engineVersion, self}]` |
| POST | `/nodes/{id}/availability` | `{availability: active\|pause\|drain}` |

## Kubernetes

All paths start with `/api/workspaces/{ws}/clusters/{c}`. `{type}` is a resource id from
`/resources` (`pods`, `deployments.apps`, `certificates.cert-manager.io`…), or a plural,
kind or short name; `{ns}` is `_` for cluster-scoped objects; `namespace=*` (or empty)
means all namespaces. Changes are audited and keep running when the request is cancelled.
See [kubernetes.md](kubernetes.md).

| Method | Path | |
|---|---|---|
| GET | `/overview?namespace=` | → `{version, platform, namespaces, nodes: [NodeSummary], pods, workloads, problems: [{severity, kind, namespace, name, reason, message}], warnings: [Event], metrics}` |
| GET | `/resources?refresh=1` | → `[{id, group, version, resource, kind, namespaced, verbs, shortNames, category}]` (discovery, cached 5 minutes) |
| GET | `/namespaces` | → `{namespaces: [string], default}` |
| GET | `/list/{type}?namespace=&labelSelector=&fieldSelector=` | → `Table` |
| GET | `/object/{type}/{ns}/{name}` | → `{object, yaml, editYaml, type}` (Secret values masked; `editYaml` without status and server-set fields) |
| GET | `/object/{type}/{ns}/{name}/events` | → `[Event]` |
| GET | `/object/{type}/{ns}/{name}/pods` | → `Table` of the object's pods (workloads, services, nodes) |
| GET | `/object/{type}/{ns}/{name}/history` | → `[{revision, name, created, images, cause, current, replicas}]` |
| POST | `/object/secrets/{ns}/{name}/reveal` | → `{key: value}` (audited) |
| DELETE | `/object/{type}/{ns}/{name}?force=1` | `force`: no grace period |
| POST | `/object/{type}/{ns}/{name}/{action}` | action ∈ scale `{replicas}`, restart, image `{container, image}`, pause, resume, suspend, unsuspend, trigger (CronJob → Job), undo `{revision}` (0 = previous), cordon, uncordon, drain → `{ok, output?}` |
| POST | `/apply` | `{manifest, namespace, dryRun}` → `[{kind, name, namespace, action: created\|configured\|unchanged\|failed, diff, error, note}]` (server-side apply; dry runs are not audited) |
| GET | `/metrics/pods?namespace=` | → `{"ns/pod": Usage, "ns/pod/container": Usage}`; `Usage = {cpu (millicores), memory (bytes)}`; 404 without metrics-server |
| GET | `/metrics/nodes` | → `{node: Usage}` |
| GET | `/helm?namespace=` | → `[{name, namespace, revision, status, chart, chartVersion, appVersion, updated, description}]` |
| GET | `/helm/{ns}/{name}?revision=` | → release + `{values, manifest, notes, history}` |
| POST | `/forward` | `{type, namespace, name, port}` → `Forward` (pods, services — a ready pod and the target port — and workloads) |
| GET | `/logs/stream?type=&namespace=&name=&container=&lines=&since=&previous=1&follow=1` | SSE, see below; a workload streams all its pods |
| GET | `/events/stream?namespace=` | SSE: the last hour, then `{event: Event}` as events are added or updated |
| GET | `/exec/{ns}/{pod}?container=&shell=auto\|bash\|sh&cols=&rows=` | WebSocket terminal (same protocol as host terminals) |

`Table = {type, columns: [{name, type, format, description, priority}], rows: [{name, namespace, uid, created, labels, owner, deleting, cells}]}` —
the API server's own columns (`priority > 0` = wide). `Event = {time, type, reason, object: "Kind/name", namespace, message, count, source, uid}`.

## Logs (SSE)

`GET /api/workspaces/{ws}/logs/stream?host=&source=journal|file|docker|compose|service|events&unit=&path=&container=&project=&service=&lines=200|all&since=&follow=1`
→ `text/event-stream`, each event `data: {"line": "..."}`; `event: end` on finish, `event: error` with `{"message"}`.
Docker sources add `ts` (docker's RFC3339 timestamp), `src` (compose service or swarm
task) and `stderr`; `since` is a duration (`15m`, `2h`, `1d`) or a timestamp, which is how
a dropped stream resumes. Kubernetes log streams send the same `{ts, src, line}` (`src` is
the pod, the container, or `pod/container` when several stream together). `source=events` sends `{"event": {time, type, action, id, name, scope, attrs}}`.

## Agent

`approval_request` events carry `grantLabel` when "always allow" covers more than the exact
call (e.g. every read in a folder). For a change over several hosts (`hosts`), approving with
`canary: true` runs it on the first host and then asks again (`stage: "next"`: approve continues
on the rest, deny stops).

| Method | Path | |
|---|---|---|
| GET | `/api/workspaces/{ws}/sessions` | → `[SessionInfo]` |
| POST | `/api/workspaces/{ws}/sessions` | `{model?, mode?}` → `SessionInfo` |
| GET | `/api/workspaces/{ws}/sessions/{id}` | → `{session: SessionInfo, events: [Event]}` |
| PUT | `/api/workspaces/{ws}/sessions/{id}` | `{title?, model?, mode?}` |
| DELETE | `/api/workspaces/{ws}/sessions/{id}` | |
| POST | `/api/workspaces/{ws}/sessions/{id}/messages` | `{text}` → 202 |
| POST | `/api/workspaces/{ws}/sessions/{id}/interrupt` | |
| POST | `/api/workspaces/{ws}/sessions/{id}/approvals/{approvalId}` | `{decision: "approve"\|"deny"\|"always", note?, secret?, canary?}` — `secret` answers a card that asks for a password (`approval_request.secret`); a missing or wrong one is 422 `bad_secret` and the card stays open |
| GET | `/api/workspaces/{ws}/sessions/{id}/stream` | SSE: every new `Event` as `data: <json>` |
| POST | `/api/workspaces/{ws}/sessions/{id}/questions/{questionId}` | `{choices?: [label], text?, skipped?}` answers an `ask_user` question |
| GET | `/api/workspaces/{ws}/sessions/{id}/tasks` | → `[TaskInfo]` background tasks |
| GET | `/api/workspaces/{ws}/sessions/{id}/tasks/{taskId}` | → `{task: TaskInfo, output}` (the latest megabyte) |
| POST | `/api/workspaces/{ws}/sessions/{id}/tasks/{taskId}/stop` | |
| GET | `/api/workspaces/{ws}/sessions/{id}/outputs/{outputId}` | the full text of a long tool output (`tool_result.outputId`) |
| POST | `/api/workspaces/{ws}/sessions/{id}/fork` | `{at?: eventId}` → 201 `{session: SessionInfo, text}` — `at`: copy everything before that user message, whose text is returned |
| POST | `/api/workspaces/{ws}/sessions/{id}/export` | `{as: "report"\|"runbook"\|"skill", title?}` → `{path}` |
| POST | `/api/workspaces/{ws}/sessions/{id}/shell` | `{command, target?}` → the `shell` event; no target = this computer |
| POST | `/api/workspaces/{ws}/sessions/{id}/compact` | summarize the conversation now |
| GET | `/api/workspaces/{ws}/harness` | → `{commands, subagents, web, search, local, models}` for the chat |
| POST | `/api/workspaces/{ws}/hooks/trust` | trust the workspace's hooks on this machine → `Workspace` |

`POST /sessions` also takes `headless: true` (used by `opsdeck run`): what would ask is denied.
A message starting with `/<command>` of the workspace is sent as that command's prompt.

```ts
type SessionInfo = { id; workspace; title; model; mode: "plan"|"ask"|"auto"|"full";
                     state: "idle"|"running"|"waiting_approval"|"waiting_input";
                     created; updated; usage: {inputTokens: number; outputTokens: number}; headless?; forkedFrom? }
type TaskInfo = { id; callId; tool; target; command; notifyOn?; started; ended?; state: "running"|"done"|"failed"|"stopped";
                  exitCode; matched?; error?; lines }
type Event = { id: string; ts: string; type: string; parent?: string; ... }
// parent: set on the events of a subagent — the callId of its delegate call
// user              {text, command?, prompt?}     command: a workspace slash command, prompt: what the agent got
// notification      {text}                        a background task finished or matched (starts a turn)
// shell             {command, target, label, output, exitCode, durationMs}   the user's ! command
// recall            {items: [{kind: "skill"|"doc"|"report"|"script", ref, title}], preloaded}
//                   workspace knowledge attached to the message above; preloaded = skill included in full
// assistant_delta   {messageId, text}            streaming chunk (not persisted)
// assistant         {messageId, text}            final text of a message
// tool_call         {callId, tool, input, risk, target, environment, agent?}
// tool_result       {callId, output, isError, durationMs, view?, outputId?, outputSize?}
//                   view: structured data for the chat (search, page, http, timeseries, vector, alerts, logs, eol, vulns, releases)
//                   outputId: the whole output is kept (the model got the first 60,000 characters)
// tool_progress     {callId, text}                what a running call is doing (not persisted)
// approval_request  {approvalId, callId, tool, input, risk, target, environment, reason, preview, hosts?, canary?, stage?, title?}
//                   preview: diff/command text; hosts: a change over several hosts; stage "next": the rest of a canary rollout
// approval_resolved {approvalId, decision, note, canary?}
// question          {questionId, callId, question, options: [{label, description?}], multiple, allowText}
// question_answered {questionId, choices?, text?, skipped?}
// todo              {callId, items: [{text, status: "pending"|"in_progress"|"done"|"skipped"}]}
// subagent          {callId, agent, phase: "start"|"end", model?, readOnly?, state?, steps?, toolCalls?, inputTokens?, outputTokens?}
// task              {task: TaskInfo}              a background task started or changed
// hook              {callId?, hook, event, outcome: "ok"|"deny"|"ask"|"allow"|"error", message?}
// status            {state, usage}
// error             {message}
// compaction        {summary}
```

## Audit

| GET | `/api/audit?workspace=&limit=200&before=<seq>` | → `[{seq, ts, workspace, actor, action, target, detail, result, hash}]` newest first |
| GET | `/api/audit/verify` | → `{ok, entries, brokenAt}` |
