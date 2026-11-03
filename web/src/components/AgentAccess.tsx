import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, Check, CheckCircle2, ChevronDown, ChevronRight, Eye, EyeOff, KeyRound, Plug, Plus, RefreshCw, Trash2, Unplug, Wrench } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { AccessKey, AgentClient, KeyIssued } from "../api/types";
import { useApp } from "../context/app";
import { timeAgo } from "../lib/format";
import { Modal, useConfirm, useToast } from "./overlay";
import { Badge, Button, CodeBlock, CopyButton, FormField, IconButton, Input, Segmented, Select, Tabs, Toggle, cx } from "./ui";

const MODES = [
  { id: "plan", label: "Read-only", help: "only reads; every change is refused" },
  { id: "ask", label: "Ask", help: "every change waits for your approval in opsdeck" },
  { id: "auto", label: "Auto", help: "what the workspace's permission rules allow runs; the rest asks" },
] as const;

/** id of the "set it up yourself" choice */
const MANUAL = "manual";

// setup returns the configuration for one agent (an AgentClient id), to set
// it up by hand. command is this opsdeck's executable, by its full path:
// agents started from a desktop menu do not have the shell's PATH.
function setup(agent: string, name: string, endpoint: string, secret: string, command: string): { label: string; code: string; note?: string }[] {
  const header = `Authorization: Bearer ${secret}`;
  const auth = { Authorization: `Bearer ${secret}` };
  const shellCmd = /[\s"'$]/.test(command) ? JSON.stringify(command) : command;
  const json = (v: unknown) => JSON.stringify(v, null, 2);
  switch (agent) {
    case "claude-desktop":
      return [
        {
          label: "claude_desktop_config.json",
          code: json({ mcpServers: { [name]: { command, args: ["mcp"], env: { OPSDECK_TOKEN: secret } } } }),
          note: "Settings › Developer › Edit Config (macOS: ~/Library/Application Support/Claude, Windows: %APPDATA%\\Claude, Linux: ~/.config/Claude), then restart Claude Desktop. opsdeck must be running.",
        },
      ];
    case "claude-code":
      return [
        { label: "HTTP", code: `claude mcp add --scope user --transport http ${name} ${endpoint} \\\n  --header "${header}"` },
        { label: "stdio", code: `claude mcp add --scope user ${name} --env OPSDECK_TOKEN=${secret} -- ${shellCmd} mcp`, note: "Finds the running opsdeck on any port." },
        { label: "Skill", code: "opsdeck skill install claude-code", note: "Or, inside Claude Code: /plugin marketplace add vahidid/opsdeck, then /plugin install opsdeck@opsdeck." },
      ];
    case "codex":
      return [
        { label: "~/.codex/config.toml", code: `[mcp_servers.${name}]\ncommand = ${JSON.stringify(command)}\nargs = ["mcp"]\nenv = { OPSDECK_TOKEN = "${secret}" }\ntool_timeout_sec = 3600` },
        { label: "Skill", code: "opsdeck skill install codex" },
      ];
    case "cursor":
      return [
        { label: "~/.cursor/mcp.json", code: json({ mcpServers: { [name]: { url: endpoint, headers: auth } } }) },
        { label: "Rule", code: "opsdeck skill install cursor", note: "Run it in your project: it adds .cursor/rules/opsdeck.mdc." },
      ];
    case "gemini-cli":
      return [
        { label: "~/.gemini/settings.json", code: json({ mcpServers: { [name]: { httpUrl: endpoint, headers: auth, timeout: 3600000 } } }) },
        { label: "Skill", code: "opsdeck skill install gemini" },
      ];
    case "antigravity-cli":
      return [
        { label: "agy", code: `agy mcp add --type http --header "${header}" ${name} ${endpoint}`, note: "Options go before the name." },
        { label: "~/.gemini/config/mcp_config.json", code: json({ mcpServers: { [name]: { serverUrl: endpoint, headers: auth } } }) },
        { label: "Skill", code: "opsdeck skill install antigravity" },
      ];
    case "antigravity":
      return [
        {
          label: "mcp_config.json",
          code: json({ mcpServers: { [name]: { serverUrl: endpoint, headers: auth } } }),
          note: "Agent panel › … › MCP Servers › Manage › View raw config (~/.gemini/antigravity/mcp_config.json), then Refresh.",
        },
      ];
    case "windsurf":
      return [{ label: "~/.codeium/windsurf/mcp_config.json", code: json({ mcpServers: { [name]: { serverUrl: endpoint, headers: auth } } }) }];
    case "vscode":
      return [{ label: "mcp.json", code: json({ servers: { [name]: { type: "http", url: endpoint, headers: auth } } }), note: "MCP: Open User Configuration, or .vscode/mcp.json in a project." }];
    default:
      return [
        { label: "HTTP", code: `URL:    ${endpoint}\nHeader: ${header}`, note: "Streamable HTTP MCP. Any MCP client works." },
        { label: "stdio", code: `OPSDECK_TOKEN=${secret} ${shellCmd} mcp` },
      ];
  }
}

/** initials for an agent tile */
function mark(name: string) {
  return name
    .split(/\s+/)
    .map((w) => w[0])
    .join("")
    .slice(0, 2)
    .toUpperCase();
}

// AgentAccessSection lets agents outside opsdeck (Claude Code, Codex,
// Cursor, Antigravity…) use a workspace through opsdeck's MCP server, one
// key each. opsdeck writes the key into the agent's own config, so
// connecting is a click; rotating rewrites it there.
export function AgentAccessSection() {
  const { workspaces } = useApp();
  const toast = useToast();
  const confirm = useConfirm();
  const [keys, setKeys] = useState<AccessKey[] | null>(null);
  const [clients, setClients] = useState<AgentClient[]>([]);
  const [endpoint, setEndpoint] = useState("");
  const [command, setCommand] = useState("opsdeck");
  const [open, setOpen] = useState(false);
  const [issued, setIssued] = useState<{ issued: KeyIssued; client: string; rotated?: boolean } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = () =>
    api
      .accessKeys()
      .then((r) => {
        setKeys(r.keys ?? []);
        setEndpoint(r.endpoint);
        if (r.command) setCommand(r.command);
      })
      .catch(() => setKeys([]));
  const loadClients = () =>
    api
      .agentClients()
      .then((r) => setClients(r.clients ?? []))
      .catch(() => setClients([]));
  useEffect(() => {
    void load();
    void loadClients();
  }, []);

  const clientName = (id?: string) => clients.find((c) => c.id === id)?.name ?? id ?? "";

  async function setMode(k: AccessKey, mode: string) {
    try {
      await api.updateAccessKey(k.id, { mode });
      await load();
    } catch (e) {
      toast.error("Cannot change the key", errMsg(e));
    }
  }

  async function rotate(k: AccessKey, relink?: string) {
    const target = relink ?? k.client;
    const where = target ? clientName(target) : "";
    if (
      !relink &&
      !(await confirm({
        title: `Rotate the key of ${k.label}?`,
        message: target
          ? `The old key stops working at once. opsdeck writes the new one into ${where}'s config for you.`
          : "The old key stops working at once. You get the new key to paste into the agent's config.",
        confirmLabel: "Rotate",
      }))
    )
      return;
    setBusy(k.id);
    try {
      const r = await api.rotateAccessKey(k.id, relink ? { client: relink, skill: false } : {});
      await load();
      if (r.link) toast.success(relink ? `${where} is connected again` : "Key rotated", r.link.config);
      else if (r.linkError) toast.warning(`The key was rotated but ${where}'s config was not updated`, r.linkError);
      setIssued({ issued: r, client: target || MANUAL, rotated: true });
    } catch (e) {
      toast.error("Cannot rotate the key", errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  async function revoke(k: AccessKey) {
    const linked = k.client && k.linked;
    if (
      !(await confirm({
        title: linked ? `Disconnect ${k.label}?` : `Revoke the key of ${k.label}?`,
        message: linked
          ? `opsdeck revokes the key and removes "${k.server}" from ${clientName(k.client)}'s config. Its session and history stay.`
          : "The agent using it is refused from now on. Its session and history stay.",
        danger: true,
        confirmLabel: linked ? "Disconnect" : "Revoke",
      }))
    )
      return;
    try {
      const r = await api.revokeAccessKey(k.id);
      if (r.unlinkError) toast.warning("Key revoked; the agent's config was not changed", r.unlinkError);
      else toast.success(linked ? "Disconnected" : "Key revoked", k.label);
    } catch (e) {
      toast.error("Cannot revoke the key", errMsg(e));
    }
    await load();
  }

  return (
    <div className="space-y-3">
      <p className="text-xs text-muted">
        Use a workspace from Claude Code, Codex, Cursor, Gemini CLI, Antigravity or any MCP client. opsdeck writes the connection into the agent&apos;s own
        config; the agent gets this workspace&apos;s tools while opsdeck keeps the credentials, and every call is checked, redacted and logged like the
        built-in agent&apos;s. Each key has a session on the Agent page, where its calls show up and wait for your approval.
      </p>
      {keys === null ? null : keys.length === 0 ? (
        <div className="rounded-lg border border-dashed border-line-strong p-4 text-center text-xs text-faint">No agents connected yet.</div>
      ) : (
        <div className="rounded-lg border border-line divide-y divide-line">
          {keys.map((k) => (
            <div key={k.id} className="flex flex-wrap items-center gap-3 px-3 py-2.5">
              <span className="grid size-8 place-items-center rounded-lg bg-agent/15 text-agent-ink shrink-0 text-[11px] font-semibold">
                {k.client ? mark(clientName(k.client)) : <Plug className="size-4" />}
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2 text-[13px] font-medium">
                  {k.label}
                  <Badge mono>{k.workspace}</Badge>
                  {k.client ? (
                    k.linked ? (
                      <Badge tone="ok">
                        <Check className="size-3" /> in {clientName(k.client)}
                      </Badge>
                    ) : (
                      <Badge tone="warn" title={`"${k.server}" is no longer in ${clientName(k.client)}'s config`}>
                        <AlertTriangle className="size-3" /> missing from {clientName(k.client)}
                      </Badge>
                    )
                  ) : (
                    <Badge>set up by hand</Badge>
                  )}
                </div>
                <div className="text-[11.5px] text-faint">
                  <span className="font-mono">{k.hint}…</span> · {k.lastUsed ? `used ${timeAgo(k.lastUsed)}` : "not used yet"}
                  {k.rotated && <> · rotated {timeAgo(k.rotated)}</>} ·{" "}
                  <Link to={`/w/${encodeURIComponent(k.workspace)}/agent/mcp-${k.id}`} className="text-accent hover:underline">
                    its session
                  </Link>
                </div>
              </div>
              <Segmented value={k.mode} onChange={(m) => void setMode(k, m)} options={MODES.map((m) => ({ id: m.id, label: m.label, title: m.help }))} />
              {k.client && !k.linked && (
                <Button size="sm" variant="agentSoft" icon={<Wrench className="size-3.5" />} loading={busy === k.id} onClick={() => void rotate(k, k.client)}>
                  Repair
                </Button>
              )}
              <IconButton title={k.client ? "Rotate the key (opsdeck updates the agent's config)" : "Rotate the key and show the new one"} onClick={() => void rotate(k)} disabled={busy === k.id}>
                <RefreshCw className={cx("size-4", busy === k.id && "animate-spin")} />
              </IconButton>
              <IconButton title={k.client && k.linked ? "Disconnect" : "Revoke"} onClick={() => void revoke(k)}>
                {k.client && k.linked ? <Unplug className="size-4" /> : <Trash2 className="size-4" />}
              </IconButton>
            </div>
          ))}
        </div>
      )}
      <div className="flex items-center gap-3">
        <Button
          variant="agentSoft"
          icon={<Plus className="size-4" />}
          onClick={() => {
            void loadClients();
            setOpen(true);
          }}
          disabled={!workspaces.length}
        >
          Connect an agent
        </Button>
        <span className="text-[11.5px] text-faint">
          Endpoint <span className="font-mono">{endpoint}</span> · see{" "}
          <a href="https://github.com/vahidid/opsdeck/blob/main/docs/agents.md" target="_blank" rel="noreferrer" className="text-accent hover:underline">
            docs/agents.md
          </a>
        </span>
      </div>
      <ConnectModal
        open={open}
        clients={clients}
        onClose={() => setOpen(false)}
        onDone={(r, client) => {
          setOpen(false);
          setIssued({ issued: r, client });
          void load();
        }}
      />
      {issued && <IssuedModal issued={issued.issued} client={issued.client} clientName={clientName(issued.client)} rotated={issued.rotated} endpoint={endpoint} command={command} onClose={() => setIssued(null)} />}
    </div>
  );
}

function ConnectModal({ open, clients, onClose, onDone }: { open: boolean; clients: AgentClient[]; onClose: () => void; onDone: (r: KeyIssued, client: string) => void }) {
  const { workspaces } = useApp();
  const toast = useToast();
  const sorted = useMemo(() => [...clients].sort((a, b) => Number(b.detected) - Number(a.detected)), [clients]);
  const [client, setClient] = useState("");
  const [label, setLabel] = useState("");
  const [ws, setWs] = useState("");
  const [mode, setMode] = useState("ask");
  const [skill, setSkill] = useState(true);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setClient(sorted.find((c) => c.detected)?.id ?? sorted[0]?.id ?? MANUAL);
      setLabel("");
      setWs(workspaces[0]?.name ?? "");
      setMode("ask");
      setSkill(true);
    }
  }, [open, workspaces, sorted]);

  const cur = clients.find((c) => c.id === client);
  const name = cur?.name ?? "Other agent";

  async function connect() {
    setBusy(true);
    try {
      const r = await api.createAccessKey({ label: label.trim() || name, workspace: ws, mode, client: client === MANUAL ? undefined : client, skill: !!cur?.skill && skill });
      onDone(r, client);
    } catch (e) {
      toast.error("Cannot connect", errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Connect an agent"
      icon={<Plug />}
      width={620}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="agent" icon={client === MANUAL ? <KeyRound className="size-4" /> : <Plug className="size-4" />} onClick={() => void connect()} loading={busy} disabled={!ws}>
            {client === MANUAL ? "Make the key" : `Connect ${name}`}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <div>
          <div className="text-xs font-medium text-muted mb-1.5">Agent</div>
          <div className="grid grid-cols-2 sm:grid-cols-3 gap-2">
            {sorted.map((c) => (
              <AgentTile key={c.id} active={client === c.id} onClick={() => setClient(c.id)} title={c.name} sub={c.detected ? "Found on this computer" : "Not found"} found={c.detected} />
            ))}
            <AgentTile active={client === MANUAL} onClick={() => setClient(MANUAL)} title="Other / by hand" sub="Copy the key and the setup" />
          </div>
          {cur && (
            <div className="mt-2 text-[11.5px] text-faint">
              opsdeck adds <span className="font-mono">opsdeck-{ws || "…"}</span> to <span className="font-mono break-all">{cur.config}</span>
              {!cur.detected && cur.hint && (
                <>
                  {" "}
                  · not installed? <span className="font-mono">{cur.hint}</span>
                </>
              )}
            </div>
          )}
        </div>
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Workspace" help="A key reaches one workspace.">
            <Select value={ws} onChange={(e) => setWs(e.target.value)}>
              {workspaces.map((w) => (
                <option key={w.name} value={w.name}>
                  {w.title || w.name}
                </option>
              ))}
            </Select>
          </FormField>
          <FormField label="Label" help="Its session and the activity log show it.">
            <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={name} />
          </FormField>
        </div>
        <div>
          <div className="text-xs font-medium text-muted mb-1.5">Mode</div>
          <Segmented value={mode} onChange={setMode} options={MODES.map((m) => ({ id: m.id, label: m.label }))} />
          <div className="mt-1.5 text-[11.5px] text-faint">{MODES.find((m) => m.id === mode)?.help}. Protected environments always ask.</div>
        </div>
        {cur?.skill && <Toggle checked={skill} onChange={setSkill} label={<span>Also install the opsdeck skill (how to work through opsdeck: look first, expect approvals, leave a report)</span>} />}
      </div>
    </Modal>
  );
}

function AgentTile({ active, onClick, title, sub, found }: { active: boolean; onClick: () => void; title: string; sub: string; found?: boolean }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cx(
        "flex items-center gap-2.5 rounded-lg border px-2.5 py-2 text-left transition-colors",
        active ? "border-agent bg-agent/10" : "border-line hover:border-line-strong hover:bg-panel-2",
      )}
    >
      <span className={cx("grid size-7 place-items-center rounded-md text-[10.5px] font-semibold shrink-0", found ? "bg-agent/15 text-agent-ink" : "bg-panel-3 text-faint")}>{mark(title)}</span>
      <span className="min-w-0">
        <span className="block text-[12.5px] font-medium truncate">{title}</span>
        <span className={cx("block text-[10.5px] truncate", found ? "text-ok" : "text-faint")}>{sub}</span>
      </span>
    </button>
  );
}

// IssuedModal shows the outcome of connecting or rotating: what opsdeck
// changed in the agent's config, and the key itself (hidden until asked
// for: it is shown this once).
function IssuedModal({
  issued,
  client,
  clientName,
  rotated,
  endpoint,
  command,
  onClose,
}: {
  issued: KeyIssued;
  client: string;
  clientName: string;
  rotated?: boolean;
  endpoint: string;
  command: string;
  onClose: () => void;
}) {
  const { key, secret, link, linkError } = issued;
  const name = link?.server ?? `opsdeck-${key.workspace}`;
  const steps = setup(client, name, endpoint, secret, command);
  const manual = client === MANUAL || !!linkError;
  const [show, setShow] = useState(manual);
  const [more, setMore] = useState(manual);
  const [tab, setTab] = useState(steps[0].label);
  const cur = steps.find((s) => s.label === tab) ?? steps[0];
  return (
    <Modal
      open
      onClose={onClose}
      title={link ? `${clientName} is connected` : rotated ? `New key for ${key.label}` : `Set up ${key.label}`}
      icon={link ? <CheckCircle2 /> : <KeyRound />}
      width={640}
      footer={
        <Button variant="primary" onClick={onClose}>
          Done
        </Button>
      }
    >
      <div className="space-y-3">
        {link && (
          <div className="rounded-lg border border-ok/30 bg-ok/8 px-3 py-2.5 text-[12.5px] space-y-1">
            <div>
              <b>{name}</b> is in <span className="font-mono break-all">{link.config}</span>, with {rotated ? "the new" : "its"} key. It reaches <b>{key.workspace}</b> in{" "}
              {MODES.find((m) => m.id === key.mode)?.label ?? key.mode} mode.
            </div>
            {link.skill && (
              <div className="text-muted">
                The opsdeck skill is in <span className="font-mono break-all">{link.skill}</span>.
              </div>
            )}
            {link.skillError && <div className="text-warn">The skill was not installed: {link.skillError}</div>}
            {link.note && <div className="text-muted">{link.note}</div>}
          </div>
        )}
        {linkError && (
          <div className="rounded-lg border border-warn/30 bg-warn/8 px-3 py-2 text-[12.5px]">
            opsdeck could not change {clientName}&apos;s config: {linkError}. Set it up by hand below.
          </div>
        )}
        <div className="rounded-lg border border-line px-3 py-2">
          <div className="flex items-center gap-2">
            <KeyRound className="size-3.5 text-muted" />
            <span className="text-[12.5px] font-medium flex-1">Access key</span>
            <CopyButton text={secret} label="Copy" />
            <Button size="xs" variant="ghost" icon={show ? <EyeOff className="size-3" /> : <Eye className="size-3" />} onClick={() => setShow((v) => !v)}>
              {show ? "Hide" : "Show"}
            </Button>
          </div>
          <div className="mt-1.5 font-mono text-[11.5px] break-all">{show ? secret : `${key.hint}${"•".repeat(24)}`}</div>
          <div className="mt-1 text-[11px] text-faint">
            opsdeck keeps only a hash, so this is the only time it is shown. {link ? "You don't need it: the agent has it already. " : ""}Lost it? Rotate the key for a
            new one.
          </div>
        </div>
        <button type="button" className="flex items-center gap-1 text-[12px] text-muted hover:text-fg" onClick={() => setMore((v) => !v)}>
          {more ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />} Manual setup
        </button>
        {more && (
          <div className="space-y-2">
            <Tabs value={tab} onChange={setTab} tabs={steps.map((s) => ({ id: s.label, label: s.label }))} />
            <CodeBlock>{cur.code}</CodeBlock>
            {cur.note && <p className="text-[11.5px] text-faint">{cur.note}</p>}
          </div>
        )}
        <p className="text-[11.5px] text-faint">
          Its calls appear in the session “{key.label} (MCP)” on the Agent page of {key.workspace}. When one waits for your approval, opsdeck tells you wherever you are.
        </p>
      </div>
    </Modal>
  );
}
