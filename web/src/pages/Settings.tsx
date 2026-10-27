import { useEffect, useRef, useState } from "react";
import { Bot, Cpu, Globe, KeyRound, Laptop, MessageSquareText, Moon, Plug, Plus, RefreshCw, Save, Settings as SettingsIcon, ShieldCheck, SlidersHorizontal, Sparkles, Sun, Trash2, Webhook } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { AgentKind, InstalledAgent, Item, ProviderConfig, Settings, VpnCodes, WebSettings } from "../api/types";
import { isAgentKind } from "../api/types";
import { useApp } from "../context/app";
import { useAppearance } from "../context/appearance";
import { useUpdate } from "../context/update";
import { Badge, Button, Card, ChipInput, CopyButton, ErrorBox, FormField, IconButton, Input, Loading, PageHeader, Segmented, Select, Toggle, cx } from "../components/ui";
import { useConfirm, useToast } from "../components/overlay";
import { VaultItemModal } from "../components/VaultItemForm";
import { ModelChooser } from "../components/ModelChooser";
import { HooksEditor } from "../components/HooksEditor";
import { AgentAccessSection } from "../components/AgentAccess";
import { passwordStrength } from "../lib/format";

const PRESETS: { label: string; type: ProviderConfig["type"]; id: string; baseUrl: string; models: string[]; needsKey: boolean }[] = [
  { label: "Anthropic", type: "anthropic", id: "anthropic", baseUrl: "https://api.anthropic.com", models: ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"], needsKey: true },
  { label: "OpenAI", type: "openai", id: "openai", baseUrl: "https://api.openai.com/v1", models: [], needsKey: true },
  { label: "OpenRouter", type: "openai", id: "openrouter", baseUrl: "https://openrouter.ai/api/v1", models: [], needsKey: true },
  { label: "Ollama (local)", type: "openai", id: "ollama", baseUrl: "http://localhost:11434/v1", models: ["llama3.1", "qwen2.5-coder"], needsKey: false },
  { label: "LM Studio (local)", type: "openai", id: "lmstudio", baseUrl: "http://localhost:1234/v1", models: [], needsKey: false },
];

const AGENT_PRESETS: { kind: AgentKind; label: string; id: string; command: string; args: string[]; models: string[]; login: string }[] = [
  { kind: "claude-code", label: "Claude Code", id: "claude-code", command: "claude", args: [], models: ["default"], login: "Uses your own Claude Code login (subscription or API key). Run `claude` once in a terminal and sign in." },
  { kind: "codex", label: "Codex (ChatGPT)", id: "codex", command: "codex", args: [], models: ["default"], login: "Uses your ChatGPT account through the official Codex CLI. Run `codex login` once." },
  { kind: "gemini-cli", label: "Gemini CLI", id: "gemini", command: "gemini", args: [], models: ["default"], login: "Uses your Google account through Gemini CLI. Run `gemini` once and sign in." },
  { kind: "acp", label: "Other ACP agent (opencode, Qwen Code…)", id: "acp-agent", command: "", args: [], models: ["default"], login: "Any agent that speaks the Agent Client Protocol, e.g. `opencode acp`." },
];

const NO_LOCAL: Settings["localAccess"] = { disabled: false, allowedDirs: [], autoReadCommands: false };
const normSettings = (v: Settings): Settings => ({
  ...v,
  vpn: {
    elevate: v.vpn?.elevate || "auto",
    browser: v.vpn?.browser ?? "",
    codes: {
      ntfyUrl: v.vpn?.codes?.ntfyUrl ?? "",
      ntfyTokenRef: v.vpn?.codes?.ntfyTokenRef ?? "",
      command: v.vpn?.codes?.command ?? "",
      pattern: v.vpn?.codes?.pattern ?? "",
    },
  },
  git: { name: v.git?.name ?? "", email: v.git?.email ?? "" },
  providers: v.providers ?? [],
  localAccess: { ...NO_LOCAL, ...(v.localAccess ?? {}), allowedDirs: v.localAccess?.allowedDirs ?? [] },
  agent: {
    autoServerChanges: !!v.agent?.autoServerChanges,
    subagentModel: v.agent?.subagentModel ?? "",
    noSubagents: !!v.agent?.noSubagents,
    hooks: (v.agent?.hooks ?? []).map((h) => ({ ...h, tool: h.tool ?? "", risk: h.risk ?? "", environment: h.environment ?? [], workspace: h.workspace ?? "" })),
  },
  web: {
    access: v.web?.access ?? "",
    search: v.web?.search ?? "",
    searchUrl: v.web?.searchUrl ?? "",
    keyRef: v.web?.keyRef ?? "",
    proxy: v.web?.proxy ?? "",
    domains: v.web?.domains ?? [],
    noDefaultDomains: !!v.web?.noDefaultDomains,
  },
  updates: { noCheck: !!v.updates?.noCheck },
});

const KIND_LABEL: Record<string, string> = {
  anthropic: "Anthropic API",
  openai: "OpenAI-compatible API",
  "claude-code": "Local agent · Claude Code",
  codex: "Local agent · Codex",
  "gemini-cli": "Local agent · Gemini CLI",
  acp: "Local agent · ACP",
};

export function AppSettingsPage() {
  const { setCustomizeOpen } = useAppearance();
  const [s, setS] = useState<Settings | null>(null);
  const [orig, setOrig] = useState<string>("");
  const [err, setErr] = useState("");
  const [saving, setSaving] = useState(false);
  const [tokens, setTokens] = useState<Item[]>([]);
  const [newKeyFor, setNewKeyFor] = useState<number | "web" | null>(null);
  const [installed, setInstalled] = useState<InstalledAgent[] | null>(null);
  // providers added in this visit whose models are fetched right away
  const autoLoad = useRef(new Set<string>());
  const { theme, setTheme, workspaces } = useApp();
  const toast = useToast();
  const confirm = useConfirm();

  const loadTokens = () =>
    api
      .items("global")
      .then((l) => setTokens(l.filter((i) => i.type === "token")))
      .catch(() => {});

  useEffect(() => {
    api
      .settings()
      .then((v) => {
        const norm = normSettings(v);
        setS(norm);
        setOrig(JSON.stringify(norm));
      })
      .catch((e) => setErr(errMsg(e)));
    void loadTokens();
    api
      .detectAgents()
      .then(setInstalled)
      .catch(() => setInstalled([]));
  }, []);

  if (err && !s) return <ErrorBox error={err} />;
  if (!s) return <Loading />;
  const dirty = JSON.stringify(s) !== orig;

  // functional: model lists load in the background and land after other edits
  const setProvider = (i: number, p: Partial<ProviderConfig>) =>
    setS((cur) => (cur ? { ...cur, providers: cur.providers.map((x, j) => (j === i ? { ...x, ...p } : x)) } : cur));
  const addPreset = (idx: number) => {
    const pr = PRESETS[idx];
    let id = pr.id;
    let n = 2;
    while (s.providers.some((p) => p.id === id)) id = `${pr.id}-${n++}`;
    if (!pr.needsKey) autoLoad.current.add(id);
    setS({ ...s, providers: [...s.providers, { id, type: pr.type, name: pr.label, baseUrl: pr.baseUrl, apiKeyRef: "", models: pr.models }] });
  };
  const uniqueId = (base: string) => {
    let id = base;
    let n = 2;
    while (s.providers.some((p) => p.id === id)) id = `${base}-${n++}`;
    return id;
  };
  const addAgent = (a: { kind: AgentKind; label: string; id: string; command: string; args: string[]; models: string[] }) => {
    const id = uniqueId(a.id);
    // ask the CLI for its models unless it is an ACP agent without a command yet
    if (a.command || a.kind !== "acp") autoLoad.current.add(id);
    setS({ ...s, providers: [...s.providers, { id, type: a.kind, name: a.label, baseUrl: "", apiKeyRef: "", models: a.models, command: a.command, args: a.args, env: {} }] });
  };
  const allModels = s.providers.flatMap((p) => p.models.map((m) => ({ id: `${p.id}/${m}`, label: `${p.name} · ${p.modelNames?.[m] || m}` })));

  async function save() {
    if (!s) return;
    setSaving(true);
    try {
      const v = await api.saveSettings(s);
      const norm = normSettings(v);
      setS(norm);
      setOrig(JSON.stringify(norm));
      toast.success("Settings saved");
    } catch (e) {
      toast.error("Save failed", errMsg(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex-1 overflow-y-auto">
      <PageHeader
        icon={<SettingsIcon />}
        title="App settings"
        subtitle="Model providers, security and appearance. Applies to all workspaces."
        actions={
          <Button variant="primary" icon={<Save className="size-4" />} onClick={save} loading={saving} disabled={!dirty}>
            Save changes
          </Button>
        }
      />
      <div className="px-5 pb-8 space-y-4 max-w-4xl">
        <Card
          title={
            <>
              <Cpu className="size-4 text-muted" /> Model providers
            </>
          }
          actions={
            <Select
              value=""
              onChange={(e) => {
                if (e.target.value !== "") addPreset(Number(e.target.value));
              }}
              className="h-7 text-xs w-44"
            >
              <option value="">+ Add API provider…</option>
              {PRESETS.map((p, i) => (
                <option key={p.id} value={i}>
                  {p.label}
                </option>
              ))}
            </Select>
          }
        >
          <div className="mb-4 rounded-lg border border-line bg-panel-2 p-3">
            <div className="flex items-center gap-2 mb-1">
              <Bot className="size-4 text-muted" />
              <span className="font-medium text-[13px]">Local agents</span>
              <span className="text-[11px] text-faint">use the AI tools you already have installed and logged in — no API key</span>
            </div>
            <p className="text-[12px] text-muted mb-2">
              opsdeck runs the agent on this machine with your own login, turns off its built-in tools and gives it only this workspace&apos;s opsdeck tools — so
              approvals, secret redaction and the audit log still apply. opsdeck never reads your login.
            </p>
            {installed === null && <div className="text-[12px] text-faint">Looking for installed agents…</div>}
            {installed && installed.length > 0 && (
              <div className="flex flex-wrap gap-2 mb-2">
                {installed.map((a) => (
                  <Button
                    key={a.command}
                    size="xs"
                    icon={<Plus className="size-3" />}
                    onClick={() => addAgent({ kind: a.kind, label: a.name, id: a.command, command: a.command, args: a.args ?? [], models: a.models })}
                  >
                    {a.name} <span className="text-faint font-mono ml-1">{a.version}</span>
                  </Button>
                ))}
              </div>
            )}
            {installed && installed.length === 0 && <div className="text-[12px] text-faint mb-2">No agent CLI found on PATH. You can still add one and set its command.</div>}
            <Select
              value=""
              onChange={(e) => {
                if (e.target.value !== "") addAgent(AGENT_PRESETS[Number(e.target.value)]);
              }}
              className="h-7 text-xs w-64"
            >
              <option value="">+ Add local agent manually…</option>
              {AGENT_PRESETS.map((a, i) => (
                <option key={a.kind} value={i}>
                  {a.label}
                </option>
              ))}
            </Select>
          </div>
          {s.providers.length === 0 && (
            <div className="text-muted text-[13px] py-3">
              No providers yet. The agent needs at least one. Use <span className="font-medium">Ollama</span> or <span className="font-medium">LM Studio</span> to keep everything on this
              machine.
            </div>
          )}
          <div className="space-y-3">
            {s.providers.map((p, i) => (
              <div key={i} className="rounded-lg border border-line bg-panel-2 p-3 space-y-3">
                <div className="flex items-center gap-2">
                  <Badge tone={p.type === "anthropic" ? "accent" : isAgentKind(p.type) ? "ok" : "info"}>{KIND_LABEL[p.type] ?? p.type}</Badge>
                  <span className="font-medium">{p.name}</span>
                  <div className="flex-1" />
                  <IconButton
                    title="Remove provider"
                    onClick={async () => {
                      if (await confirm({ title: `Remove ${p.name}?`, danger: true, confirmLabel: "Remove" })) setS({ ...s, providers: s.providers.filter((_, j) => j !== i) });
                    }}
                  >
                    <Trash2 className="size-3.5" />
                  </IconButton>
                </div>
                <div className="grid grid-cols-3 gap-3">
                  <FormField label="ID" help="Prefix for model ids">
                    <Input mono value={p.id} onChange={(e) => setProvider(i, { id: e.target.value.replace(/[^a-z0-9-]/gi, "").toLowerCase() })} />
                  </FormField>
                  <FormField label="Name">
                    <Input value={p.name} onChange={(e) => setProvider(i, { name: e.target.value })} />
                  </FormField>
                  <FormField label="Type">
                    <Select value={p.type} onChange={(e) => setProvider(i, { type: e.target.value as ProviderConfig["type"] })}>
                      <option value="anthropic">Anthropic API</option>
                      <option value="openai">OpenAI-compatible API</option>
                      <option value="claude-code">Claude Code (local)</option>
                      <option value="codex">Codex / ChatGPT (local)</option>
                      <option value="gemini-cli">Gemini CLI (local)</option>
                      <option value="acp">ACP agent (local)</option>
                    </Select>
                  </FormField>
                </div>
                {isAgentKind(p.type) ? (
                  <>
                    <div className="text-[12px] text-muted">{AGENT_PRESETS.find((a) => a.kind === p.type)?.login}</div>
                    <div className="grid grid-cols-[1fr_2fr] gap-3">
                      <FormField label="Command" help={p.type === "acp" ? "Agent executable" : "Leave empty to use the default on PATH"}>
                        <Input mono value={p.command ?? ""} onChange={(e) => setProvider(i, { command: e.target.value })} placeholder={AGENT_PRESETS.find((a) => a.kind === p.type)?.command || "opencode"} />
                      </FormField>
                      <FormField label="Arguments" help={p.type === "acp" ? "e.g. acp  (for opencode)" : "Extra arguments (optional)"}>
                        <ChipInput mono value={p.args ?? []} onChange={(a) => setProvider(i, { args: a })} placeholder="argument…" />
                      </FormField>
                    </div>
                    <ModelChooser p={p} onChange={(v) => setProvider(i, v)} autoLoad={autoLoad.current.has(p.id)} />
                  </>
                ) : (
                  <>
                <FormField label="Base URL">
                  <Input mono value={p.baseUrl} onChange={(e) => setProvider(i, { baseUrl: e.target.value })} />
                </FormField>
                <FormField label="API key" help="Stored in your global vault as a token item. Leave empty for local servers.">
                  <div className="flex gap-2">
                    <Select
                      value={p.apiKeyRef}
                      onChange={(e) => (e.target.value === "__new" ? setNewKeyFor(i) : setProvider(i, { apiKeyRef: e.target.value }))}
                      className="font-mono text-xs"
                    >
                      <option value="">— none —</option>
                      {p.apiKeyRef && !tokens.some((t) => t.ref === p.apiKeyRef) && <option value={p.apiKeyRef}>{p.apiKeyRef}</option>}
                      {tokens.map((t) => (
                        <option key={t.id} value={t.ref}>
                          {t.name}
                        </option>
                      ))}
                      <option value="__new">+ Add API key to vault…</option>
                    </Select>
                  </div>
                </FormField>
                <ModelChooser p={p} onChange={(v) => setProvider(i, v)} autoLoad={autoLoad.current.has(p.id)} />
                  </>
                )}
              </div>
            ))}
          </div>
          {allModels.length > 0 && (
            <div className="mt-4">
              <FormField label="Default model">
                <Select value={s.defaultModel} onChange={(e) => setS({ ...s, defaultModel: e.target.value })} className="font-mono text-xs">
                  <option value="">— first available —</option>
                  {s.defaultModel && !allModels.some((m) => m.id === s.defaultModel) && <option value={s.defaultModel}>{s.defaultModel}</option>}
                  {allModels.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.label}
                    </option>
                  ))}
                </Select>
              </FormField>
            </div>
          )}
        </Card>

        <Card
          title={
            <>
              <Laptop className="size-4 text-muted" /> This computer
            </>
          }
        >
          <div className="space-y-4">
            <div className="text-xs text-muted leading-relaxed">
              Lets the agent work with this machine like a coding harness does: read your files (e.g. <span className="font-mono">~/.ssh/config</span>,{" "}
              <span className="font-mono">known_hosts</span>, a project folder), write files, run local commands, and import your SSH keys straight into the
              vault without the model ever seeing them. In Ask mode reads outside the workspace ask once per folder, credential files per file, and every
              write and command asks; in Auto mode all of it runs except destructive commands, credential files and the vault; in Full mode nothing asks.
              opsdeck's own vault, login token and settings are never accessible.
            </div>
            <Toggle
              checked={!s.localAccess.disabled}
              onChange={(v) => setS({ ...s, localAccess: { ...s.localAccess, disabled: !v } })}
              label="Allow the agent to access this computer"
            />
            <Toggle
              checked={!s.agent?.autoServerChanges}
              onChange={(v) => setS({ ...s, agent: { ...s.agent!, autoServerChanges: !v } })}
              label={
                <span>
                  Ask before every change on a server, in every mode{" "}
                  <span className="text-faint">(hosts, clusters, pushes and merges — off: Auto and Full change servers without asking)</span>
                </span>
              }
            />
            <FormField
              label="Run system-mode VPNs with"
              help="System (TUN) VPNs need root. auto = sudo when opsdeck is not root: sudo asks for your password in the system's password dialog (none with passwordless sudo for openvpn, openconnect, openfortivpn and wg-quick); pkexec shows polkit's dialog. Userspace VPNs never need this."
            >
              <Select value={s.vpn.elevate || "auto"} onChange={(e) => setS({ ...s, vpn: { ...s.vpn, elevate: e.target.value as Settings["vpn"]["elevate"] } })}>
                <option value="auto">auto (sudo when not root)</option>
                <option value="sudo">sudo</option>
                <option value="pkexec">pkexec</option>
                <option value="none">none (opsdeck runs as root)</option>
              </Select>
            </FormField>
            <FormField
              label="Browser for VPN single sign-on"
              help="Chrome, Edge, Chromium or Brave that opsdeck opens as a login window for SAML VPNs. Empty = detect automatically."
            >
              <Input mono value={s.vpn.browser} onChange={(e) => setS({ ...s, vpn: { ...s.vpn, browser: e.target.value } })} placeholder="auto-detect (google-chrome, msedge, chromium…)" />
            </FormField>
            <div className="grid grid-cols-2 gap-3">
              <FormField label="Git commit name" help="Author of commits made in workspace repositories. Empty = the access token's owner on GitLab/GitHub.">
                <Input value={s.git.name} onChange={(e) => setS({ ...s, git: { ...s.git, name: e.target.value } })} placeholder="from the token" />
              </FormField>
              <FormField label="Git commit email" help="Agent commits also carry an Opsdeck-Agent-Session trailer.">
                <Input value={s.git.email} onChange={(e) => setS({ ...s, git: { ...s.git, email: e.target.value } })} placeholder="from the token" />
              </FormField>
            </div>
            {!s.localAccess.disabled && (
              <>
                <FormField label="Folders the agent may read without asking" help="Absolute paths or ~/… — e.g. ~/projects. Credential files inside them (.env, keys) still ask.">
                  <ChipInput
                    mono
                    value={s.localAccess.allowedDirs}
                    onChange={(v) => setS({ ...s, localAccess: { ...s.localAccess, allowedDirs: v } })}
                    placeholder="~/projects"
                  />
                </FormField>
                <Toggle
                  checked={s.localAccess.autoReadCommands}
                  onChange={(v) => setS({ ...s, localAccess: { ...s.localAccess, autoReadCommands: v } })}
                  label={
                    <span>
                      Run read-only local commands without asking <span className="text-faint">(ls, cat, git status… — they can read any file your account can)</span>
                    </span>
                  }
                />
              </>
            )}
          </div>
        </Card>

        <Card
          title={
            <>
              <Globe className="size-4 text-muted" /> Web access
            </>
          }
        >
          <WebSection value={s.web!} onChange={(web) => setS({ ...s, web })} tokens={tokens} onNewKey={() => setNewKeyFor("web")} />
        </Card>

        <Card
          title={
            <>
              <Plug className="size-4 text-agent-ink" /> Agent access (MCP)
            </>
          }
        >
          <AgentAccessSection />
        </Card>

        <Card
          title={
            <>
              <Bot className="size-4 text-muted" /> Agent harness
            </>
          }
        >
          <div className="space-y-4">
            <div>
              <div className="text-[13px] font-medium mb-1">Subagents</div>
              <div className="text-xs text-muted mb-2">
                The agent can hand work to subagents with their own context: <b>investigator</b> (read-only; several run in parallel) and <b>deployer</b> (carries out a
                planned change with the usual approvals), plus a workspace&apos;s own in <span className="font-mono">agents/*.md</span>. A cheaper, faster model is a good fit
                for investigators.
              </div>
              <Toggle checked={!s.agent?.noSubagents} onChange={(v) => setS({ ...s, agent: { ...s.agent!, noSubagents: !v } })} label="Allow subagents" />
              <div className={cx("mt-3 max-w-md", s.agent?.noSubagents && "opacity-50")}>
                <FormField label="Subagent model" help="Empty: the session's model (local agent CLIs cannot run subagents; then the first API model is used)">
                  <Select value={s.agent?.subagentModel ?? ""} onChange={(e) => setS({ ...s, agent: { ...s.agent!, subagentModel: e.target.value } })} className="font-mono text-xs" disabled={s.agent?.noSubagents}>
                    <option value="">— the session&apos;s model —</option>
                    {s.agent?.subagentModel && !allModels.some((m) => m.id === s.agent?.subagentModel) && <option value={s.agent.subagentModel}>{s.agent.subagentModel}</option>}
                    {s.providers
                      .filter((p) => !isAgentKind(p.type))
                      .flatMap((p) => p.models.map((m) => ({ id: `${p.id}/${m}`, label: `${p.name} · ${p.modelNames?.[m] || m}` })))
                      .map((m) => (
                        <option key={m.id} value={m.id}>
                          {m.label}
                        </option>
                      ))}
                  </Select>
                </FormField>
              </div>
            </div>
            <div className="border-t border-line pt-4">
              <div className="text-[13px] font-medium mb-1 flex items-center gap-1.5">
                <Webhook className="size-3.5 text-muted" /> Hooks
              </div>
              <div className="text-xs text-muted mb-3">
                Commands on this computer that run around the agent&apos;s work, with the event as JSON on stdin — a policy check before tool calls, an audit log, a chat
                message when a turn ends. Workspaces can bring their own hooks; those run only after you trust them in the workspace&apos;s settings.
              </div>
              <HooksEditor hooks={s.agent?.hooks ?? []} onChange={(hooks) => setS({ ...s, agent: { ...s.agent!, hooks } })} canAllow workspaces={workspaces.map((w) => w.name)} />
            </div>
          </div>
        </Card>

        <Card
          title={
            <>
              <MessageSquareText className="size-4 text-muted" /> One-time VPN codes (SMS, email)
            </>
          }
        >
          <VPNCodesSection
            value={s.vpn.codes}
            onChange={(codes) => setS({ ...s, vpn: { ...s.vpn, codes } })}
            tokens={tokens}
            reloadTokens={loadTokens}
          />
        </Card>

        <Card
          title={
            <>
              <KeyRound className="size-4 text-muted" /> Security
            </>
          }
        >
          <div className="grid grid-cols-2 gap-6">
            <FormField label="Auto-lock after (minutes)" help="The vault locks itself after this much inactivity. 0 = never (not recommended).">
              <Input type="number" min={0} value={s.autoLockMinutes} onChange={(e) => setS({ ...s, autoLockMinutes: Math.max(0, Number(e.target.value) || 0) })} />
            </FormField>
          </div>
          <ChangePassword />
        </Card>

        <Card
          title={
            <>
              <Sparkles className="size-4 text-muted" /> Updates
            </>
          }
        >
          <UpdatesSection
            autoCheck={!s.updates?.noCheck}
            setAutoCheck={(v) => setS({ ...s, updates: { noCheck: !v } })}
          />
        </Card>

        <Card title="Appearance">
          <div className="flex flex-wrap items-center gap-3">
            <Segmented
              value={theme}
              onChange={setTheme}
              options={[
                { id: "dark", label: <><Moon className="size-3" /> Dark</> },
                { id: "light", label: <><Sun className="size-3" /> Light</> },
              ]}
            />
            <Button size="sm" icon={<SlidersHorizontal className="size-3.5" />} onClick={() => setCustomizeOpen(true)}>
              Accent, density, menu…
            </Button>
            <span className="text-[11.5px] text-faint">Saved in this browser.</span>
          </div>
        </Card>
      </div>
      <VaultItemModal
        open={newKeyFor !== null}
        onClose={() => setNewKeyFor(null)}
        scope="global"
        defaultType="token"
        allowedTypes={["token"]}
        onSaved={(it) => {
          void loadTokens();
          if (newKeyFor === "web") setS((cur) => (cur ? { ...cur, web: { ...cur.web!, keyRef: it.ref } } : cur));
          else if (newKeyFor !== null) setProvider(newKeyFor, { apiKeyRef: it.ref });
        }}
      />
    </div>
  );
}

const SEARCH: { id: WebSettings["search"]; label: string; help: string }[] = [
  { id: "", label: "DuckDuckGo", help: "No key needed. Results come from DuckDuckGo's HTML page." },
  { id: "searxng", label: "SearXNG (self-hosted)", help: "Your own metasearch instance; its settings must allow format=json. Queries stay with you." },
  { id: "brave", label: "Brave Search API", help: "Needs a Brave Search API key." },
  { id: "tavily", label: "Tavily", help: "Search made for agents; needs a Tavily API key." },
];

function WebSection({ value, onChange, tokens, onNewKey }: { value: WebSettings; onChange: (v: WebSettings) => void; tokens: Item[]; onNewKey: () => void }) {
  const access = value.access || "fetch";
  const backend = SEARCH.find((b) => b.id === (value.search === "duckduckgo" ? "" : value.search)) ?? SEARCH[0];
  return (
    <div className="space-y-4">
      <div className="text-xs text-muted leading-relaxed">
        Lets the agent search the web and read pages — vendor documentation, release notes, error messages, CVEs — and look up end-of-life dates, known vulnerabilities
        and releases. Workspaces can turn it down or off (and a workspace that allows only local models has it off unless it says otherwise).
      </div>
      <div className="flex items-center gap-3">
        <span className="text-xs font-medium text-muted w-28">Default access</span>
        <Segmented
          value={access}
          onChange={(a) => onChange({ ...value, access: a })}
          options={[
            { id: "off", label: "Off" },
            { id: "search", label: "Search only" },
            { id: "fetch", label: "Search & read pages" },
          ]}
        />
      </div>
      {access !== "off" && (
        <>
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Search with" help={backend.help}>
              <Select value={backend.id} onChange={(e) => onChange({ ...value, search: e.target.value as WebSettings["search"] })}>
                {SEARCH.map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.label}
                  </option>
                ))}
              </Select>
            </FormField>
            {backend.id === "searxng" && (
              <FormField label="SearXNG address">
                <Input mono value={value.searchUrl} onChange={(e) => onChange({ ...value, searchUrl: e.target.value })} placeholder="https://searx.example.org" />
              </FormField>
            )}
            {(backend.id === "brave" || backend.id === "tavily") && (
              <FormField label="API key" help="Stored in your global vault as a token item.">
                <Select value={value.keyRef} onChange={(e) => (e.target.value === "__new" ? onNewKey() : onChange({ ...value, keyRef: e.target.value }))} className="font-mono text-xs">
                  <option value="">— choose —</option>
                  {value.keyRef && !tokens.some((t) => t.ref === value.keyRef) && <option value={value.keyRef}>{value.keyRef}</option>}
                  {tokens.map((t) => (
                    <option key={t.id} value={t.ref}>
                      {t.name}
                    </option>
                  ))}
                  <option value="__new">+ Add API key to vault…</option>
                </Select>
              </FormField>
            )}
          </div>
          <FormField label="Proxy" help="socks5://host:port or http://host:port — for networks where sites are blocked or filtered. A workspace can instead send its web requests through one of its own VPNs.">
            <Input mono value={value.proxy} onChange={(e) => onChange({ ...value, proxy: e.target.value })} placeholder="direct" />
          </FormField>
          {access === "fetch" && (
            <div className="space-y-2">
              <FormField label="Sites read without asking" help="Other sites ask once per site in Ask and Plan mode (Auto and Full read them).">
                <ChipInput mono value={value.domains} onChange={(d) => onChange({ ...value, domains: d })} placeholder="wiki.example.com" />
              </FormField>
              <Toggle
                checked={!value.noDefaultDomains}
                onChange={(v) => onChange({ ...value, noDefaultDomains: !v })}
                label={
                  <span>
                    Also documentation sites <span className="text-faint">(kubernetes.io, docs.docker.com, nginx.org, github.com, postgresql.org, … and their subdomains)</span>
                  </span>
                }
              />
            </div>
          )}
          <div className="rounded-lg border border-line bg-panel-2 p-3 text-[12px] text-muted space-y-1.5">
            <div className="flex items-center gap-1.5 text-fg font-medium text-[12.5px]">
              <ShieldCheck className="size-3.5 text-ok" /> Built-in safeguards
            </div>
            <ul className="list-disc ml-5 space-y-0.5">
              <li>Only public addresses: pages and redirects can never reach opsdeck itself, this computer, the local network or cloud metadata.</li>
              <li>Pages reach the model marked as untrusted data; after the agent reads one, its next change asks you, in every mode.</li>
              <li>Queries are redacted, and a query that names an address of the workspace asks first.</li>
              <li>Every request is in the audit journal, like any other tool call.</li>
            </ul>
          </div>
        </>
      )}
    </div>
  );
}

// A random ntfy topic: whoever knows the name can read it.
function newTopic(): string {
  const abc = "abcdefghijkmnopqrstuvwxyz23456789";
  const b = crypto.getRandomValues(new Uint8Array(24));
  return "https://ntfy.sh/opsdeck-" + Array.from(b, (x) => abc[x % abc.length]).join("");
}

function VPNCodesSection({ value, onChange, tokens, reloadTokens }: { value: VpnCodes; onChange: (v: VpnCodes) => void; tokens: Item[]; reloadTokens: () => void }) {
  const [testing, setTesting] = useState(false);
  const [newToken, setNewToken] = useState(false);
  const [howto, setHowto] = useState(false);
  const toast = useToast();
  const set = (p: Partial<VpnCodes>) => onChange({ ...value, ...p });

  async function test() {
    setTesting(true);
    try {
      const r = await api.vpnCodesTest(value);
      toast.success(`Received the code ${r.code}`, `from ${r.source} — it works`);
    } catch (e) {
      toast.error("No code arrived", errMsg(e));
    } finally {
      setTesting(false);
    }
  }

  return (
    <div className="space-y-4">
      <div className="text-xs text-muted leading-relaxed">
        FortiGate VPNs that send a code by SMS or email for every login ask for it in opsdeck: a prompt shows on any page, and on the VPN page. A dropped
        tunnel reconnects with the same session, without a new code, while the gateway keeps it. To have the code filled in by itself, forward the
        gateway's messages from your phone to an <span className="font-mono">ntfy</span> topic (ntfy.sh or your own ntfy server); opsdeck reads it only
        while a login waits for a code.
      </div>
      <FormField
        label="ntfy topic for codes from your phone"
        help="Whoever knows a topic's name can read it: keep it secret (New topic makes a long random one), or use a protected topic on your own ntfy server with an access token."
      >
        <div className="flex gap-2">
          <Input mono value={value.ntfyUrl} onChange={(e) => set({ ntfyUrl: e.target.value })} placeholder="https://ntfy.sh/opsdeck-…" className="flex-1" />
          {value.ntfyUrl && <CopyButton text={value.ntfyUrl} />}
          <Button size="md" onClick={() => set({ ntfyUrl: newTopic() })}>
            New topic
          </Button>
        </div>
      </FormField>
      <div className="grid grid-cols-2 gap-3">
        <FormField label="Access token" help="Only for a protected topic (a token item in the global vault).">
          <Select
            value={value.ntfyTokenRef}
            onChange={(e) => (e.target.value === "__new" ? setNewToken(true) : set({ ntfyTokenRef: e.target.value }))}
            className="font-mono text-xs"
          >
            <option value="">— none —</option>
            {value.ntfyTokenRef && !tokens.some((t) => t.ref === value.ntfyTokenRef) && <option value={value.ntfyTokenRef}>{value.ntfyTokenRef}</option>}
            {tokens.map((t) => (
              <option key={t.id} value={t.ref}>
                {t.name}
              </option>
            ))}
            <option value="__new">+ Add token to vault…</option>
          </Select>
        </FormField>
        <FormField label="Code pattern" help="A regular expression whose first group is the code. Empty: 4 to 8 digits, preferring the number after a word like code, token or کد.">
          <Input mono value={value.pattern} onChange={(e) => set({ pattern: e.target.value })} placeholder="default" />
        </FormField>
      </div>
      <FormField
        label="Command that prints the code (optional)"
        help={
          <>
            Instead of, or besides, the phone: a command on this computer that prints the newest code, e.g. from a USB modem (
            <span className="font-mono">mmcli</span>), KDE Connect or the Messages database of a Mac. It runs every few seconds while a code is awaited, with{" "}
            <span className="font-mono">$OPSDECK_CODE_SINCE</span> (Unix time of the request: print only newer codes, or nothing yet) and{" "}
            <span className="font-mono">$OPSDECK_VPN</span>.
          </>
        }
      >
        <Input mono value={value.command} onChange={(e) => set({ command: e.target.value })} placeholder="~/bin/latest-sms-code" />
      </FormField>
      <div className="flex items-center gap-2">
        <Button icon={<MessageSquareText className="size-4" />} loading={testing} disabled={!value.ntfyUrl && !value.command} onClick={() => void test()}>
          Test
        </Button>
        <span className="text-[11px] text-faint">
          {testing ? "Waiting up to 90 s for a message with a code: send or forward one to the topic now." : "Waits for a message with a code, with the values above (unsaved too)."}
        </span>
        <button className="ml-auto text-[11px] text-accent hover:underline" onClick={() => setHowto((v) => !v)}>
          {howto ? "Hide" : "How to forward SMS from the phone"}
        </button>
      </div>
      {howto && (
        <div className="rounded-md border border-line bg-panel-2 px-3 py-2.5 text-xs text-muted leading-relaxed space-y-2">
          <div>
            <b className="text-fg">Android:</b> install an SMS forwarding app (for example <i>SMS Forwarder</i> or <i>SMS to URL Forwarder</i>) and add a rule:
            messages from the VPN's sender are sent as a webhook, method POST, to the topic URL above, with the message text as the body.
          </div>
          <div>
            <b className="text-fg">iPhone:</b> in Shortcuts → Automation, add a <i>Message</i> automation for the VPN's sender (or a word in its SMS), set it to
            run immediately, with the action <i>Get Contents of URL</i>: the topic URL, method POST, request body the message's content.
          </div>
          <div>
            Forward only the VPN's messages. opsdeck takes a code only from messages that arrive after the login asked for one. More in docs/vpn.md.
          </div>
        </div>
      )}
      <VaultItemModal
        open={newToken}
        onClose={() => setNewToken(false)}
        scope="global"
        defaultType="token"
        allowedTypes={["token"]}
        onSaved={(it) => {
          reloadTokens();
          set({ ntfyTokenRef: it.ref });
        }}
      />
    </div>
  );
}

function UpdatesSection({ autoCheck, setAutoCheck }: { autoCheck: boolean; setAutoCheck: (v: boolean) => void }) {
  const { status, checking, check, showDialog } = useUpdate();
  const latest = status?.latest;
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3">
        <div className="flex-1 text-[13px]">
          <div>
            opsdeck <span className="font-mono">{status?.current || "…"}</span>
          </div>
          <div className="text-xs text-muted mt-0.5">
            {status?.error
              ? status.error
              : status?.state === "done" && latest
                ? `Version ${latest.version} is installed; restart opsdeck to use it.`
                : status?.available && latest
                  ? `Version ${latest.version} is available.`
                  : status?.manual
                    ? status.manual
                    : latest
                      ? "You have the latest version."
                      : status?.checkedAt
                        ? "No release found."
                        : "Not checked yet."}
            {status?.checkedAt && <> · checked {new Date(status.checkedAt).toLocaleString()}</>}
          </div>
        </div>
        {status?.available && (
          <Button variant="primary" size="sm" onClick={showDialog}>
            Show update
          </Button>
        )}
        <Button size="sm" icon={<RefreshCw className="size-3.5" />} onClick={() => void check()} loading={checking}>
          Check now
        </Button>
      </div>
      <Toggle checked={autoCheck} onChange={setAutoCheck} label="Check for new versions on GitHub automatically" />
    </div>
  );
}

function ChangePassword() {
  const [o, setO] = useState("");
  const [n, setN] = useState("");
  const [n2, setN2] = useState("");
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const st = passwordStrength(n);
  const ok = o && n.length >= 10 && n === n2;
  return (
    <div className="mt-5 border-t border-line pt-4">
      <div className="text-[13px] font-medium mb-2">Change master password</div>
      <div className="grid grid-cols-3 gap-3 items-start">
        <FormField label="Current password">
          <Input type="password" value={o} onChange={(e) => setO(e.target.value)} />
        </FormField>
        <FormField label="New password" help={n ? st.label : "min. 10 characters"}>
          <Input type="password" value={n} onChange={(e) => setN(e.target.value)} />
        </FormField>
        <FormField label="Confirm" help={n2 && n !== n2 ? "Does not match" : " "}>
          <Input type="password" value={n2} onChange={(e) => setN2(e.target.value)} />
        </FormField>
      </div>
      <Button
        className="mt-3"
        disabled={!ok}
        loading={busy}
        icon={<Plus className="size-3.5 hidden" />}
        onClick={async () => {
          setBusy(true);
          try {
            await api.vaultChangePassword(o, n);
            toast.success("Master password changed");
            setO("");
            setN("");
            setN2("");
          } catch (e) {
            toast.error("Cannot change password", errMsg(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        Change password
      </Button>
    </div>
  );
}
