import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Bot, Download, Plus, Plug, Save, ScrollText, Settings, ShieldAlert, Trash2, Webhook } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { McpServer, PermissionRule, Workspace } from "../api/types";
import { useApp, useWs } from "../context/app";
import { Badge, Button, Card, ChipInput, FormField, IconButton, Input, PageHeader, Select, Textarea, Toggle, cx } from "../components/ui";
import { useConfirm, useToast } from "../components/overlay";
import { SecretRefPicker } from "../components/VaultItemForm";
import { HooksEditor } from "../components/HooksEditor";
import { useAsync } from "../hooks";
import { WORKSPACE_COLORS } from "../lib/format";
import { saveFile } from "../lib/desktop";

function normWorkspace(workspace: Workspace): Workspace {
  const st = workspace.settings ?? { defaultUser: "", defaultAuth: "", defaultModel: "" };
  return {
    ...structuredClone(workspace),
    environments: workspace.environments ?? [],
    tags: workspace.tags ?? [],
    settings: { ...st, web: st.web ?? "", webVia: st.webVia ?? "", models: st.models ?? [] },
    permissions: { rules: workspace.permissions?.rules ?? [] },
    mcpServers: workspace.mcpServers ?? [],
    hooks: (workspace.hooks ?? []).map((h) => ({ ...h, tool: h.tool ?? "", risk: h.risk ?? "", environment: h.environment ?? [] })),
    untrustedHooks: workspace.untrustedHooks ?? [],
  };
}

export function WorkspaceSettingsPage() {
  const { name: ws, workspace, reload } = useWs();
  const { reloadWorkspaces } = useApp();
  const [w, setW] = useState<Workspace | null>(null);
  const [saving, setSaving] = useState(false);
  const toast = useToast();
  const confirm = useConfirm();
  const navigate = useNavigate();
  const models = useAsync(() => api.models(), []);
  const mcp = useAsync(() => api.mcp(ws), [ws]);

  const harness = useAsync(() => api.harness(ws), [ws, workspace]);
  const { resources } = useWs();
  const networks = resources.filter((r) => ["WireGuard", "Tailscale", "NetBird", "OpenVPN", "OpenConnect", "OpenFortiVPN", "SocksProxy"].includes(r.kind));

  useEffect(() => {
    if (workspace) setW(normWorkspace(workspace));
  }, [workspace]);

  if (!w) return null;
  const dirty = !!workspace && JSON.stringify(w) !== JSON.stringify(normWorkspace(workspace));

  async function save() {
    if (!w) return;
    setSaving(true);
    try {
      await api.saveWorkspace(ws, w);
      await Promise.all([reload(), reloadWorkspaces()]);
      void mcp.reload();
      toast.success("Workspace saved");
    } catch (e) {
      toast.error("Save failed", errMsg(e));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    const purge = await confirm({
      title: `Delete workspace "${ws}"?`,
      message: (
        <>
          Removes the workspace definition and its local state (sessions, known hosts). <b>Its vault scope and all secrets in it are deleted too.</b>
        </>
      ),
      danger: true,
      confirmLabel: "Delete workspace",
      typeToConfirm: ws,
    });
    if (!purge) return;
    try {
      await api.deleteWorkspace(ws, true);
      await reloadWorkspaces();
      toast.success("Workspace deleted", ws);
      navigate("/");
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  const setRule = (i: number, r: Partial<PermissionRule>) => setW({ ...w, permissions: { rules: w.permissions.rules.map((x, j) => (j === i ? { ...x, ...r } : x)) } });
  const setMcp = (i: number, m: Partial<McpServer>) => setW({ ...w, mcpServers: w.mcpServers.map((x, j) => (j === i ? { ...x, ...m } : x)) });

  return (
    <div className="flex-1 overflow-y-auto">
      <PageHeader
        icon={<Settings />}
        title="Workspace settings"
        subtitle={<span className="font-mono text-xs">{w.path}</span>}
        actions={
          <Button variant="primary" icon={<Save className="size-4" />} onClick={save} loading={saving} disabled={!dirty}>
            Save changes
          </Button>
        }
      />
      <div className="px-5 pb-8 space-y-4 max-w-4xl">
        <Card title="General">
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Title">
              <Input value={w.title} onChange={(e) => setW({ ...w, title: e.target.value })} />
            </FormField>
            <FormField label="Name" help="Immutable identifier">
              <Input mono value={w.name} disabled />
            </FormField>
          </div>
          <FormField label="Description" className="mt-3">
            <Textarea value={w.description} onChange={(e) => setW({ ...w, description: e.target.value })} className="min-h-14" />
          </FormField>
          <div className="grid grid-cols-2 gap-3 mt-3">
            <div>
              <div className="text-xs font-medium text-muted mb-1.5">Color</div>
              <div className="flex gap-1.5 flex-wrap">
                {WORKSPACE_COLORS.map((c) => (
                  <button
                    key={c}
                    onClick={() => setW({ ...w, color: c })}
                    className={cx("size-6 rounded-md", w.color === c && "ring-2 ring-offset-2 ring-offset-panel ring-fg")}
                    style={{ background: c }}
                  />
                ))}
              </div>
            </div>
            <FormField label="Tags">
              <ChipInput value={w.tags} onChange={(t) => setW({ ...w, tags: t })} />
            </FormField>
          </div>
        </Card>

        <Card
          title="Environments"
          actions={
            <Button size="xs" variant="ghost" icon={<Plus className="size-3" />} onClick={() => setW({ ...w, environments: [...w.environments, { name: "", protected: false }] })}>
              Add
            </Button>
          }
        >
          <div className="text-xs text-muted mb-3">Protected environments always require approval for agent changes, and destructive actions there are blocked.</div>
          <div className="space-y-2">
            {w.environments.map((e, i) => (
              <div key={i} className="flex items-center gap-3">
                <Input
                  mono
                  value={e.name}
                  onChange={(ev) => setW({ ...w, environments: w.environments.map((x, j) => (j === i ? { ...x, name: ev.target.value } : x)) })}
                  placeholder="prod"
                  className="w-56"
                />
                <Toggle
                  checked={e.protected}
                  onChange={(v) => setW({ ...w, environments: w.environments.map((x, j) => (j === i ? { ...x, protected: v } : x)) })}
                  label={e.protected ? <span className="text-err">protected</span> : "protected"}
                />
                <div className="flex-1" />
                <IconButton title="Remove" onClick={() => setW({ ...w, environments: w.environments.filter((_, j) => j !== i) })}>
                  <Trash2 className="size-3.5" />
                </IconButton>
              </div>
            ))}
          </div>
        </Card>

        <Card title="Defaults">
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Default SSH user">
              <Input mono value={w.settings.defaultUser} onChange={(e) => setW({ ...w, settings: { ...w.settings, defaultUser: e.target.value } })} placeholder="deploy" />
            </FormField>
            <FormField label="Default agent model">
              <Select value={w.settings.defaultModel} onChange={(e) => setW({ ...w, settings: { ...w.settings, defaultModel: e.target.value } })} className="font-mono text-xs">
                <option value="">— app default —</option>
                {(models.data ?? []).map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.label || m.id}
                  </option>
                ))}
              </Select>
            </FormField>
          </div>
          <div className="mt-3">
            <div className="text-xs font-medium text-muted mb-1">Default credential (used by hosts without their own)</div>
            <SecretRefPicker value={w.settings.defaultAuth} onChange={(v) => setW({ ...w, settings: { ...w.settings, defaultAuth: v } })} ws={ws} itemTypes={["ssh-key", "login"]} />
          </div>
        </Card>

        <Card
          title={
            <>
              <Bot className="size-4 text-muted" /> Agent
            </>
          }
        >
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Web access" help="Search the web and read pages (vendor docs, CVEs, release notes). Default: App settings → Web access; off when only local models are allowed.">
              <Select value={w.settings.web ?? ""} onChange={(e) => setW({ ...w, settings: { ...w.settings, web: e.target.value as Workspace["settings"]["web"] } })}>
                <option value="">App default{harness.data ? ` (now ${harness.data.web === "fetch" ? "search & read" : harness.data.web})` : ""}</option>
                <option value="off">Off</option>
                <option value="search">Search only</option>
                <option value="fetch">Search & read pages</option>
              </Select>
            </FormField>
            <FormField label="Web requests through" help="One of this workspace's VPNs or proxies, e.g. where sites are only reachable from the office network.">
              <Select value={w.settings.webVia ?? ""} onChange={(e) => setW({ ...w, settings: { ...w.settings, webVia: e.target.value } })} disabled={w.settings.web === "off"}>
                <option value="">— direct (or the App settings proxy) —</option>
                {networks.map((n) => (
                  <option key={n.name} value={`network/${n.name}`}>
                    {n.name} ({n.kind})
                  </option>
                ))}
              </Select>
            </FormField>
          </div>
          <FormField label="Allowed models" className="mt-3" help="Sessions of this workspace may only use these (patterns like ollama/* or anthropic/claude-*). Only local models: nothing leaves this computer, and web access defaults to off.">
            <ChipInput mono value={w.settings.models ?? []} onChange={(v) => setW({ ...w, settings: { ...w.settings, models: v } })} placeholder="any model" />
          </FormField>
          {harness.data && (harness.data.subagents.length > 0 || harness.data.commands.length > 0) && (
            <div className="mt-3 grid grid-cols-2 gap-3">
              <div className="rounded-lg border border-line bg-panel-2 p-3">
                <div className="text-xs font-medium text-muted mb-1.5 flex items-center gap-1.5">
                  <Bot className="size-3.5" /> Subagents
                </div>
                {harness.data.subagents.map((a) => (
                  <div key={a.name} className="text-[12px] flex items-baseline gap-1.5">
                    <span className="font-mono font-medium shrink-0">{a.name}</span>
                    <span className={cx("text-[10.5px] whitespace-nowrap shrink-0", a.readOnly ? "text-info" : "text-warn")}>{a.readOnly ? "read-only" : "changes"}</span>
                    <span className="text-muted truncate min-w-0" title={a.description}>
                      {a.description}
                    </span>
                  </div>
                ))}
                <div className="text-[11px] text-faint mt-1.5">
                  Add your own as <span className="font-mono">agents/&lt;name&gt;.md</span> (Docs page).
                </div>
              </div>
              <div className="rounded-lg border border-line bg-panel-2 p-3">
                <div className="text-xs font-medium text-muted mb-1.5 flex items-center gap-1.5">
                  <ScrollText className="size-3.5" /> Slash commands
                </div>
                {harness.data.commands.length === 0 && <div className="text-[12px] text-faint">None yet.</div>}
                {harness.data.commands.map((c) => (
                  <div key={c.name} className="text-[12px] flex items-baseline gap-1.5">
                    <span className="font-mono font-medium shrink-0">/{c.name}</span>
                    {c.argumentHint && <span className="font-mono text-faint whitespace-nowrap shrink-0">{c.argumentHint}</span>}
                    <span className="text-muted truncate min-w-0" title={c.description}>
                      {c.description}
                    </span>
                  </div>
                ))}
                <div className="text-[11px] text-faint mt-1.5">
                  Prompt templates in <span className="font-mono">commands/&lt;name&gt;.md</span>; <span className="font-mono">$ARGUMENTS</span> is what follows the command.
                </div>
              </div>
            </div>
          )}
          <div className="border-t border-line mt-4 pt-4">
            <div className="text-[13px] font-medium mb-1 flex items-center gap-1.5">
              <Webhook className="size-3.5 text-muted" /> Workspace hooks
            </div>
            <div className="text-xs text-muted mb-3">
              Commands around the agent&apos;s tool calls, shared with the workspace (they are in <span className="font-mono">workspace.yaml</span>). Hooks that came with the
              folder — a clone, a pull — run only after you trust them on this machine; the ones you add here are trusted.
            </div>
            {(w.untrustedHooks ?? []).length > 0 && (
              <div className="mb-3 flex items-center gap-2 rounded-md border border-warn/40 bg-warn/10 p-2 text-[12px]">
                <span className="flex-1">
                  {w.untrustedHooks!.length > 1
                    ? `${w.untrustedHooks!.length} hooks came from the workspace files and do not run. Trusting them lets opsdeck run their commands on this machine.`
                    : "1 hook came from the workspace files and does not run. Trusting it lets opsdeck run its command on this machine."}
                </span>
                <Button
                  size="xs"
                  disabled={dirty}
                  title={dirty ? "Save your changes first" : undefined}
                  onClick={async () => {
                    try {
                      await api.trustHooks(ws);
                      await reload();
                      toast.success("Hooks trusted");
                    } catch (err) {
                      toast.error("Trust failed", errMsg(err));
                    }
                  }}
                >
                  Trust hooks
                </Button>
              </div>
            )}
            <HooksEditor hooks={w.hooks ?? []} onChange={(hooks) => setW({ ...w, hooks })} untrusted={w.untrustedHooks} environments={w.environments.map((e) => e.name)} />
          </div>
        </Card>

        <Card
          title={
            <>
              <ShieldAlert className="size-4 text-muted" /> Agent permission rules
            </>
          }
          actions={
            <Button size="xs" variant="ghost" icon={<Plus className="size-3" />} onClick={() => setW({ ...w, permissions: { rules: [...w.permissions.rules, { effect: "ask" }] } })}>
              Add rule
            </Button>
          }
        >
          <div className="text-xs text-muted mb-3">
            Evaluated top to bottom; the first matching rule wins. Without a match: reads are allowed, changes ask, destructive actions ask. <span className="font-mono">match</span> is a pattern
            (<span className="font-mono">*</span> = anything) checked against each command of a shell script (<span className="font-mono">exec</span>,{" "}
            <span className="font-mono">local_exec</span>): an <b>allow</b> rule covers a script only when every command in it that is not a read matches, so{" "}
            <span className="font-mono">systemctl restart nginx*</span> never allows <span className="font-mono">…; rm -rf …</span>. <b>ask</b> and <b>deny</b> apply when any command matches.
          </div>
          {w.permissions.rules.length === 0 && <div className="text-xs text-faint">No custom rules — defaults apply.</div>}
          <div className="space-y-2">
            {w.permissions.rules.map((r, i) => (
              <div key={i} className="grid grid-cols-[110px_130px_120px_1fr_1fr_auto] gap-2 items-center">
                <Select value={r.effect} onChange={(e) => setRule(i, { effect: e.target.value as PermissionRule["effect"] })} className={cx(r.effect === "deny" ? "text-err" : r.effect === "allow" ? "text-ok" : "text-warn")}>
                  <option value="allow">allow</option>
                  <option value="ask">ask</option>
                  <option value="deny">deny</option>
                </Select>
                <Select value={r.risk ?? ""} onChange={(e) => setRule(i, { risk: (e.target.value || undefined) as PermissionRule["risk"] })}>
                  <option value="">any risk</option>
                  <option value="read">read</option>
                  <option value="change">change</option>
                  <option value="destructive">destructive</option>
                </Select>
                <Input mono value={r.tool ?? ""} onChange={(e) => setRule(i, { tool: e.target.value || undefined })} placeholder="any tool" />
                <Select
                  value={r.environment?.[0] ?? ""}
                  onChange={(e) => setRule(i, { environment: e.target.value ? [e.target.value] : undefined })}
                >
                  <option value="">any environment</option>
                  {w.environments.map((e) => (
                    <option key={e.name} value={e.name}>
                      {e.name}
                    </option>
                  ))}
                </Select>
                <Input mono value={r.match ?? ""} onChange={(e) => setRule(i, { match: e.target.value || undefined })} placeholder="match e.g. iptables -F*" />
                <IconButton title="Remove rule" onClick={() => setW({ ...w, permissions: { rules: w.permissions.rules.filter((_, j) => j !== i) } })}>
                  <Trash2 className="size-3.5" />
                </IconButton>
              </div>
            ))}
          </div>
        </Card>

        <Card
          title={
            <>
              <Plug className="size-4 text-muted" /> MCP servers
            </>
          }
          actions={
            <Button size="xs" variant="ghost" icon={<Plus className="size-3" />} onClick={() => setW({ ...w, mcpServers: [...w.mcpServers, { name: "", command: "", args: [], env: {} }] })}>
              Add server
            </Button>
          }
        >
          <div className="text-xs text-muted mb-3">
            External tools for the agent in this workspace (stdio MCP servers). Environment values can be vault refs (<span className="font-mono">vault://…</span>); tools without a read-only hint
            are treated as changes.
          </div>
          <div className="space-y-3">
            {w.mcpServers.map((m, i) => {
              const status = mcp.data?.find((s) => s.name === m.name);
              return (
                <div key={i} className="rounded-lg border border-line bg-panel-2 p-3 space-y-2.5">
                  <div className="flex items-center gap-2">
                    <Input mono value={m.name} onChange={(e) => setMcp(i, { name: e.target.value })} placeholder="name" className="w-48" />
                    {status && (
                      <Badge tone={status.status === "running" || status.status === "ready" || status.status === "connected" ? "ok" : status.error ? "err" : "neutral"} title={status.error}>
                        {status.status}
                        {status.tools?.length ? ` · ${status.tools.length} tools` : ""}
                      </Badge>
                    )}
                    <div className="flex-1" />
                    <IconButton title="Remove" onClick={() => setW({ ...w, mcpServers: w.mcpServers.filter((_, j) => j !== i) })}>
                      <Trash2 className="size-3.5" />
                    </IconButton>
                  </div>
                  <div className="grid grid-cols-[1fr_2fr] gap-2">
                    <FormField label="Command">
                      <Input mono value={m.command} onChange={(e) => setMcp(i, { command: e.target.value })} placeholder="npx" />
                    </FormField>
                    <FormField label="Arguments">
                      <ChipInput mono value={m.args} onChange={(a) => setMcp(i, { args: a })} placeholder="-y @modelcontextprotocol/server-gitlab" />
                    </FormField>
                  </div>
                  <EnvEditor env={m.env ?? {}} onChange={(env) => setMcp(i, { env })} />
                  {status?.error && <div className="text-[11px] text-err">{status.error}</div>}
                  {status?.error?.includes("not trusted") && (
                    <div className="flex items-center gap-2 rounded-md border border-warn/40 bg-warn/10 p-2 text-[12px]">
                      <span className="flex-1">
                        This server came from the workspace files. Trusting it lets opsdeck run{" "}
                        <code className="font-mono">{[m.command, ...(m.args ?? [])].join(" ")}</code> on this machine.
                      </span>
                      <Button
                        size="xs"
                        onClick={async () => {
                          try {
                            await api.trustMcp(ws, m.name);
                            void mcp.reload();
                          } catch (err) {
                            toast.error("Trust failed", errMsg(err));
                          }
                        }}
                      >
                        Trust and start
                      </Button>
                    </div>
                  )}
                  {status?.tools && status.tools.length > 0 && (
                    <div className="flex flex-wrap gap-1">
                      {status.tools.map((t) => (
                        <Badge key={t.name} tone={t.readOnly ? "ok" : "neutral"} title={t.description} mono>
                          {t.name}
                        </Badge>
                      ))}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </Card>

        <ExportCard ws={ws} />

        <Card title={<span className="text-err">Danger zone</span>} className="border-err/30">
          <div className="flex items-center justify-between gap-4">
            <div className="text-xs text-muted">Delete this workspace, its local state and its vault scope. This cannot be undone.</div>
            <Button variant="danger" icon={<Trash2 className="size-3.5" />} onClick={remove}>
              Delete workspace
            </Button>
          </div>
        </Card>
      </div>
    </div>
  );
}

function EnvEditor({ env, onChange }: { env: Record<string, string>; onChange: (e: Record<string, string>) => void }) {
  const entries = Object.entries(env);
  const set = (list: [string, string][]) => onChange(Object.fromEntries(list));
  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <span className="text-xs font-medium text-muted">Environment</span>
        <button className="text-[11px] text-accent hover:underline" onClick={() => set([...entries, [`VAR_${entries.length + 1}`, ""]])}>
          + variable
        </button>
      </div>
      <div className="space-y-1.5">
        {entries.map(([k, v], i) => (
          <div key={i} className="grid grid-cols-[200px_1fr_auto] gap-2">
            <Input mono value={k} onChange={(e) => set(entries.map((x, j) => (j === i ? [e.target.value, x[1]] : x)))} />
            <Input mono value={v} onChange={(e) => set(entries.map((x, j) => (j === i ? [x[0], e.target.value] : x)))} placeholder="value or vault://global/gitlab-token" />
            <IconButton title="Remove" onClick={() => set(entries.filter((_, j) => j !== i))}>
              <Trash2 className="size-3.5" />
            </IconButton>
          </div>
        ))}
      </div>
    </div>
  );
}

function ExportCard({ ws }: { ws: string }) {
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const problem = password.length < 8 ? "At least 8 characters" : password !== repeat ? "The passwords do not match" : "";

  async function exportWs() {
    setBusy(true);
    try {
      const file = await api.exportWorkspace(ws, password);
      const day = new Date().toISOString().slice(0, 10);
      await saveFile(new Blob([JSON.stringify(file)], { type: "application/json" }), `${ws}-${day}.opsdeck`);
      setPassword("");
      setRepeat("");
    } catch (e) {
      toast.error("Export failed", errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card
      title={
        <>
          <Download className="size-4 text-muted" /> Export workspace
        </>
      }
    >
      <div className="text-xs text-muted mb-3">
        One file with this workspace's hosts and resources, docs, skills, scripts and the items of its vault scope, encrypted with the password below. Import it on another machine
        with <i>New / import workspace → Import export file</i>. Agent sessions, reports, artifacts, Git working copies and items of the global vault scope are not included. Anyone with
        the file and the password can read the secrets: when you hand it to someone, choose a password other than your master password.
      </div>
      <div className="grid grid-cols-[1fr_1fr_auto] gap-3 items-end">
        <FormField label="File password">
          <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </FormField>
        <FormField label="Repeat password">
          <Input type="password" autoComplete="new-password" value={repeat} onChange={(e) => setRepeat(e.target.value)} />
        </FormField>
        <Button icon={<Download className="size-3.5" />} onClick={exportWs} loading={busy} disabled={!!problem} title={problem}>
          Export
        </Button>
      </div>
    </Card>
  );
}
