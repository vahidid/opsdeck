import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { KeyRound, Plug, Plus, Trash2 } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { AccessKey } from "../api/types";
import { useApp } from "../context/app";
import { timeAgo } from "../lib/format";
import { Modal, useConfirm, useToast } from "./overlay";
import { Badge, Button, CodeBlock, FormField, IconButton, Input, Segmented, Select, Tabs } from "./ui";

const MODES = [
  { id: "plan", label: "Read-only", help: "only reads; every change is refused" },
  { id: "ask", label: "Ask", help: "every change waits for your approval in opsdeck" },
  { id: "auto", label: "Auto", help: "what the workspace's permission rules allow runs; the rest asks" },
] as const;

const AGENTS = ["Claude Code", "Codex", "Cursor", "Gemini CLI", "VS Code", "Other"];

// setup returns the configuration for one agent, by its kind of config.
function setup(agent: string, name: string, endpoint: string, secret: string): { label: string; code: string; note?: string }[] {
  const header = `Authorization: Bearer ${secret}`;
  switch (agent) {
    case "Claude Code":
      return [
        { label: "HTTP", code: `claude mcp add --transport http ${name} ${endpoint} \\\n  --header "${header}"` },
        { label: "stdio", code: `claude mcp add ${name} --env OPSDECK_TOKEN=${secret} -- opsdeck mcp`, note: "Finds the running opsdeck on any port. Needs the opsdeck CLI on PATH." },
        { label: "Skill", code: "opsdeck skill install claude-code", note: "Or, inside Claude Code: /plugin marketplace add vahidid/opsdeck, then /plugin install opsdeck@opsdeck." },
      ];
    case "Codex":
      return [
        { label: "~/.codex/config.toml", code: `[mcp_servers.${name}]\ncommand = "opsdeck"\nargs = ["mcp"]\nenv = { OPSDECK_TOKEN = "${secret}" }` },
        { label: "Skill", code: "opsdeck skill install codex" },
      ];
    case "Cursor":
      return [
        { label: "~/.cursor/mcp.json", code: JSON.stringify({ mcpServers: { [name]: { url: endpoint, headers: { Authorization: `Bearer ${secret}` } } } }, null, 2) },
        { label: "Rule", code: "opsdeck skill install cursor", note: "Run it in your project: it adds .cursor/rules/opsdeck.mdc." },
      ];
    case "Gemini CLI":
      return [
        { label: "~/.gemini/settings.json", code: JSON.stringify({ mcpServers: { [name]: { httpUrl: endpoint, headers: { Authorization: `Bearer ${secret}` } } } }, null, 2) },
        { label: "Skill", code: "opsdeck skill install gemini" },
      ];
    case "VS Code":
      return [{ label: ".vscode/mcp.json", code: JSON.stringify({ servers: { [name]: { type: "http", url: endpoint, headers: { Authorization: `Bearer ${secret}` } } } }, null, 2) }];
    default:
      return [
        { label: "HTTP", code: `URL:    ${endpoint}\nHeader: ${header}`, note: "Streamable HTTP MCP. Any MCP client works." },
        { label: "stdio", code: `OPSDECK_TOKEN=${secret} opsdeck mcp` },
      ];
  }
}

// AgentAccessSection lets agents outside opsdeck (Claude Code, Codex,
// Cursor…) use a workspace through opsdeck's MCP server, one key each.
export function AgentAccessSection() {
  const { workspaces } = useApp();
  const toast = useToast();
  const confirm = useConfirm();
  const [keys, setKeys] = useState<AccessKey[] | null>(null);
  const [endpoint, setEndpoint] = useState("");
  const [open, setOpen] = useState(false);
  const [made, setMade] = useState<{ key: AccessKey; secret: string; agent: string } | null>(null);

  const load = () =>
    api
      .accessKeys()
      .then((r) => {
        setKeys(r.keys ?? []);
        setEndpoint(r.endpoint);
      })
      .catch(() => setKeys([]));
  useEffect(() => void load(), []);

  async function setMode(k: AccessKey, mode: string) {
    try {
      await api.updateAccessKey(k.id, { mode });
      await load();
    } catch (e) {
      toast.error("Cannot change the key", errMsg(e));
    }
  }

  async function revoke(k: AccessKey) {
    if (!(await confirm({ title: `Revoke the key of ${k.label}?`, message: "The agent using it is refused from now on. Its session and history stay.", danger: true, confirmLabel: "Revoke" }))) return;
    await api.revokeAccessKey(k.id);
    toast.success("Key revoked", k.label);
    await load();
  }

  return (
    <div className="space-y-3">
      <p className="text-xs text-muted">
        Use a workspace from Claude Code, Codex, Cursor, Gemini CLI or any MCP client. They get this workspace&apos;s tools; opsdeck keeps the credentials,
        and every call is checked, redacted and logged like the built-in agent&apos;s. Each key has a session on the Agent page, where its calls show up and
        wait for your approval. Keys are stored hashed; a key is shown once.
      </p>
      {keys === null ? null : keys.length === 0 ? (
        <div className="rounded-lg border border-dashed border-line-strong p-4 text-center text-xs text-faint">No agents connected yet.</div>
      ) : (
        <div className="rounded-lg border border-line divide-y divide-line">
          {keys.map((k) => (
            <div key={k.id} className="flex flex-wrap items-center gap-3 px-3 py-2.5">
              <span className="grid size-8 place-items-center rounded-lg bg-agent/15 text-agent-ink shrink-0">
                <Plug className="size-4" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 text-[13px] font-medium">
                  {k.label}
                  <Badge mono>{k.workspace}</Badge>
                </div>
                <div className="text-[11.5px] text-faint">
                  <span className="font-mono">{k.hint}…</span> · {k.lastUsed ? `used ${timeAgo(k.lastUsed)}` : "not used yet"} ·{" "}
                  <Link to={`/w/${encodeURIComponent(k.workspace)}/agent/mcp-${k.id}`} className="text-accent hover:underline">
                    its session
                  </Link>
                </div>
              </div>
              <Segmented value={k.mode} onChange={(m) => void setMode(k, m)} options={MODES.map((m) => ({ id: m.id, label: m.label, title: m.help }))} />
              <IconButton title="Revoke" onClick={() => void revoke(k)}>
                <Trash2 className="size-4" />
              </IconButton>
            </div>
          ))}
        </div>
      )}
      <div className="flex items-center gap-3">
        <Button variant="agentSoft" icon={<Plus className="size-4" />} onClick={() => setOpen(true)} disabled={!workspaces.length}>
          Connect an agent
        </Button>
        <span className="text-[11.5px] text-faint">
          Endpoint <span className="font-mono">{endpoint}</span> · see{" "}
          <a href="https://github.com/vahidid/opsdeck/blob/main/docs/agents.md" target="_blank" rel="noreferrer" className="text-accent hover:underline">
            docs/agents.md
          </a>
        </span>
      </div>
      <NewKeyModal
        open={open}
        onClose={() => setOpen(false)}
        onMade={(m) => {
          setOpen(false);
          setMade(m);
          void load();
        }}
      />
      {made && <SetupModal made={made} endpoint={endpoint} onClose={() => setMade(null)} />}
    </div>
  );
}

function NewKeyModal({ open, onClose, onMade }: { open: boolean; onClose: () => void; onMade: (m: { key: AccessKey; secret: string; agent: string }) => void }) {
  const { workspaces } = useApp();
  const toast = useToast();
  const [agent, setAgent] = useState(AGENTS[0]);
  const [label, setLabel] = useState("");
  const [ws, setWs] = useState("");
  const [mode, setMode] = useState("ask");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setAgent(AGENTS[0]);
      setLabel("");
      setWs(workspaces[0]?.name ?? "");
      setMode("ask");
    }
  }, [open, workspaces]);

  async function create() {
    setBusy(true);
    try {
      const r = await api.createAccessKey({ label: label.trim() || agent, workspace: ws, mode });
      onMade({ key: r.key, secret: r.secret, agent });
    } catch (e) {
      toast.error("Cannot make the key", errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Connect an agent"
      icon={<KeyRound />}
      width={520}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="agent" onClick={() => void create()} loading={busy} disabled={!ws}>
            Make the key
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <FormField label="Agent">
          <Select value={agent} onChange={(e) => setAgent(e.target.value)}>
            {AGENTS.map((a) => (
              <option key={a}>{a}</option>
            ))}
          </Select>
        </FormField>
        <FormField label="Label" help="Shown on its session and in the activity log, e.g. “Claude Code on my laptop”.">
          <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={agent} />
        </FormField>
        <FormField label="Workspace" help="A key reaches one workspace; make one per workspace.">
          <Select value={ws} onChange={(e) => setWs(e.target.value)}>
            {workspaces.map((w) => (
              <option key={w.name} value={w.name}>
                {w.title || w.name}
              </option>
            ))}
          </Select>
        </FormField>
        <div>
          <div className="text-xs font-medium text-muted mb-1.5">Mode</div>
          <Segmented value={mode} onChange={setMode} options={MODES.map((m) => ({ id: m.id, label: m.label }))} />
          <div className="mt-1.5 text-[11.5px] text-faint">
            {MODES.find((m) => m.id === mode)?.help}. Protected environments always ask.
          </div>
        </div>
      </div>
    </Modal>
  );
}

function SetupModal({ made, endpoint, onClose }: { made: { key: AccessKey; secret: string; agent: string }; endpoint: string; onClose: () => void }) {
  const name = `opsdeck-${made.key.workspace}`;
  const steps = setup(made.agent, name, endpoint, made.secret);
  const [tab, setTab] = useState(steps[0].label);
  const cur = steps.find((s) => s.label === tab) ?? steps[0];
  return (
    <Modal open onClose={onClose} title={`Set up ${made.key.label}`} icon={<Plug />} width={640} footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
      <div className="space-y-3">
        <div className="rounded-lg border border-warn/30 bg-warn/8 px-3 py-2 text-[12.5px]">
          This is the only time the key is shown. It gives {made.key.label} the tools of <b>{made.key.workspace}</b>; keep it like a password.
        </div>
        <CodeBlock>{made.secret}</CodeBlock>
        <Tabs value={tab} onChange={setTab} tabs={steps.map((s) => ({ id: s.label, label: s.label }))} />
        <CodeBlock>{cur.code}</CodeBlock>
        {cur.note && <p className="text-[11.5px] text-faint">{cur.note}</p>}
        <p className="text-[11.5px] text-faint">
          Its calls appear in the session “{made.key.label} (MCP)” on the Agent page of {made.key.workspace}; approve or deny them there.
        </p>
      </div>
    </Modal>
  );
}
