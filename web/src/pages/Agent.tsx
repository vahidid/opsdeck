import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  AlertTriangle,
  BookOpen,
  Bot,
  Check,
  ChevronDown,
  CircleSlash,
  Cpu,
  Eye,
  FileClock,
  FileText,
  GitFork,
  Globe,
  Hand,
  Loader2,
  MessageSquarePlus,
  Minimize2,
  MoreHorizontal,
  Pencil,
  Rocket,
  ScrollText,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  SquareArrowOutUpRight,
  Terminal,
  Trash2,
  User,
  Wrench,
  Zap,
} from "lucide-react";
import { api, errMsg } from "../api/client";
import type { AgentEvent, HarnessInfo, ModelInfo, RecallItem, SessionInfo, SessionMode, SessionState } from "../api/types";
import { useWs } from "../context/app";
import { useOpenInTab, useTabTitle, useTabs } from "../context/tabs";
import { useTerminals } from "../context/terminals";
import { Badge, Button, EmptyState, IconButton, Input, Segmented, Select, StatusDot, cx } from "../components/ui";
import { MenuItem, Modal, Popover, useConfirm, useToast } from "../components/overlay";
import { Markdown } from "../components/Markdown";
import { textDir } from "../lib/rtl";
import { useAsync, useLocalStorage } from "../hooks";
import { fmtTokens, timeAgo } from "../lib/format";
import { BUILTIN_COMMANDS, Composer, parseShell } from "./agent/composer";
import type { SlashCommand } from "./agent/composer";
import { NotificationMsg, ShellMsg, TasksButton, TodoPanel, UserCommand, useTasks } from "./agent/panels";
import { buildTimeline, callKey, groupBlocks, waiting } from "./agent/timeline";
import { ToolGroup } from "./agent/tools";

// ---------------------------------------------------------------- page

export function AgentPage() {
  const { name: ws, workspace } = useWs();
  const { sid } = useParams();
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const toast = useToast();
  const confirm = useConfirm();
  const sessions = useAsync(() => api.sessions(ws), [ws]);
  const models = useAsync(() => api.models(), []);
  const settings = useAsync(() => api.settings(), []);
  const [renaming, setRenaming] = useState<SessionInfo | null>(null);
  const [menu, setMenu] = useState<string | null>(null);
  const openInTab = useOpenInTab();
  const wsTabs = useTabs();
  const current = sessions.data?.find((s) => s.id === sid);
  useTabTitle(sid ? `Agent · ${current?.title || sid.slice(0, 8)}` : "Agent · new");

  useEffect(() => {
    if (params.get("new")) {
      params.delete("new");
      setParams(params, { replace: true });
      if (sid) navigate(`/w/${encodeURIComponent(ws)}/agent`);
    }
  }, [params, setParams, sid, navigate, ws]);

  // the models this workspace allows (settings.models patterns)
  const allowed = useMemo(() => {
    const pats = workspace?.settings?.models ?? [];
    const list = models.data ?? [];
    if (!pats.length) return list;
    const re = pats.map((p) => new RegExp("^" + p.split("*").map((x) => x.replace(/[.+?^${}()|[\]\\]/g, "\\$&")).join(".*") + "$"));
    return list.filter((m) => re.some((r) => r.test(m.id)));
  }, [models.data, workspace]);

  const defaultModel = useMemo(() => {
    const pref = workspace?.settings?.defaultModel || settings.data?.defaultModel;
    return allowed.find((m) => m.id === pref)?.id ?? allowed[0]?.id ?? "";
  }, [allowed, settings.data, workspace]);

  const sorted = useMemo(() => [...(sessions.data ?? [])].sort((a, b) => (b.updated || "").localeCompare(a.updated || "")), [sessions.data]);

  async function remove(s: SessionInfo) {
    if (!(await confirm({ title: `Delete session "${s.title || "Untitled"}"?`, message: "The transcript is deleted. The audit journal keeps its entries.", danger: true, confirmLabel: "Delete" })))
      return;
    try {
      await api.deleteSession(ws, s.id);
      if (s.id === sid) navigate(`/w/${encodeURIComponent(ws)}/agent`);
      void sessions.reload();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  async function fork(s: SessionInfo) {
    try {
      const r = await api.forkSession(ws, s.id);
      void sessions.reload();
      navigate(`/w/${encodeURIComponent(ws)}/agent/${encodeURIComponent(r.session.id)}`);
    } catch (e) {
      toast.error("Fork failed", errMsg(e));
    }
  }

  return (
    <div className="flex h-full min-h-0">
      <aside className="hidden sm:flex w-52 md:w-64 shrink-0 border-r border-line bg-panel flex-col min-h-0">
        <div className="p-2 border-b border-line">
          <Button className="w-full" icon={<MessageSquarePlus className="size-4" />} {...openInTab("agent")}>
            New session
          </Button>
        </div>
        <div className="flex-1 overflow-y-auto p-1.5 space-y-px">
          {sorted.length === 0 && !sessions.loading && <div className="text-xs text-faint p-3">No sessions yet.</div>}
          {sorted.map((s) => (
            <div
              key={s.id}
              {...openInTab(`agent/${encodeURIComponent(s.id)}`)}
              title="Ctrl/⌘-click or middle-click: open in a new tab"
              className={cx("group relative flex items-center gap-2 rounded-md px-2 py-1.5 cursor-pointer", s.id === sid ? "bg-panel-3" : "hover:bg-panel-2")}
            >
              <StateDot state={s.state} />
              <div className="min-w-0 flex-1">
                <div dir="auto" className={cx("truncate text-[12.5px] text-start", s.id === sid ? "text-fg" : "text-muted group-hover:text-fg")}>
                  {s.title || "Untitled session"}
                </div>
                <div className="text-[10.5px] text-faint flex items-center gap-1.5">
                  <span>{timeAgo(s.updated)}</span>
                  <span className="uppercase">{s.mode}</span>
                  {s.headless && (
                    <span className="inline-flex items-center gap-0.5" title="Started by opsdeck run (headless)">
                      <Terminal className="size-2.5" /> run
                    </span>
                  )}
                  {s.forkedFrom && <GitFork className="size-2.5" aria-label="forked" />}
                </div>
              </div>
              <div className="relative" onClick={(e) => e.stopPropagation()}>
                <IconButton title="Session actions" className="size-6 opacity-0 group-hover:opacity-100" onClick={() => setMenu(menu === s.id ? null : s.id)}>
                  <MoreHorizontal className="size-3.5" />
                </IconButton>
                <Popover open={menu === s.id} onClose={() => setMenu(null)} className="right-0 mt-1 w-44">
                  <MenuItem icon={<SquareArrowOutUpRight />} onClick={() => (setMenu(null), wsTabs.open(ws, `agent/${encodeURIComponent(s.id)}`))}>
                    Open in new tab
                  </MenuItem>
                  <MenuItem icon={<Pencil />} onClick={() => (setMenu(null), setRenaming(s))}>
                    Rename
                  </MenuItem>
                  <MenuItem icon={<GitFork />} onClick={() => (setMenu(null), void fork(s))}>
                    Fork
                  </MenuItem>
                  <MenuItem icon={<Trash2 />} danger onClick={() => (setMenu(null), void remove(s))}>
                    Delete
                  </MenuItem>
                </Popover>
              </div>
            </div>
          ))}
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col">
        {models.data && models.data.length === 0 ? (
          <NoModels />
        ) : sid ? (
          <SessionView key={sid} ws={ws} sid={sid} models={allowed} onChanged={() => void sessions.reload()} />
        ) : (
          <NewSessionView
            ws={ws}
            models={allowed}
            defaultModel={defaultModel}
            onCreated={(s) => {
              void sessions.reload();
              navigate(`/w/${encodeURIComponent(ws)}/agent/${encodeURIComponent(s.id)}`);
            }}
          />
        )}
      </section>
      <RenameModal
        session={renaming}
        onClose={() => setRenaming(null)}
        onSave={async (title) => {
          if (!renaming) return;
          try {
            await api.updateSession(ws, renaming.id, { title });
            setRenaming(null);
            void sessions.reload();
          } catch (e) {
            toast.error("Rename failed", errMsg(e));
          }
        }}
      />
    </div>
  );
}

function StateDot({ state }: { state: SessionState }) {
  if (state === "running") return <StatusDot tone="info" pulse />;
  if (state === "waiting_approval" || state === "waiting_input") return <StatusDot tone="warn" pulse />;
  return <StatusDot tone="neutral" />;
}

function NoModels() {
  return (
    <EmptyState
      icon={<Cpu />}
      title="Connect a model to use the agent"
      className="flex-1"
      action={
        <Link to="/settings">
          <Button variant="primary">Open settings</Button>
        </Link>
      }
    >
      Add an Anthropic or OpenAI-compatible provider (OpenAI, OpenRouter, Ollama, LM Studio…). API keys are stored in your vault. Everything else in opsdeck works without a model.
    </EmptyState>
  );
}

function RenameModal({ session, onClose, onSave }: { session: SessionInfo | null; onClose: () => void; onSave: (t: string) => Promise<void> }) {
  const [t, setT] = useState("");
  useEffect(() => setT(session?.title ?? ""), [session]);
  return (
    <Modal
      open={!!session}
      onClose={onClose}
      title="Rename session"
      width={420}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void onSave(t.trim())} disabled={!t.trim()}>
            Save
          </Button>
        </>
      }
    >
      <Input dir="auto" autoFocus value={t} onChange={(e) => setT(e.target.value)} onKeyDown={(e) => e.key === "Enter" && t.trim() && void onSave(t.trim())} />
    </Modal>
  );
}

// ---------------------------------------------------------------- mode/model controls

const MODES: { id: SessionMode; label: string; icon: React.ReactNode; desc: (servers: boolean) => string }[] = [
  { id: "plan", label: "Plan", icon: <Eye className="size-3.5" />, desc: () => "Read-only investigation. The agent proposes a plan; nothing is changed." },
  { id: "ask", label: "Ask", icon: <Hand className="size-3.5" />, desc: () => "Reads run freely; every change asks for your approval (per workspace rules)." },
  {
    id: "auto",
    label: "Auto",
    icon: <Zap className="size-3.5" />,
    desc: (servers) =>
      "Everything on this computer runs without asking — files anywhere, local commands, the workspace. Destructive commands, credential files and the vault still ask. " +
      (servers ? "Server changes run too, except destructive ones and protected environments." : "Every change on a server asks."),
  },
  {
    id: "full",
    label: "Full",
    icon: <Rocket className="size-3.5" />,
    desc: (servers) =>
      "No questions on this computer, destructive commands and credential files included. " +
      (servers ? "Server changes run without asking too — destructive ones and protected environments included." : "Every change on a server still asks."),
  },
];

function ModePicker({ value, onChange }: { value: SessionMode; onChange: (m: SessionMode) => void }) {
  const [open, setOpen] = useState(false);
  const settings = useAsync(() => api.settings(), [open]);
  const servers = !!settings.data?.agent?.autoServerChanges;
  const cur = MODES.find((m) => m.id === value) ?? MODES[1];
  return (
    <div className="relative">
      <button
        onClick={() => setOpen((o) => !o)}
        className={cx(
          "flex items-center gap-1.5 h-7 px-2 rounded-md border text-xs font-medium",
          value === "full" ? "border-err/40 bg-err/10 text-err" : value === "auto" ? "border-warn/40 bg-warn/10 text-warn" : value === "plan" ? "border-info/40 bg-info/10 text-info" : "border-line bg-panel-2 text-fg",
        )}
      >
        {cur.icon}
        {cur.label}
        <ChevronDown className="size-3 opacity-60" />
      </button>
      <Popover open={open} onClose={() => setOpen(false)} className="bottom-full mb-1 left-0 w-96 p-1">
        {MODES.map((m) => (
          <button
            key={m.id}
            onClick={() => {
              onChange(m.id);
              setOpen(false);
            }}
            className={cx("w-full text-left rounded-md px-2.5 py-2 hover:bg-panel-3 flex gap-2.5", m.id === value && "bg-panel-2")}
          >
            <span className="mt-0.5 text-muted">{m.icon}</span>
            <span className="flex-1">
              <span className="flex items-center gap-2 text-[13px] font-medium">
                {m.label}
                {m.id === value && <Check className="size-3.5 text-accent" />}
              </span>
              <span className="block text-[11.5px] text-muted mt-0.5">{m.desc(servers)}</span>
              {(m.id === "auto" || m.id === "full") && (
                <span className={cx("flex items-center gap-1 text-[11px] mt-1", m.id === "full" ? "text-err" : "text-warn")}>
                  <AlertTriangle className="size-3" /> {m.id === "full" ? "Only for work you would let it do unattended." : "Use with care on shared infrastructure."}
                </span>
              )}
            </span>
          </button>
        ))}
        <div className="border-t border-line mt-1 px-2.5 py-1.5 text-[11px] text-faint">
          Server changes in Auto and Full: <b>{servers ? "run without asking" : "always ask"}</b> — App settings → Agent.
        </div>
      </Popover>
    </div>
  );
}

function ModelPicker({ models, value, onChange }: { models: ModelInfo[]; value: string; onChange: (m: string) => void }) {
  // one group per provider when there are several
  const groups: { provider: string; name: string; models: ModelInfo[] }[] = [];
  for (const m of models) {
    const g = groups.find((x) => x.provider === m.provider);
    if (g) g.models.push(m);
    else groups.push({ provider: m.provider, name: m.providerName || m.provider, models: [m] });
  }
  // the full label: the closed picker shows only the selected option
  const option = (m: ModelInfo) => (
    <option key={m.id} value={m.id} title={m.id}>
      {m.label || m.id}
    </option>
  );
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)} className="h-7 text-xs w-auto max-w-72 py-0" title={value}>
      {!models.some((m) => m.id === value) && value && <option value={value}>{value}</option>}
      {groups.length > 1
        ? groups.map((g) => (
            <optgroup key={g.provider} label={g.name}>
              {g.models.map(option)}
            </optgroup>
          ))
        : models.map(option)}
    </Select>
  );
}

function parseModeCommand(t: string): SessionMode | null {
  const m = /^\/(plan|ask|auto|full)\s*$/i.exec(t.trim());
  return m ? (m[1].toLowerCase() as SessionMode) : null;
}

/** the chat's commands: built-in ones and the workspace's commands/*.md */
function useCommands(harness?: HarnessInfo, builtin: SlashCommand[] = BUILTIN_COMMANDS): SlashCommand[] {
  return useMemo(
    () => [
      ...builtin,
      ...(harness?.commands ?? [])
        .filter((c) => !builtin.some((b) => b.name === c.name))
        .map((c) => ({ name: c.name, args: c.argumentHint, description: c.description, workspace: true })),
    ],
    [harness, builtin],
  );
}

// ---------------------------------------------------------------- new session

const EXAMPLES = [
  { icon: <ShieldCheck className="size-4" />, text: "Check disk, memory and load on all prod hosts and summarize anything above 80%." },
  { icon: <AlertTriangle className="size-4" />, text: "nginx on app-1 returns 502. Investigate the logs, upstream and config, and tell me the root cause." },
  { icon: <Wrench className="size-4" />, text: "List containers on every docker host and flag the ones that are restarting or unhealthy." },
  { icon: <Sparkles className="size-4" />, text: "Check which of our hosts run an OS or database version that is out of support, and list known CVEs for them." },
];

const NEW_COMMANDS = BUILTIN_COMMANDS.filter((c) => ["plan", "ask", "auto", "full"].includes(c.name));

function HarnessStrip({ h }: { h?: HarnessInfo }) {
  if (!h) return null;
  const web = h.web === "fetch" ? "search & read pages" : h.web === "search" ? "search only" : "off";
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11.5px] text-muted">
      <span className="inline-flex items-center gap-1.5" title={h.search ? `Search: ${h.search}` : undefined}>
        <Globe className={cx("size-3.5", h.web === "off" ? "text-faint" : "text-accent")} /> Web: {web}
      </span>
      {h.subagents.length > 0 && (
        <span className="inline-flex items-center gap-1.5" title={h.subagents.map((s) => `${s.name}: ${s.description}`).join("\n")}>
          <Bot className="size-3.5 text-agent-ink" /> Subagents: {h.subagents.map((s) => s.name).join(", ")}
        </span>
      )}
      {h.commands.length > 0 && (
        <span className="inline-flex items-center gap-1.5" title={h.commands.map((c) => `/${c.name} — ${c.description}`).join("\n")}>
          <ScrollText className="size-3.5 text-accent" /> {h.commands.length} workspace command{h.commands.length > 1 ? "s" : ""}
        </span>
      )}
      {h.models.length > 0 && (
        <span className="inline-flex items-center gap-1.5">
          <Cpu className="size-3.5 text-muted" /> Models: {h.models.join(", ")}
        </span>
      )}
    </div>
  );
}

function NewSessionView({ ws, models, defaultModel, onCreated }: { ws: string; models: ModelInfo[]; defaultModel: string; onCreated: (s: SessionInfo) => void }) {
  const { workspace, resources } = useWs();
  const [mode, setMode] = useState<SessionMode>("ask");
  const [model, setModel] = useState(defaultModel);
  const toast = useToast();
  // ?draft= (from the overview or a log line) pre-fills the first message
  const [params, setParams] = useSearchParams();
  const [seed, setSeed] = useState(() => params.get("draft") ?? "");
  useEffect(() => {
    if (params.get("draft")) {
      params.delete("draft");
      setParams(params, { replace: true });
    }
  }, [params, setParams]);
  const harness = useAsync(() => api.harness(ws), [ws]);
  const commands = useCommands(harness.data, NEW_COMMANDS);
  useEffect(() => {
    if (!model && defaultModel) setModel(defaultModel);
  }, [defaultModel, model]);

  async function start(text: string) {
    const m = parseModeCommand(text);
    if (m) {
      setMode(m);
      return true;
    }
    try {
      const s = await api.createSession(ws, { model: model || undefined, mode });
      await api.sendMessage(ws, s.id, text);
      onCreated(s);
      return true;
    } catch (e) {
      toast.error("Cannot start session", errMsg(e));
      return false;
    }
  }

  return (
    <div className="flex-1 flex flex-col items-center justify-center p-6 overflow-y-auto">
      <div className="w-full max-w-[760px]">
        <div className="flex items-center gap-3 mb-1">
          <span className="size-10 rounded-xl grid place-items-center bg-agent text-agent-fg shadow-lg shadow-agent/30">
            <Bot className="size-5" />
          </span>
          <div>
            <h1 className="text-[18px] font-semibold">What should we do in {workspace?.title || ws}?</h1>
            <div className="text-muted text-xs">
              The agent knows this workspace: {resources.filter((r) => r.kind === "Host").length} hosts, its networks, docs and memory. Secrets stay in the vault.
            </div>
          </div>
        </div>
        <div className="mt-3">
          <HarnessStrip h={harness.data} />
        </div>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-2 my-5">
          {EXAMPLES.map((e) => (
            <button
              key={e.text}
              onClick={() => setSeed(e.text)}
              className="flex gap-2.5 text-left rounded-lg border border-line bg-panel p-3 hover:border-line-strong hover:bg-panel-2 transition-colors"
            >
              <span className="text-muted mt-0.5">{e.icon}</span>
              <span className="text-[12.5px] text-muted">{e.text}</span>
            </button>
          ))}
        </div>
        <Composer
          onSend={start}
          seed={seed}
          autoFocus
          shell={false}
          commands={commands}
          placeholder="Ask the agent to investigate or change something…   / for commands"
          left={
            <>
              <ModePicker value={mode} onChange={setMode} />
              <ModelPicker models={models} value={model} onChange={setModel} />
            </>
          }
        />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- session view

function eventSeq(e: AgentEvent): number {
  const n = Number(String(e.id ?? "").replace(/^e/, ""));
  return Number.isFinite(n) ? n : Number.MAX_SAFE_INTEGER;
}

/** Union of persisted (server) and live events, ordered by their sequence id. */
function mergeEvents(live: AgentEvent[], server: AgentEvent[]): AgentEvent[] {
  const byId = new Map<string, AgentEvent>();
  for (const e of server) byId.set(e.id, e);
  const finished = new Set(server.filter((e) => e.type === "assistant").map((e) => (e as { messageId?: string }).messageId));
  for (const e of live) {
    if (e.type === "status" || e.type === "tool_progress" || byId.has(e.id)) continue;
    if (e.type === "assistant_delta" && finished.has(e.messageId)) continue;
    if (e.type === "assistant" && server.some((x) => x.type === "assistant" && x.messageId === e.messageId)) continue;
    byId.set(e.id, e);
  }
  return [...byId.values()].sort((a, b) => eventSeq(a) - eventSeq(b));
}

function SessionView({ ws, sid, models, onChanged }: { ws: string; sid: string; models: ModelInfo[]; onChanged: () => void }) {
  const toast = useToast();
  const navigate = useNavigate();
  const wsTabs = useTabs();
  const terms = useTerminals();
  const { hosts } = useWs();
  const [params, setParams] = useSearchParams();
  const [session, setSession] = useState<SessionInfo | null>(null);
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [deltas, setDeltas] = useState<Record<string, string>>({});
  const [progress, setProgress] = useState<Record<string, string>>({});
  const [loadErr, setLoadErr] = useState("");
  const [connected, setConnected] = useState(false);
  const [menu, setMenu] = useState(false);
  const [tasksOpen, setTasksOpen] = useState(false);
  const [busy, setBusy] = useState("");
  const [seed] = useState(() => params.get("draft") ?? "");
  const harness = useAsync(() => api.harness(ws), [ws]);
  const commands = useCommands(harness.data);
  const scrollRef = useRef<HTMLDivElement>(null);
  const stickRef = useRef(true);
  const onChangedRef = useRef(onChanged);
  onChangedRef.current = onChanged;

  useEffect(() => {
    if (params.get("draft")) {
      params.delete("draft");
      setParams(params, { replace: true });
    }
  }, [params, setParams]);

  const load = useCallback(async () => {
    try {
      const r = await api.session(ws, sid);
      setSession(r.session);
      // Merge with events that arrived over the stream meanwhile, by id.
      setEvents((prev) => mergeEvents(prev, r.events.filter((e) => e.type !== "assistant_delta")));
      setLoadErr("");
    } catch (e) {
      setLoadErr(errMsg(e));
    }
  }, [ws, sid]);

  useEffect(() => {
    void load();
  }, [load]);

  // live stream
  useEffect(() => {
    const es = new EventSource(api.sessionStreamUrl(ws, sid), { withCredentials: true });
    es.onopen = () => {
      setConnected(true);
      // Fetch after subscribing so nothing emitted in between is lost
      // (also fills the gap after a reconnect).
      void load();
    };
    es.onerror = () => setConnected(false);
    es.onmessage = (m) => {
      let ev: AgentEvent;
      try {
        ev = JSON.parse(m.data) as AgentEvent;
      } catch {
        return;
      }
      if (ev.type === "tool_progress") {
        setProgress((p) => ({ ...p, [callKey(ev.parent, ev.callId)]: ev.text }));
        return;
      }
      if (ev.type === "assistant_delta") {
        setDeltas((d) => ({ ...d, [ev.messageId]: (d[ev.messageId] ?? "") + ev.text }));
        setEvents((list) =>
          list.some((x) => (x.type === "assistant" || x.type === "assistant_delta") && x.messageId === ev.messageId) ? list : [...list, { ...ev, text: "" }],
        );
        return;
      }
      if (ev.type === "status") {
        setSession((s) => (s ? { ...s, state: ev.state, usage: ev.usage ?? s.usage } : s));
        if (ev.state === "idle") onChangedRef.current();
      }
      if (ev.type === "tool_result")
        setProgress((p) => {
          const key = callKey(ev.parent, ev.callId);
          if (!(key in p)) return p;
          const { [key]: _drop, ...rest } = p;
          return rest;
        });
      setEvents((list) => {
        if (ev.id && list.some((x) => x.id === ev.id)) return list;
        if (ev.type === "assistant") {
          const idx = list.findIndex((x) => x.type === "assistant_delta" && x.messageId === ev.messageId);
          if (idx >= 0) {
            const copy = list.slice();
            copy[idx] = ev;
            return copy;
          }
        }
        return [...list, ev];
      });
      if (ev.type === "assistant") {
        setDeltas((d) => {
          if (!(ev.messageId in d)) return d;
          const { [ev.messageId]: _drop, ...rest } = d;
          return rest;
        });
      }
    };
    return () => es.close();
  }, [ws, sid, load]);

  const { items: timeline, todo } = useMemo(() => buildTimeline(events, deltas), [events, deltas]);
  const tasks = useTasks(events as { type: string; task?: never }[]);

  // auto-scroll when at bottom
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickRef.current) el.scrollTop = el.scrollHeight;
  }, [timeline, progress]);

  const blocks = useMemo(() => groupBlocks(timeline), [timeline]);
  const [toolView, setToolView] = useLocalStorage<"grouped" | "open">("opsdeck.agent.toolView", "grouped");

  const state = session?.state ?? "idle";
  const running = state !== "idle";
  const wait = waiting(timeline);
  const ctx = { ws, sid, progress };
  const showTodo = !!todo?.length && (running || todo.some((t) => t.status !== "done" && t.status !== "skipped"));

  async function updateSession(b: { mode?: SessionMode; model?: string }) {
    try {
      const s = await api.updateSession(ws, sid, b);
      setSession(s);
      onChangedRef.current();
    } catch (e) {
      toast.error("Update failed", errMsg(e));
    }
  }

  async function fork(at?: string) {
    try {
      const r = await api.forkSession(ws, sid, at);
      onChangedRef.current();
      const draft = r.text ? `?draft=${encodeURIComponent(r.text)}` : "";
      navigate(`/w/${encodeURIComponent(ws)}/agent/${encodeURIComponent(r.session.id)}${draft}`);
    } catch (e) {
      toast.error("Fork failed", errMsg(e));
    }
  }

  async function exportAs(as: "report" | "runbook" | "skill") {
    setBusy("export-" + as);
    try {
      const r = await api.exportSession(ws, sid, as);
      toast.success(as === "report" ? "Saved as a report" : as === "runbook" ? "Runbook written" : "Skill written", r.path);
      wsTabs.open(ws, `docs?path=${encodeURIComponent(r.path)}`);
    } catch (e) {
      toast.error("Export failed", errMsg(e));
    } finally {
      setBusy("");
    }
  }

  async function compact() {
    setBusy("compact");
    try {
      await api.compactSession(ws, sid);
      await load();
      toast.success("Conversation summarized");
    } catch (e) {
      toast.error("Cannot summarize", errMsg(e));
    } finally {
      setBusy("");
    }
  }

  // the chat's own commands; anything else goes to the agent
  async function command(text: string): Promise<boolean | null> {
    const [head, ...rest] = text.trim().split(/\s+/);
    const arg = rest.join(" ");
    const go = (path: string) => wsTabs.open(ws, path);
    switch (head.slice(1).toLowerCase()) {
      case "plan":
      case "ask":
      case "auto":
      case "full":
        await updateSession({ mode: head.slice(1).toLowerCase() as SessionMode });
        return true;
      case "model":
        if (!arg) {
          toast.error("Which model?", "e.g. /model anthropic/claude-sonnet-5-5");
          return false;
        }
        await updateSession({ model: arg });
        return true;
      case "compact":
        await compact();
        return true;
      case "fork":
        await fork();
        return true;
      case "export": {
        const as = (arg || "report") as "report" | "runbook" | "skill";
        if (!["report", "runbook", "skill"].includes(as)) {
          toast.error("Export as report, runbook or skill");
          return false;
        }
        await exportAs(as);
        return true;
      }
      case "tasks":
        setTasksOpen(true);
        return true;
      case "new":
        navigate(`/w/${encodeURIComponent(ws)}/agent`);
        return true;
      case "ssh": {
        const h = hosts.find((x) => x.name === arg);
        if (!h) {
          toast.error("Which host?", hosts.map((x) => x.name).join(", ") || "This workspace has no hosts");
          return false;
        }
        terms.open(ws, h.name);
        go("terminal");
        return true;
      }
      case "logs":
      case "vault":
      case "docs":
      case "topology":
        go(head.slice(1).toLowerCase());
        return true;
    }
    return null;
  }

  async function send(text: string) {
    const sh = parseShell(text);
    if (sh) {
      if (!sh.command) return false;
      try {
        stickRef.current = true;
        await api.shell(ws, sid, sh.command, sh.target || undefined);
      } catch (e) {
        toast.error("Command failed", errMsg(e));
        return false;
      }
      return true;
    }
    if (text.startsWith("/")) {
      const r = await command(text);
      if (r !== null) return r;
    }
    try {
      stickRef.current = true;
      await api.sendMessage(ws, sid, text);
      setSession((s) => (s ? { ...s, state: "running" } : s));
      return true;
    } catch (e) {
      toast.error("Send failed", errMsg(e));
      return false;
    }
  }

  async function stop() {
    try {
      await api.interrupt(ws, sid);
    } catch (e) {
      toast.error("Interrupt failed", errMsg(e));
    }
  }

  if (loadErr && !session) {
    return (
      <EmptyState icon={<CircleSlash />} title="Cannot load session" className="flex-1" action={<Button onClick={() => void load()}>Retry</Button>}>
        {loadErr}
      </EmptyState>
    );
  }

  const stateText =
    state === "running" ? "Working…" : state === "waiting_approval" ? "Waiting for your approval" : state === "waiting_input" ? "Waiting for your answer" : "Idle";
  return (
    <div className="flex flex-col h-full min-h-0">
      <div className="flex items-center gap-3 px-4 h-12 border-b border-line bg-panel shrink-0">
        <StateDot state={state} />
        <div className="min-w-0 flex-1 flex items-center gap-2">
          <div dir="auto" className="truncate font-medium">
            {session?.title || "Untitled session"}
          </div>
          {session?.headless && (
            <Badge tone="info" title="Started by opsdeck run: nothing waits for an approval, what would ask is denied">
              headless
            </Badge>
          )}
          {session?.forkedFrom && (
            <Link to={`/w/${encodeURIComponent(ws)}/agent/${encodeURIComponent(session.forkedFrom)}`} className="text-faint hover:text-fg" title="Forked from this session">
              <GitFork className="size-3.5" />
            </Link>
          )}
        </div>
        <span className="text-xs text-muted">{stateText}</span>
        {session && (
          <span className="text-[11px] text-faint tabular-nums" title="Tokens in / out">
            {fmtTokens(session.usage?.inputTokens)} in · {fmtTokens(session.usage?.outputTokens)} out
          </span>
        )}
        {!connected && <Badge tone="warn">reconnecting</Badge>}
        <TasksButton ws={ws} sid={sid} tasks={tasks} open={tasksOpen} setOpen={setTasksOpen} />
        <Segmented
          size="xs"
          value={toolView}
          onChange={setToolView}
          options={[
            { id: "grouped", label: "Grouped", title: "Tool calls collapsed into groups" },
            { id: "open", label: "Detailed", title: "Every tool call shown" },
          ]}
        />
        <div className="relative">
          <IconButton title="Session" onClick={() => setMenu((m) => !m)}>
            {busy ? <Loader2 className="size-4 animate-spin" /> : <MoreHorizontal className="size-4" />}
          </IconButton>
          <Popover open={menu} onClose={() => setMenu(false)} className="right-0 mt-1 w-72">
            <MenuItem icon={<GitFork />} onClick={() => (setMenu(false), void fork())} hint="/fork">
              Fork session
            </MenuItem>
            <MenuItem icon={<Minimize2 />} onClick={() => (setMenu(false), void compact())} hint="/compact">
              Compact the conversation
            </MenuItem>
            <div className="border-t border-line my-1" />
            <div className="px-3 py-1 text-[10.5px] uppercase tracking-wide text-faint">Save to the workspace</div>
            <MenuItem icon={<FileClock />} onClick={() => (setMenu(false), void exportAs("report"))} hint="transcript">
              As a report
            </MenuItem>
            <MenuItem icon={<BookOpen />} onClick={() => (setMenu(false), void exportAs("runbook"))} hint="docs/runbooks">
              As a runbook
            </MenuItem>
            <MenuItem icon={<Sparkles />} onClick={() => (setMenu(false), void exportAs("skill"))} hint="skills/">
              As a skill
            </MenuItem>
          </Popover>
        </div>
      </div>
      <div
        ref={scrollRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
        className="flex-1 min-h-0 overflow-y-auto"
      >
        <div className="max-w-[880px] mx-auto px-5 py-5 space-y-4">
          {!session && (
            <div className="flex justify-center py-10">
              <Loader2 className="size-5 animate-spin text-muted" />
            </div>
          )}
          {session && timeline.length === 0 && <div className="text-center text-muted py-10">Send a message to start.</div>}
          {blocks.map((t, bi) => {
            switch (t.kind) {
              case "user":
                return <UserMsg key={t.id} text={t.text} command={t.command} onFork={t.forkable && !running ? () => void fork(t.id) : undefined} />;
              case "notification":
                return <NotificationMsg key={t.id} text={t.text} />;
              case "shell":
                return <ShellMsg key={t.id} ev={t.ev} />;
              case "recall":
                return <Recall key={t.id} items={t.items} preloaded={t.preloaded} />;
              case "assistant":
                return <AssistantMsg key={t.id} text={t.text} streaming={t.streaming} />;
              case "tools":
                return <ToolGroup key={t.id} items={t.items} ctx={ctx} live={running && bi === blocks.length - 1} expandAll={toolView === "open"} />;
              case "error":
                return (
                  <div key={t.id} className="flex gap-2 rounded-lg border border-err/30 bg-err/8 px-3 py-2.5 text-err text-[13px]">
                    <AlertTriangle className="size-4 shrink-0 mt-0.5" />
                    <div dir="auto" className="whitespace-pre-wrap break-words text-start">
                      {t.message}
                    </div>
                  </div>
                );
              case "compaction":
                return <Compaction key={t.id} summary={t.summary} />;
            }
            return null;
          })}
          {state === "running" && !timeline.some((t) => t.kind === "assistant" && t.streaming) && blocks[blocks.length - 1]?.kind !== "tools" && (
            <div className="flex items-center gap-2 text-muted text-xs pl-9">
              <Loader2 className="size-3.5 animate-spin" /> Thinking…
            </div>
          )}
        </div>
      </div>
      <div className="shrink-0 px-5 pb-4 pt-2">
        <div className="max-w-[880px] mx-auto">
          {showTodo && <TodoPanel items={todo!} running={running} />}
          {wait.approvals + wait.questions > 0 && (
            <button
              onClick={() => document.querySelector("[data-pending-approval]")?.scrollIntoView({ behavior: "smooth", block: "center" })}
              className="mb-2 w-full flex items-center gap-2 rounded-lg border border-warn/40 bg-warn/10 px-3 h-9 text-warn text-xs font-medium"
            >
              <ShieldAlert className="size-4" />
              {wait.questions > 0 && wait.approvals === 0
                ? `The agent asks you a question — click to answer`
                : `${wait.approvals + wait.questions} item${wait.approvals + wait.questions > 1 ? "s" : ""} waiting for you — click to review`}
            </button>
          )}
          <Composer
            onSend={send}
            running={running}
            onStop={stop}
            disabled={!session}
            seed={seed}
            commands={commands}
            hosts={hosts.map((h) => h.name)}
            shell
            left={
              session && (
                <>
                  <ModePicker value={session.mode} onChange={(m) => void updateSession({ mode: m })} />
                  <ModelPicker models={models} value={session.model} onChange={(m) => void updateSession({ model: m })} />
                </>
              )
            }
          />
        </div>
      </div>
    </div>
  );
}

const RECALL_ICON = { skill: Sparkles, doc: FileText, report: FileClock, script: ScrollText, memory: BookOpen } as const;

function recallPath(it: RecallItem): string {
  switch (it.kind) {
    case "skill":
      return "skills?skill=" + encodeURIComponent(it.ref);
    case "script":
      return "scripts?script=" + encodeURIComponent(it.ref);
    default:
      return "docs?path=" + encodeURIComponent(it.ref);
  }
}

/** the workspace knowledge opsdeck attached to the message above */
function Recall({ items, preloaded }: { items: RecallItem[]; preloaded?: string }) {
  const openInTab = useOpenInTab();
  return (
    <div className="flex items-start gap-2 pl-9 -mt-2 text-[11.5px] text-muted">
      <span className="flex items-center gap-1 h-5 shrink-0" title="Skills, docs, reports and scripts of this workspace that match the message; given to the agent with it">
        <BookOpen className="size-3.5" /> Knowledge
      </span>
      <div className="flex flex-wrap gap-1 min-w-0">
        {items.map((it) => {
          const Icon = RECALL_ICON[it.kind] ?? FileText;
          const label = it.kind === "skill" || it.kind === "script" ? it.ref : it.title || it.ref;
          return (
            <a
              key={it.kind + it.ref}
              href="#"
              {...openInTab(recallPath(it))}
              title={`${it.kind}: ${it.ref}${preloaded === it.ref ? " — included in full" : ""}`}
              className="inline-flex items-center gap-1 h-5 max-w-[260px] rounded border border-line bg-panel-2 px-1.5 hover:text-fg hover:border-line-strong"
            >
              <Icon className="size-3 shrink-0" />
              <span className="truncate">{label}</span>
              {preloaded === it.ref && <Check className="size-3 shrink-0 text-ok" />}
            </a>
          );
        })}
      </div>
    </div>
  );
}

function UserMsg({ text, command, onFork }: { text: string; command?: string; onFork?: () => void }) {
  return (
    <div className="group flex gap-3">
      <span className="size-6 rounded-md bg-panel-3 grid place-items-center shrink-0 mt-0.5">
        <User className="size-3.5 text-muted" />
      </span>
      <div
        dir={textDir(text)}
        className="relative flex-1 min-w-0 rounded-lg bg-panel-2 border border-line px-3.5 py-2.5 whitespace-pre-wrap break-words text-[13.5px] text-start leading-relaxed"
      >
        {command ? <UserCommand text={text} command={command} /> : text}
        {onFork && (
          <button
            onClick={onFork}
            title="Fork from here: a new session with everything before this message, and this message to edit and send"
            className="absolute -top-2.5 end-2 hidden group-hover:flex items-center gap-1 h-5 px-1.5 rounded border border-line-strong bg-panel text-[10.5px] text-muted hover:text-fg"
          >
            <GitFork className="size-3" /> Fork from here
          </button>
        )}
      </div>
    </div>
  );
}

function AssistantMsg({ text, streaming }: { text: string; streaming: boolean }) {
  return (
    <div className="flex gap-3">
      <span className="size-6 rounded-md bg-agent text-agent-fg grid place-items-center shrink-0 mt-0.5">
        <Bot className="size-3.5 text-agent-fg" />
      </span>
      <div className="flex-1 min-w-0 text-[13.5px] pt-0.5">
        {text ? <Markdown dir={textDir(text) === "rtl" ? "rtl" : "auto"}>{text}</Markdown> : <span className="text-faint">…</span>}
        {streaming && <span className="inline-block w-1.5 h-4 bg-agent-ink/80 ml-0.5 align-text-bottom pulse-dot" />}
      </div>
    </div>
  );
}

function Compaction({ summary }: { summary: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="text-xs text-faint">
      <button onClick={() => setOpen((o) => !o)} className="flex items-center gap-2 w-full">
        <span className="h-px flex-1 bg-line" />
        <FileText className="size-3.5" /> Earlier conversation summarized {open ? "▴" : "▾"}
        <span className="h-px flex-1 bg-line" />
      </button>
      {open && (
        <div className="mt-2 rounded-md border border-line bg-panel p-3 text-muted">
          <Markdown dir={textDir(summary) === "rtl" ? "rtl" : "auto"}>{summary}</Markdown>
        </div>
      )}
    </div>
  );
}
