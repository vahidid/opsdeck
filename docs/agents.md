# Use opsdeck from other agents

opsdeck is an MCP server too. Claude Code, Codex, Cursor, Gemini CLI, VS Code or any other
MCP client can use a workspace's tools: run commands on its hosts over SSH (through its
VPNs and jump hosts), read logs and metrics, work with Docker, Kubernetes, databases, DNS,
certificates and deployments.

The outside agent does the thinking; opsdeck does the doing. It keeps every credential and
uses it on the agent's behalf. Each call is classified by risk and checked against the
workspace's permission rules, its output is redacted, and it is written to the activity
journal, just like a call of opsdeck's own agent. A change waits until you approve it in
opsdeck.

There is also a **skill pack** that teaches the agent how to work through opsdeck: look
before it changes anything, prefer the specific tools, expect approvals, and leave a report.

## 1. Make an access key

In opsdeck open **App settings › Agent access (MCP) › Connect an agent**. Pick the agent,
the workspace and a mode:

| Mode | What happens |
|---|---|
| **Read-only** (plan) | the agent can only read; every change is refused |
| **Ask** | every change waits for your approval in opsdeck |
| **Auto** | what the workspace's permission rules allow runs; the rest asks |

Changes on a **protected** environment always ask, whatever the mode.

opsdeck shows the key (`odk_…`) once, with the setup for the agent you picked. It keeps
only a hash of it. A key reaches one workspace; make one per workspace and agent. You can
change a key's mode or revoke it at any time; a revoked key is refused at once.

## 2. Connect the agent

opsdeck serves MCP over streamable HTTP at `http://127.0.0.1:7788/mcp/ext`, with the key as
`Authorization: Bearer odk_…`. For agents that only start stdio servers, `opsdeck mcp`
relays the same server over stdio. It reads the key from `OPSDECK_TOKEN` and finds the
running opsdeck by itself, even when the desktop app had to use another port. The desktop
app answers `mcp` too:

- macOS: `/Applications/opsdeck.app/Contents/MacOS/opsdeck mcp`
- Linux (.deb/.rpm): `opsdeck-desktop mcp`
- Windows: `opsdeck.exe mcp`, with the `opsdeck.exe` the installer put under Program Files
  (or the portable `.exe`)

opsdeck must be running (the app open, or `opsdeck serve`) and unlocked while the agent
works.

**Claude Code**

```bash
claude mcp add --scope user --transport http opsdeck-acme http://127.0.0.1:7788/mcp/ext \
  --header "Authorization: Bearer odk_…"
# or over stdio:
claude mcp add --scope user opsdeck-acme --env OPSDECK_TOKEN=odk_… -- opsdeck mcp
```

**Codex** (`~/.codex/config.toml`)

```toml
[mcp_servers.opsdeck-acme]
command = "opsdeck"
args = ["mcp"]
env = { OPSDECK_TOKEN = "odk_…" }
```

**Cursor** (`~/.cursor/mcp.json`)

```json
{ "mcpServers": { "opsdeck-acme": { "url": "http://127.0.0.1:7788/mcp/ext", "headers": { "Authorization": "Bearer odk_…" } } } }
```

**Gemini CLI** (`~/.gemini/settings.json`)

```json
{ "mcpServers": { "opsdeck-acme": { "httpUrl": "http://127.0.0.1:7788/mcp/ext", "headers": { "Authorization": "Bearer odk_…" } } } }
```

**VS Code** (`.vscode/mcp.json`)

```json
{ "servers": { "opsdeck-acme": { "type": "http", "url": "http://127.0.0.1:7788/mcp/ext", "headers": { "Authorization": "Bearer odk_…" } } } }
```

Any other client: streamable HTTP at the URL above with the header, or `opsdeck mcp` over
stdio with `OPSDECK_TOKEN`.

## 3. Install the skill

**Claude Code: the plugin.** The plugin brings the skill and the MCP server together and
asks for the access key when you install it:

```
/plugin marketplace add vahidid/opsdeck
/plugin install opsdeck@opsdeck
```

The plugin connects to `http://127.0.0.1:7788/mcp/ext`. If your opsdeck listens on
another port, add the server yourself as in step 2 and install just the skill.

**Any agent: `opsdeck skill install`.** It writes the skill where the agent looks for it:

| Command | Writes |
|---|---|
| `opsdeck skill install claude-code` | `~/.claude/skills/opsdeck/` |
| `opsdeck skill install codex` | `~/.codex/skills/opsdeck/` |
| `opsdeck skill install gemini` | `~/.gemini/extensions/opsdeck/` (an extension with `GEMINI.md`) |
| `opsdeck skill install cursor` | `./.cursor/rules/opsdeck.mdc` in the current project |
| `opsdeck skill install dir --dir PATH` | `SKILL.md` and `references/` in `PATH` |

`--force` replaces an earlier install. If your version of an agent reads skills from
another folder, use `--dir`. `opsdeck skill print` prints the skill, to paste into an
`AGENTS.md` or a system prompt. The files are in
[`plugins/opsdeck/skills/opsdeck`](../plugins/opsdeck/skills/opsdeck).

## Where the calls show up

Each key has a session on the workspace's **Agent** page, named after the key, e.g.
*Claude Code (MCP)*. Its tool calls appear there as they run. A change waits there with its
approval card (the command, the diff, the environment), and the MCP call returns once you
approve or deny it. If you stop the calls from that session, they end, and a waiting
approval counts as denied. Nobody chats in that session: you talk to the outside agent in
its own window.

The session's mode follows the key; changing the mode in the session changes it until the
key is used again with its own mode.

## What the agent gets

The tools of the workspace, as opsdeck's own agent has them. A workspace only offers the
tools of what it has: no `database` tool without a Database resource, no `local_*` tools
unless agents may use this computer (App settings › This computer), and so on. It also
gets the tools of the MCP servers the workspace declares. Left out are the tools that only
make sense inside opsdeck: `delegate` (subagents need opsdeck's model), `ask_user` and
`todo` (the outside agent asks you and keeps its checklist itself).
[references/tools.md](../plugins/opsdeck/skills/opsdeck/references/tools.md) lists them.

## Security

- The endpoint listens on `127.0.0.1` only and refuses requests with a foreign `Host` or
  any `Origin` header, so web pages can't reach it, even with a key.
- Keys are random (192 bits), start with `odk_` so a leaked one is easy to find, and are
  stored as SHA-256 hashes in opsdeck's config folder (`access-keys.json`, readable only by
  you). The agent keeps its key in its own config file; treat that file like a password.
- The agent never gets a secret. Credentials are used by opsdeck; outputs go through the
  same redaction as the built-in agent's.
- Making, changing and revoking keys is recorded in the activity journal, and so is every
  call.
- Removing a workspace revokes its keys.

## Privacy

opsdeck runs on your computer. Its MCP server sends nothing anywhere; the tool results go
only to the agent you connected, and from there to whatever model that agent uses. That
agent's own privacy terms apply to what it sends to its model.

## Troubleshooting

| Message | What to do |
|---|---|
| *invalid or revoked opsdeck access key* | make a new key in App settings › Agent access |
| *opsdeck is not running at …* | open the opsdeck app or run `opsdeck serve` |
| *opsdeck is locked* | unlock opsdeck; the vault locks itself after idle time (App settings › Security) |
| *the workspace of this access key no longer exists* | the workspace was removed; make a key for another one |
| a call seems to hang | it waits for your approval: open the key's session on the Agent page |
