import { useEffect, useState } from "react";
import { Bot, Cpu, KeyRound, Laptop, Moon, Plus, Save, Settings as SettingsIcon, Sun, Trash2 } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { AgentKind, InstalledAgent, Item, ProviderConfig, Settings } from "../api/types";
import { isAgentKind } from "../api/types";
import { useApp } from "../context/app";
import { Badge, Button, Card, ChipInput, ErrorBox, FormField, IconButton, Input, Loading, PageHeader, Segmented, Select, Toggle } from "../components/ui";
import { useConfirm, useToast } from "../components/overlay";
import { VaultItemModal } from "../components/VaultItemForm";
import { passwordStrength } from "../lib/format";

const PRESETS: { label: string; type: ProviderConfig["type"]; id: string; baseUrl: string; models: string[]; needsKey: boolean }[] = [
  { label: "Anthropic", type: "anthropic", id: "anthropic", baseUrl: "https://api.anthropic.com", models: ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"], needsKey: true },
  { label: "OpenAI", type: "openai", id: "openai", baseUrl: "https://api.openai.com/v1", models: [], needsKey: true },
  { label: "OpenRouter", type: "openai", id: "openrouter", baseUrl: "https://openrouter.ai/api/v1", models: [], needsKey: true },
  { label: "Ollama (local)", type: "openai", id: "ollama", baseUrl: "http://localhost:11434/v1", models: ["llama3.1", "qwen2.5-coder"], needsKey: false },
  { label: "LM Studio (local)", type: "openai", id: "lmstudio", baseUrl: "http://localhost:1234/v1", models: [], needsKey: false },
];

const AGENT_PRESETS: { kind: AgentKind; label: string; id: string; command: string; args: string[]; models: string[]; login: string }[] = [
  { kind: "claude-code", label: "Claude Code", id: "claude-code", command: "claude", args: [], models: ["default", "opus", "sonnet", "haiku"], login: "Uses your own Claude Code login (subscription or API key). Run `claude` once in a terminal and sign in." },
  { kind: "codex", label: "Codex (ChatGPT)", id: "codex", command: "codex", args: [], models: ["default"], login: "Uses your ChatGPT account through the official Codex CLI. Run `codex login` once." },
  { kind: "gemini-cli", label: "Gemini CLI", id: "gemini", command: "gemini", args: [], models: ["default"], login: "Uses your Google account through Gemini CLI. Run `gemini` once and sign in." },
  { kind: "acp", label: "Other ACP agent (opencode, Qwen Code…)", id: "acp-agent", command: "", args: [], models: ["default"], login: "Any agent that speaks the Agent Client Protocol, e.g. `opencode acp`." },
];

const NO_LOCAL: Settings["localAccess"] = { disabled: false, allowedDirs: [], autoReadCommands: false };
const normSettings = (v: Settings): Settings => ({
  ...v,
  vpn: { elevate: v.vpn?.elevate || "auto", browser: v.vpn?.browser ?? "" },
  providers: v.providers ?? [],
  localAccess: { ...NO_LOCAL, ...(v.localAccess ?? {}), allowedDirs: v.localAccess?.allowedDirs ?? [] },
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
  const [s, setS] = useState<Settings | null>(null);
  const [orig, setOrig] = useState<string>("");
  const [err, setErr] = useState("");
  const [saving, setSaving] = useState(false);
  const [tokens, setTokens] = useState<Item[]>([]);
  const [newKeyFor, setNewKeyFor] = useState<number | null>(null);
  const [installed, setInstalled] = useState<InstalledAgent[] | null>(null);
  const { theme, setTheme } = useApp();
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

  const setProvider = (i: number, p: Partial<ProviderConfig>) => setS({ ...s, providers: s.providers.map((x, j) => (j === i ? { ...x, ...p } : x)) });
  const addPreset = (idx: number) => {
    const pr = PRESETS[idx];
    let id = pr.id;
    let n = 2;
    while (s.providers.some((p) => p.id === id)) id = `${pr.id}-${n++}`;
    setS({ ...s, providers: [...s.providers, { id, type: pr.type, name: pr.label, baseUrl: pr.baseUrl, apiKeyRef: "", models: pr.models }] });
  };
  const uniqueId = (base: string) => {
    let id = base;
    let n = 2;
    while (s.providers.some((p) => p.id === id)) id = `${base}-${n++}`;
    return id;
  };
  const addAgent = (a: { kind: AgentKind; label: string; id: string; command: string; args: string[]; models: string[] }) =>
    setS({ ...s, providers: [...s.providers, { id: uniqueId(a.id), type: a.kind, name: a.label, baseUrl: "", apiKeyRef: "", models: a.models, command: a.command, args: a.args, env: {} }] });
  const allModels = s.providers.flatMap((p) => p.models.map((m) => `${p.id}/${m}`));

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
                    <FormField label="Models" help="“default” uses the agent's own default model; or add model names/aliases the agent accepts">
                      <ChipInput mono value={p.models} onChange={(m) => setProvider(i, { models: m })} placeholder="model…" />
                    </FormField>
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
                <FormField label="Models" help="Press Enter after each model id">
                  <ChipInput mono value={p.models} onChange={(m) => setProvider(i, { models: m })} placeholder="model id…" />
                </FormField>
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
                  {allModels.map((m) => (
                    <option key={m} value={m}>
                      {m}
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
              vault without the model ever seeing them. Reads outside the workspace ask once per folder, credential files ask per file, and every write and
              command asks for approval. opsdeck's own vault, login token and settings are never accessible.
            </div>
            <Toggle
              checked={!s.localAccess.disabled}
              onChange={(v) => setS({ ...s, localAccess: { ...s.localAccess, disabled: !v } })}
              label="Allow the agent to access this computer"
            />
            <FormField
              label="Run system-mode VPNs with"
              help="System (TUN) VPNs need root. auto = sudo -n when opsdeck is not root (configure passwordless sudo for openvpn, openconnect, openfortivpn and wg-quick); pkexec shows a password dialog on desktops. Userspace VPNs never need this."
            >
              <Select value={s.vpn.elevate || "auto"} onChange={(e) => setS({ ...s, vpn: { ...s.vpn, elevate: e.target.value as Settings["vpn"]["elevate"] } })}>
                <option value="auto">auto (sudo -n when not root)</option>
                <option value="sudo">sudo -n</option>
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

        <Card title="Appearance">
          <Segmented
            value={theme}
            onChange={setTheme}
            options={[
              { id: "dark", label: <><Moon className="size-3" /> Dark</> },
              { id: "light", label: <><Sun className="size-3" /> Light</> },
            ]}
          />
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
          if (newKeyFor !== null) setProvider(newKeyFor, { apiKeyRef: it.ref });
        }}
      />
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
