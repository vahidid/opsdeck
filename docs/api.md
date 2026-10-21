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

```ts
type Settings = { autoLockMinutes: number; defaultModel: string;
  providers: { id: string; type: "anthropic"|"openai"; name: string; baseUrl: string;
               apiKeyRef: string;   // vault ref, e.g. vault://global/anthropic-key ; empty for Ollama
               models: string[] }[] }
```
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
Kinds: `Host`, `HostGroup`, `WireGuard`, `SocksProxy`, `Panel`, `GitRemote`, `KubernetesCluster`.

## Networks

| GET | `/api/workspaces/{ws}/networks` | → `[{name, kind, status: "down"\|"up"\|"error", error, since, address}]` |
| POST | `/api/workspaces/{ws}/networks/{name}/up` / `/down` | |

Networks used by `via` are brought up automatically when needed.

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
