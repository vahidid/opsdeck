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
| GET | `/api/models` | → `[{id: "providerId/model", provider, model, label}]` |
| GET | `/api/agents/detect` | → `[{kind, name, command, args, path, version, login, models}]` agent CLIs found on PATH |

```ts
type Settings = { autoLockMinutes: number; defaultModel: string;
  providers: { id: string; type: "anthropic"|"openai"; name: string; baseUrl: string;
               apiKeyRef: string;   // vault ref, e.g. vault://global/anthropic-key ; empty for Ollama
               models: string[] }[];
  vpn: { elevate: "auto"|"sudo"|"pkexec"|"none";   // how system-mode VPN clients get root
         browser: string };                         // Chrome/Edge/Chromium for SSO login windows ("" = detect)
  localAccess: { disabled: boolean;        // agent access to this computer (default on)
                 allowedDirs: string[];    // readable without asking; "~/..." is expanded
                 autoReadCommands: boolean } } // run read-only local_exec commands without asking
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

| GET | `/api/workspaces/{ws}/networks` | → `[{name, kind, mode: "userspace"\|"system"\|"", status: "down"\|"connecting"\|"auth"\|"up"\|"error", error, authUrl, authWindow, since, address, interface, server, detail, proxy, lastHandshake, rxBytes, txBytes, missing, usedBy}]` |
| POST | `/api/workspaces/{ws}/networks/{name}/up` / `/down` | `up` waits up to ~20 s; slower connects continue in the background (`connecting` / `auth`) |
| GET | `/api/workspaces/{ws}/networks/{name}/log?lines=N` | → `[{time, text}]` |
| GET | `/api/vpn/drivers` | → `{drivers: [{kind, label, modes, missing: {mode: reason}}], privileged, elevate, os, browser}` |

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

## Docker (on a Host, over SSH)

| GET | `/api/workspaces/{ws}/hosts/{host}/docker/containers?all=1` | → `[{id, name, image, state, status, ports, created}]` |
| GET | `/api/workspaces/{ws}/hosts/{host}/docker/images` | → `[{id, repository, tag, size, created}]` |
| GET | `/api/workspaces/{ws}/hosts/{host}/docker/stats` | → `[{id, name, cpu, mem, memPct, net, block}]` |
| GET | `/api/workspaces/{ws}/hosts/{host}/docker/containers/{id}/inspect` | → raw JSON |
| POST | `/api/workspaces/{ws}/hosts/{host}/docker/containers/{id}/{start\|stop\|restart\|rm}` | |

## Kubernetes

| GET | `/api/workspaces/{ws}/clusters/{c}/namespaces` | → `[string]` |
| GET | `/api/workspaces/{ws}/clusters/{c}/list/{type}?namespace=` | type ∈ pods, deployments, statefulsets, daemonsets, services, ingresses, nodes, events, configmaps, jobs, cronjobs → `[Row]` |
| GET | `/api/workspaces/{ws}/clusters/{c}/yaml/{type}/{ns}/{name}` | → `{yaml}` (secrets redacted) |
| GET | `/api/workspaces/{ws}/clusters/{c}/logs/{ns}/{pod}?container=&tail=` | → `{lines: [string]}` |
| POST | `/api/workspaces/{ws}/clusters/{c}/deployments/{ns}/{name}/restart` | |
| POST | `/api/workspaces/{ws}/clusters/{c}/deployments/{ns}/{name}/scale` | `{replicas}` |
| DELETE | `/api/workspaces/{ws}/clusters/{c}/pods/{ns}/{name}` | |

`Row = { name, namespace, status, age, ready?, restarts?, node?, image?, replicas?, type?, clusterIP?, ports?, message?, reason?, ... }` (columns vary per type; render all keys).

## Logs (SSE)

`GET /api/workspaces/{ws}/logs/stream?host=&source=journal|file|docker&unit=&path=&container=&lines=200&follow=1`
→ `text/event-stream`, each event `data: {"line": "..."}`; `event: end` on finish, `event: error` with `{"message"}`.

## Agent

`approval_request` events carry `grantLabel` when "always allow" covers more than the exact
call (e.g. every read in a folder).

| Method | Path | |
|---|---|---|
| GET | `/api/workspaces/{ws}/sessions` | → `[SessionInfo]` |
| POST | `/api/workspaces/{ws}/sessions` | `{model?, mode?}` → `SessionInfo` |
| GET | `/api/workspaces/{ws}/sessions/{id}` | → `{session: SessionInfo, events: [Event]}` |
| PUT | `/api/workspaces/{ws}/sessions/{id}` | `{title?, model?, mode?}` |
| DELETE | `/api/workspaces/{ws}/sessions/{id}` | |
| POST | `/api/workspaces/{ws}/sessions/{id}/messages` | `{text}` → 202 |
| POST | `/api/workspaces/{ws}/sessions/{id}/interrupt` | |
| POST | `/api/workspaces/{ws}/sessions/{id}/approvals/{approvalId}` | `{decision: "approve"\|"deny"\|"always", note?}` |
| GET | `/api/workspaces/{ws}/sessions/{id}/stream` | SSE: every new `Event` as `data: <json>` |

```ts
type SessionInfo = { id; workspace; title; model; mode: "plan"|"ask"|"auto"; state: "idle"|"running"|"waiting_approval";
                     created; updated; usage: {inputTokens: number; outputTokens: number} }
type Event = { id: string; ts: string; type: string; ... }
// user              {text}
// assistant_delta   {messageId, text}            streaming chunk (not persisted)
// assistant         {messageId, text}            final text of a message
// tool_call         {callId, tool, input, risk, target, environment}
// tool_result       {callId, output, isError, durationMs}
// approval_request  {approvalId, callId, tool, input, risk, target, environment, reason, preview}   preview: diff/command text
// approval_resolved {approvalId, decision, note}
// status            {state, usage}
// error             {message}
// compaction        {summary}
```

## Audit

| GET | `/api/audit?workspace=&limit=200&before=<seq>` | → `[{seq, ts, workspace, actor, action, target, detail, result, hash}]` newest first |
| GET | `/api/audit/verify` | → `{ok, entries, brokenAt}` |
