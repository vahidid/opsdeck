import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  AlertTriangle,
  ArrowUp,
  BookOpen,
  Bot,
  Check,
  ChevronDown,
  ChevronRight,
  CircleSlash,
  Cpu,
  Eye,
  FileText,
  FileClock,
  ScrollText,
  Hand,
  KeyRound,
  Loader2,
  MessageSquarePlus,
  MoreHorizontal,
  Pencil,
  Rocket,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Square,
  Trash2,
  User,
  Wrench,
  SquareArrowOutUpRight,
  X,
  Zap,
} from "lucide-react";
import { ApiError, api, errMsg } from "../api/client";
import type { AgentEvent, ModelInfo, RecallItem, SessionInfo, SessionMode, SessionState } from "../api/types";
import { useWs } from "../context/app";
import { useOpenInTab, useTabTitle, useTabs } from "../context/tabs";
import { Badge, Button, CodeBlock, EmptyState, IconButton, Input, Segmented, Select, StatusDot, cx, riskTone } from "../components/ui";
import { MenuItem, Modal, Popover, useConfirm, useToast } from "../components/overlay";
import { Markdown } from "../components/Markdown";
import { textDir } from "../lib/rtl";
import { useAsync, useLocalStorage } from "../hooks";
import { fmtMs, fmtTokens, timeAgo } from "../lib/format";

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

  const defaultModel = useMemo(() => {
    const list = models.data ?? [];
    const pref = workspace?.settings?.defaultModel || settings.data?.defaultModel;
    return list.find((m) => m.id === pref)?.id ?? list[0]?.id ?? "";
  }, [models.data, settings.data, workspace]);

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

  return (
    <div className="flex h-full min-h-0">
      <aside className="w-64 shrink-0 border-r border-line bg-panel flex flex-col">
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
              className={cx(
                "group relative flex items-center gap-2 rounded-md px-2 py-1.5 cursor-pointer",
                s.id === sid ? "bg-panel-3" : "hover:bg-panel-2",
              )}
            >
              <StateDot state={s.state} />
              <div className="min-w-0 flex-1">
                <div dir="auto" className={cx("truncate text-[12.5px] text-start", s.id === sid ? "text-fg" : "text-muted group-hover:text-fg")}>
                  {s.title || "Untitled session"}
                </div>
                <div className="text-[10.5px] text-faint flex gap-1.5">
                  <span>{timeAgo(s.updated)}</span>
                  <span className="uppercase">{s.mode}</span>
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
          <SessionView
            key={sid}
            ws={ws}
            sid={sid}
            models={models.data ?? []}
            onChanged={() => void sessions.reload()}
          />
        ) : (
          <NewSessionView
            ws={ws}
            models={models.data ?? []}
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
  if (state === "waiting_approval") return <StatusDot tone="warn" pulse />;
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
      Add an Anthropic or OpenAI-compatible provider (OpenAI, OpenRouter, Ollama, LM Studio…). API keys are stored in your vault. Everything else
      in opsdeck works without a model.
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
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)} className="h-7 text-xs w-auto max-w-56 py-0">
      {!models.some((m) => m.id === value) && value && <option value={value}>{value}</option>}
      {models.map((m) => (
        <option key={m.id} value={m.id}>
          {m.label || m.id}
        </option>
      ))}
    </Select>
  );
}

// ---------------------------------------------------------------- composer

function Composer({
  onSend,
  disabled,
  running,
  onStop,
  left,
  placeholder,
  autoFocus,
}: {
  onSend: (text: string) => Promise<boolean> | boolean;
  disabled?: boolean;
  running?: boolean;
  onStop?: () => void;
  left?: React.ReactNode;
  placeholder?: string;
  autoFocus?: boolean;
}) {
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(260, el.scrollHeight)}px`;
  }, [text]);
  useEffect(() => {
    if (autoFocus) ref.current?.focus();
  }, [autoFocus]);
  async function send() {
    const t = text.trim();
    if (!t || disabled || running || sending) return;
    setSending(true);
    try {
      const ok = await onSend(t);
      if (ok !== false) setText("");
    } finally {
      setSending(false);
      ref.current?.focus();
    }
  }
  return (
    <div className="rounded-xl border border-line-strong bg-panel shadow-lg shadow-black/10 focus-within:border-accent/60 transition-colors">
      <textarea
        ref={ref}
        dir="auto"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            void send();
          }
        }}
        rows={1}
        placeholder={placeholder ?? "Ask the agent to investigate or change something…  (Enter to send, Shift+Enter for newline)"}
        className="w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-[13.5px] leading-relaxed outline-none placeholder:text-faint"
        disabled={disabled}
      />
      <div className="flex items-center gap-2 px-2 pb-2">
        {left}
        <div className="flex-1" />
        <span className="text-[10.5px] text-faint hidden md:inline">/plan /ask /auto /full to switch mode</span>
        {running ? (
          <Button size="sm" variant="danger" icon={<Square className="size-3 fill-current" />} onClick={onStop}>
            Stop
          </Button>
        ) : (
          <button
            onClick={() => void send()}
            disabled={!text.trim() || disabled || sending}
            className="size-8 rounded-lg bg-accent text-accent-fg grid place-items-center disabled:opacity-30 hover:brightness-110 transition"
            aria-label="Send"
          >
            {sending ? <Loader2 className="size-4 animate-spin" /> : <ArrowUp className="size-4" />}
          </button>
        )}
      </div>
    </div>
  );
}

function parseModeCommand(t: string): SessionMode | null {
  const m = /^\/(plan|ask|auto|full)\s*$/i.exec(t.trim());
  return m ? (m[1].toLowerCase() as SessionMode) : null;
}

// ---------------------------------------------------------------- new session

const EXAMPLES = [
  { icon: <ShieldCheck className="size-4" />, text: "Check disk, memory and load on all prod hosts and summarize anything above 80%." },
  { icon: <AlertTriangle className="size-4" />, text: "nginx on app-1 returns 502. Investigate the logs, upstream and config, and tell me the root cause." },
  { icon: <Wrench className="size-4" />, text: "List containers on every docker host and flag the ones that are restarting or unhealthy." },
  { icon: <Sparkles className="size-4" />, text: "Pull the latest image of our API from GitLab and run it as a single container on the test server." },
];

function NewSessionView({
  ws,
  models,
  defaultModel,
  onCreated,
}: {
  ws: string;
  models: ModelInfo[];
  defaultModel: string;
  onCreated: (s: SessionInfo) => void;
}) {
  const { workspace, resources } = useWs();
  const [mode, setMode] = useState<SessionMode>("ask");
  const [model, setModel] = useState(defaultModel);
  const toast = useToast();
  const [seed, setSeed] = useState("");
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
          <span className="size-9 rounded-lg grid place-items-center bg-accent/15 text-accent">
            <Bot className="size-5" />
          </span>
          <div>
            <h1 className="text-[18px] font-semibold">What should we do in {workspace?.title || ws}?</h1>
            <div className="text-muted text-xs">
              The agent knows this workspace: {resources.filter((r) => r.kind === "Host").length} hosts, its networks, docs and memory. Secrets stay
              in the vault.
            </div>
          </div>
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
        <SeededComposer
          key={seed}
          seed={seed}
          onSend={start}
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

function SeededComposer({ seed, ...rest }: { seed: string } & React.ComponentProps<typeof Composer>) {
  // Composer manages its own text; prefill by remounting with a seed
  const wrapRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!seed) return;
    const ta = wrapRef.current?.querySelector("textarea");
    if (ta) {
      const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set;
      setter?.call(ta, seed);
      ta.dispatchEvent(new Event("input", { bubbles: true }));
      ta.focus();
    }
  }, [seed]);
  return (
    <div ref={wrapRef}>
      <Composer {...rest} autoFocus />
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
    if (e.type === "status" || byId.has(e.id)) continue;
    if (e.type === "assistant_delta" && finished.has(e.messageId)) continue;
    if (e.type === "assistant" && server.some((x) => x.type === "assistant" && x.messageId === e.messageId)) continue;
    byId.set(e.id, e);
  }
  return [...byId.values()].sort((a, b) => eventSeq(a) - eventSeq(b));
}

type Timeline =
  | { kind: "user"; id: string; text: string }
  | { kind: "recall"; id: string; items: RecallItem[]; preloaded?: string }
  | { kind: "assistant"; id: string; text: string; streaming: boolean }
  | { kind: "tool"; id: string; call?: Extract<AgentEvent, { type: "tool_call" }>; result?: Extract<AgentEvent, { type: "tool_result" }>; approval?: Extract<AgentEvent, { type: "approval_request" }>; resolved?: Extract<AgentEvent, { type: "approval_resolved" }> }
  | { kind: "error"; id: string; message: string }
  | { kind: "compaction"; id: string; summary: string };

type ToolItem = Extract<Timeline, { kind: "tool" }>;

function SessionView({ ws, sid, models, onChanged }: { ws: string; sid: string; models: ModelInfo[]; onChanged: () => void }) {
  const toast = useToast();
  const [session, setSession] = useState<SessionInfo | null>(null);
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [deltas, setDeltas] = useState<Record<string, string>>({});
  const [loadErr, setLoadErr] = useState("");
  const [connected, setConnected] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const stickRef = useRef(true);
  const onChangedRef = useRef(onChanged);
  onChangedRef.current = onChanged;

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
      if (ev.type === "assistant_delta") {
        setDeltas((d) => ({ ...d, [ev.messageId]: (d[ev.messageId] ?? "") + ev.text }));
        setEvents((list) =>
          list.some((x) => (x.type === "assistant" || x.type === "assistant_delta") && x.messageId === ev.messageId)
            ? list
            : [...list, { ...ev, text: "" }],
        );
        return;
      }
      if (ev.type === "status") {
        setSession((s) => (s ? { ...s, state: ev.state, usage: ev.usage ?? s.usage } : s));
        if (ev.state === "idle") onChangedRef.current();
      }
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

  const timeline = useMemo<Timeline[]>(() => {
    const out: Timeline[] = [];
    const tools = new Map<string, Extract<Timeline, { kind: "tool" }>>();
    const toolFor = (callId: string) => {
      let t = tools.get(callId);
      if (!t) {
        t = { kind: "tool", id: callId };
        tools.set(callId, t);
        out.push(t);
      }
      return t;
    };
    const approvalToCall = new Map<string, string>();
    for (const e of events) {
      switch (e.type) {
        case "user":
          out.push({ kind: "user", id: e.id, text: e.text });
          break;
        case "recall":
          if (e.items?.length) out.push({ kind: "recall", id: e.id, items: e.items, preloaded: e.preloaded });
          break;
        case "assistant":
          if (e.text?.trim()) out.push({ kind: "assistant", id: e.messageId || e.id, text: e.text, streaming: false });
          break;
        case "assistant_delta":
          out.push({ kind: "assistant", id: e.messageId, text: deltas[e.messageId] ?? "", streaming: true });
          break;
        case "tool_call":
          toolFor(e.callId).call = e;
          break;
        case "tool_result":
          toolFor(e.callId).result = e;
          break;
        case "approval_request":
          approvalToCall.set(e.approvalId, e.callId);
          toolFor(e.callId).approval = e;
          break;
        case "approval_resolved": {
          const cid = approvalToCall.get(e.approvalId);
          if (cid) toolFor(cid).resolved = e;
          break;
        }
        case "error":
          out.push({ kind: "error", id: e.id, message: e.message });
          break;
        case "compaction":
          out.push({ kind: "compaction", id: e.id, summary: e.summary });
          break;
      }
    }
    return out;
  }, [events, deltas]);

  // auto-scroll when at bottom
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickRef.current) el.scrollTop = el.scrollHeight;
  }, [timeline]);

  // consecutive tool calls are shown as one group (like Claude Code)
  const blocks = useMemo(() => {
    const out: (Exclude<Timeline, { kind: "tool" }> | { kind: "tools"; id: string; items: ToolItem[] })[] = [];
    for (const t of timeline) {
      if (t.kind === "tool") {
        const last = out[out.length - 1];
        if (last?.kind === "tools") last.items.push(t);
        else out.push({ kind: "tools", id: "g-" + t.id, items: [t] });
      } else out.push(t);
    }
    return out;
  }, [timeline]);
  const [toolView, setToolView] = useLocalStorage<"grouped" | "open">("opsdeck.agent.toolView", "grouped");

  const state = session?.state ?? "idle";
  const running = state === "running" || state === "waiting_approval";
  const pendingApprovals = timeline.filter((t) => t.kind === "tool" && t.approval && !t.resolved).length;

  async function send(text: string) {
    const m = parseModeCommand(text);
    if (m) {
      await updateSession({ mode: m });
      return true;
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

  async function updateSession(b: { mode?: SessionMode; model?: string }) {
    try {
      const s = await api.updateSession(ws, sid, b);
      setSession(s);
      onChangedRef.current();
    } catch (e) {
      toast.error("Update failed", errMsg(e));
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

  return (
    <div className="flex flex-col h-full min-h-0">
      <div className="flex items-center gap-3 px-4 h-12 border-b border-line bg-panel shrink-0">
        <StateDot state={state} />
        <div className="min-w-0 flex-1">
          <div dir="auto" className="truncate font-medium">{session?.title || "Untitled session"}</div>
        </div>
        <span className="text-xs text-muted">
          {state === "running" ? "Working…" : state === "waiting_approval" ? "Waiting for your approval" : "Idle"}
        </span>
        {session && (
          <span className="text-[11px] text-faint tabular-nums" title="Tokens in / out">
            {fmtTokens(session.usage?.inputTokens)} in · {fmtTokens(session.usage?.outputTokens)} out
          </span>
        )}
        {!connected && <Badge tone="warn">reconnecting</Badge>}
        <Segmented
          size="xs"
          value={toolView}
          onChange={setToolView}
          options={[
            { id: "grouped", label: "Grouped", title: "Tool calls collapsed into groups" },
            { id: "open", label: "Detailed", title: "Every tool call shown" },
          ]}
        />
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
          {!session && <div className="flex justify-center py-10"><Loader2 className="size-5 animate-spin text-muted" /></div>}
          {session && timeline.length === 0 && <div className="text-center text-muted py-10">Send a message to start.</div>}
          {blocks.map((t, bi) => {
            switch (t.kind) {
              case "user":
                return <UserMsg key={t.id} text={t.text} />;
              case "recall":
                return <Recall key={t.id} items={t.items} preloaded={t.preloaded} />;
              case "assistant":
                return <AssistantMsg key={t.id} text={t.text} streaming={t.streaming} />;
              case "tools":
                return <ToolGroup key={t.id} items={t.items} ws={ws} sid={sid} live={running && bi === blocks.length - 1} expandAll={toolView === "open"} />;
              case "error":
                return (
                  <div key={t.id} className="flex gap-2 rounded-lg border border-err/30 bg-err/8 px-3 py-2.5 text-err text-[13px]">
                    <AlertTriangle className="size-4 shrink-0 mt-0.5" />
                    <div dir="auto" className="whitespace-pre-wrap break-words text-start">{t.message}</div>
                  </div>
                );
              case "compaction":
                return <Compaction key={t.id} summary={t.summary} />;
            }
          })}
          {state === "running" && !timeline.some((t) => t.kind === "assistant" && t.streaming) && (
            <div className="flex items-center gap-2 text-muted text-xs pl-9">
              <Loader2 className="size-3.5 animate-spin" /> Thinking…
            </div>
          )}
        </div>
      </div>
      <div className="shrink-0 px-5 pb-4 pt-2">
        <div className="max-w-[880px] mx-auto">
          {pendingApprovals > 0 && (
            <button
              onClick={() => document.querySelector("[data-pending-approval]")?.scrollIntoView({ behavior: "smooth", block: "center" })}
              className="mb-2 w-full flex items-center gap-2 rounded-lg border border-warn/40 bg-warn/10 px-3 h-9 text-warn text-xs font-medium"
            >
              <ShieldAlert className="size-4" /> {pendingApprovals} action{pendingApprovals > 1 ? "s" : ""} waiting for your approval — click to review
            </button>
          )}
          <Composer
            onSend={send}
            running={running}
            onStop={stop}
            disabled={!session}
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

function UserMsg({ text }: { text: string }) {
  return (
    <div className="flex gap-3">
      <span className="size-6 rounded-md bg-panel-3 grid place-items-center shrink-0 mt-0.5">
        <User className="size-3.5 text-muted" />
      </span>
      <div
        dir={textDir(text)}
        className="flex-1 min-w-0 rounded-lg bg-panel-2 border border-line px-3.5 py-2.5 whitespace-pre-wrap break-words text-[13.5px] text-start leading-relaxed"
      >
        {text}
      </div>
    </div>
  );
}

function AssistantMsg({ text, streaming }: { text: string; streaming: boolean }) {
  return (
    <div className="flex gap-3">
      <span className="size-6 rounded-md bg-accent/15 grid place-items-center shrink-0 mt-0.5">
        <Bot className="size-3.5 text-accent" />
      </span>
      <div className="flex-1 min-w-0 text-[13.5px] pt-0.5">
        {text ? <Markdown dir={textDir(text) === "rtl" ? "rtl" : "auto"}>{text}</Markdown> : <span className="text-faint">…</span>}
        {streaming && <span className="inline-block w-1.5 h-4 bg-accent/70 ml-0.5 align-text-bottom pulse-dot" />}
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

// ---------------------------------------------------------------- tool card

function inputCommand(input: unknown): string | null {
  if (input && typeof input === "object") {
    const o = input as Record<string, unknown>;
    if (typeof o.command === "string") return o.command;
    if (typeof o.cmd === "string") return o.cmd;
  }
  return null;
}

function inputSummary(tool: string, input: unknown): string {
  if (!input || typeof input !== "object") return "";
  const o = input as Record<string, unknown>;
  const cmd = inputCommand(input);
  if (cmd) return cmd;
  for (const k of ["path", "playbook", "action", "query", "url", "name", "resource", "target", "module"]) {
    if (typeof o[k] === "string" && o[k]) return `${k === "action" ? "" : k + ": "}${String(o[k])}`;
  }
  void tool;
  return "";
}

// ToolGroup is a run of tool calls between two messages: one line with
// what ran and how it went; open, one compact line per call. It stays open
// while it is running or waits for an approval.
function ToolGroup({ items, ws, sid, live, expandAll }: { items: ToolItem[]; ws: string; sid: string; live: boolean; expandAll: boolean }) {
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const pending = items.filter((t) => t.approval && !t.resolved).length;
  const failed = items.filter((t) => t.result?.isError || t.resolved?.decision === "deny").length;
  const runningCount = items.filter((t) => !t.result && !(t.resolved?.decision === "deny")).length;
  if (items.length === 1 && !expandAll) return <ToolCard item={items[0]} ws={ws} sid={sid} />;
  const open = expandAll || pending > 0 || (userOpen ?? live);
  const counts = new Map<string, number>();
  for (const t of items) {
    const n = t.call?.tool ?? t.approval?.tool ?? "tool";
    counts.set(n, (counts.get(n) ?? 0) + 1);
  }
  const total = items.reduce((n, t) => n + (t.result?.durationMs ?? 0), 0);
  return (
    <div className={cx("ml-9 rounded-lg border bg-panel overflow-hidden", pending ? "border-warn/50" : failed ? "border-err/30" : "border-line")}>
      <button onClick={() => setUserOpen(!open)} className="w-full flex items-center gap-2 px-3 h-9 text-left hover:bg-panel-2">
        {open ? <ChevronDown className="size-3.5 text-faint" /> : <ChevronRight className="size-3.5 text-faint" />}
        <Wrench className="size-3.5 text-muted" />
        <span className="text-[12.5px] font-medium">{items.length} tool calls</span>
        <span className="flex-1 min-w-0 truncate font-mono text-[11.5px] text-muted">
          {Array.from(counts)
            .map(([n, c]) => (c > 1 ? `${n} ×${c}` : n))
            .join(" · ")}
        </span>
        {pending > 0 && <Badge tone="warn">{pending} waiting for you</Badge>}
        {failed > 0 && <Badge tone="err">{failed} failed</Badge>}
        {total > 0 && <span className="text-[10.5px] text-faint tabular-nums">{fmtMs(total)}</span>}
        {runningCount > 0 && live ? <Loader2 className="size-3.5 animate-spin text-info" /> : failed === 0 && pending === 0 ? <Check className="size-3.5 text-ok" /> : null}
      </button>
      {open && (
        <div className="border-t border-line divide-y divide-line">
          {items.map((t) => (
            <ToolCard key={t.id} item={t} ws={ws} sid={sid} nested />
          ))}
        </div>
      )}
    </div>
  );
}

/** a short summary of a tool's output for its collapsed line */
function outputSummary(output: string | undefined, isError: boolean): string {
  if (!output) return "";
  const lines = output.split("\n").filter((l) => l.trim() !== "");
  const exit = /^exit code: (-?\d+)/.exec(output);
  const first = lines.find((l) => !l.startsWith("exit code:") && !/^(stdout|stderr):$/.test(l.trim())) ?? "";
  const parts = [];
  if (exit) parts.push(`exit ${exit[1]}`);
  else if (isError) parts.push(first.replace(/^error:\s*/, "").slice(0, 80));
  else if (first) parts.push(first.slice(0, 60));
  if (lines.length > 1) parts.push(`${lines.length} lines`);
  return parts.join(" · ");
}

function ToolCard({ item, ws, sid, nested }: { item: ToolItem; ws: string; sid: string; nested?: boolean }) {
  const { call, result, approval, resolved } = item;
  const tool = call?.tool ?? approval?.tool ?? "tool";
  const input = call?.input ?? approval?.input;
  const risk = call?.risk ?? approval?.risk;
  const target = call?.target || approval?.target;
  const env = call?.environment || approval?.environment;
  const pending = !!approval && !resolved;
  const denied = resolved && resolved.decision === "deny";
  const done = !!result;
  const failed = result?.isError;
  const [open, setOpen] = useState<boolean | null>(null);
  const expanded = open ?? (pending || !!failed || !!denied);
  const cmd = inputCommand(input);
  const summary = inputSummary(tool, input);

  let statusEl: React.ReactNode;
  if (pending) statusEl = <Badge tone="warn">needs approval</Badge>;
  else if (denied) statusEl = <Badge tone="err">denied</Badge>;
  else if (!done) statusEl = <Loader2 className="size-3.5 animate-spin text-info" />;
  else if (failed) statusEl = <X className="size-3.5 text-err" />;
  else statusEl = <Check className="size-3.5 text-ok" />;

  return (
    <div
      className={cx(
        nested ? "bg-panel" : "ml-9 rounded-lg border bg-panel overflow-hidden",
        !nested && (pending ? (risk === "destructive" ? "border-err/50 ring-1 ring-err/20" : "border-warn/50 ring-1 ring-warn/20") : failed || denied ? "border-err/30" : "border-line"),
        nested && pending && "ring-1 ring-inset ring-warn/40",
      )}
      {...(pending ? { "data-pending-approval": true } : {})}
    >
      <button onClick={() => setOpen(!expanded)} className={cx("w-full flex items-center gap-2 px-3 text-left hover:bg-panel-2", nested ? "h-8" : "h-9")}>
        {expanded ? <ChevronDown className="size-3.5 text-faint" /> : <ChevronRight className="size-3.5 text-faint" />}
        <Wrench className="size-3.5 text-muted" />
        <span className="font-mono text-[12px] font-medium">{tool}</span>
        {target && (
          <Badge mono className="max-w-48 truncate">
            {target}
          </Badge>
        )}
        <span className="font-mono text-[11.5px] text-muted truncate flex-1 min-w-0">{summary}</span>
        {result && !expanded && (
          <span className={cx("hidden md:inline font-mono text-[11px] truncate max-w-56", result.isError ? "text-err" : "text-faint")}>{outputSummary(result.output, result.isError)}</span>
        )}
        {env && <Badge tone={/prod/i.test(env) ? "err" : "neutral"}>{env}</Badge>}
        {risk && <Badge tone={riskTone(risk)}>{risk}</Badge>}
        {result?.durationMs !== undefined && <span className="text-[10.5px] text-faint tabular-nums">{fmtMs(result.durationMs)}</span>}
        {statusEl}
      </button>
      {expanded && (
        <div className="border-t border-line px-3 py-2.5 space-y-2.5">
          {cmd ? (
            <div className="rounded-md bg-panel-2 border border-line px-3 py-2 font-mono text-[12px] whitespace-pre-wrap break-all">
              <span className="text-accent select-none">$ </span>
              {cmd}
            </div>
          ) : (
            input !== undefined && <CodeBlock maxHeight={240}>{JSON.stringify(input, null, 2)}</CodeBlock>
          )}
          {approval && <ApprovalBox ws={ws} sid={sid} approval={approval} resolved={resolved} />}
          {tool === "ansible_run" && !pending && !denied && <AnsibleRunLink ws={ws} output={result?.output} />}
          {result && <ToolOutput output={result.output} isError={result.isError} />}
        </div>
      )}
    </div>
  );
}

// AnsibleRunLink opens the run's live log on the Ansible page.
function AnsibleRunLink({ ws, output }: { ws: string; output?: string }) {
  const id = output?.match(/^run (\S+):/m)?.[1];
  return (
    <Link
      to={`/w/${encodeURIComponent(ws)}/ansible?tab=runs${id ? `&run=${encodeURIComponent(id)}` : ""}`}
      className="inline-flex items-center gap-1.5 text-xs text-accent hover:underline"
    >
      <SquareArrowOutUpRight className="size-3.5" />
      {id ? "Full log on the Ansible page" : "Follow the live log on the Ansible page"}
    </Link>
  );
}

function ToolOutput({ output, isError }: { output: string; isError: boolean }) {
  const [all, setAll] = useState(false);
  const lines = (output ?? "").split("\n");
  const long = lines.length > 40;
  const shown = all || !long ? output : lines.slice(0, 40).join("\n");
  return (
    <div>
      <div className="text-[10.5px] uppercase tracking-wide text-faint mb-1">{isError ? "Error" : "Output"}</div>
      <CodeBlock maxHeight={all ? 640 : 420} tone={isError ? "err" : undefined}>
        {shown}
      </CodeBlock>
      {long && (
        <button onClick={() => setAll((a) => !a)} className="mt-1 text-[11px] text-accent hover:underline">
          {all ? "Show less" : `Show all ${lines.length} lines`}
        </button>
      )}
    </div>
  );
}

function DiffPreview({ text }: { text: string }) {
  const isDiff = /^(---|\+\+\+|@@)/m.test(text);
  if (!isDiff) return <CodeBlock maxHeight={320}>{text}</CodeBlock>;
  return (
    <pre className="rounded-md border border-line bg-panel-2 py-2 font-mono text-[11.5px] leading-relaxed overflow-auto max-h-[360px]">
      {text.split("\n").map((l, i) => (
        <div
          key={i}
          className={cx(
            "px-3 whitespace-pre-wrap break-all",
            l.startsWith("+") && !l.startsWith("+++") && "bg-ok/10 text-ok",
            l.startsWith("-") && !l.startsWith("---") && "bg-err/10 text-err",
            l.startsWith("@@") && "text-info",
            (l.startsWith("+++") || l.startsWith("---")) && "text-muted font-semibold",
          )}
        >
          {l || " "}
        </div>
      ))}
    </pre>
  );
}

function ApprovalBox({
  ws,
  sid,
  approval,
  resolved,
}: {
  ws: string;
  sid: string;
  approval: Extract<AgentEvent, { type: "approval_request" }>;
  resolved?: Extract<AgentEvent, { type: "approval_resolved" }>;
}) {
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const [denyOpen, setDenyOpen] = useState(false);
  const [note, setNote] = useState("");
  const [local, setLocal] = useState<string | null>(null);
  const [secret, setSecret] = useState("");
  const [secretErr, setSecretErr] = useState("");
  const decision = resolved?.decision ?? local;
  const asks = approval.secret;

  async function decide(d: "approve" | "deny" | "always") {
    if (asks && d !== "deny" && !secret) {
      setSecretErr("Enter the password, or deny.");
      return;
    }
    setBusy(d);
    setSecretErr("");
    try {
      await api.approve(ws, sid, approval.approvalId, d, d === "deny" ? note || undefined : undefined, asks && d !== "deny" ? secret : undefined);
      setLocal(d);
      setSecret("");
    } catch (e) {
      if (asks && e instanceof ApiError && e.code === "bad_secret") setSecretErr(e.message);
      else toast.error("Decision failed", errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  const destructive = approval.risk === "destructive";
  return (
    <div className={cx("rounded-md border p-3", destructive ? "border-err/40 bg-err/5" : "border-warn/40 bg-warn/5")}>
      <div className="flex items-start gap-2">
        {asks ? <KeyRound className="size-4 shrink-0 mt-0.5 text-warn" /> : <ShieldAlert className={cx("size-4 shrink-0 mt-0.5", destructive ? "text-err" : "text-warn")} />}
        <div className="flex-1 min-w-0">
          <div className="text-[13px] font-medium">
            {decision ? "Approval" : asks ? "The agent needs a password for this" : "The agent wants to run this"}
            {approval.target && (
              <>
                {" "}
                on <span className="font-mono">{approval.target}</span>
              </>
            )}
            {approval.environment && (
              <>
                {" "}
                in <Badge tone={/prod/i.test(approval.environment) ? "err" : "neutral"}>{approval.environment}</Badge>
              </>
            )}
          </div>
          {approval.reason && <div className="text-xs text-muted mt-0.5">{approval.reason}</div>}
          {approval.grantLabel && !destructive && <div className="text-[11px] text-faint mt-0.5">“Always allow” = {approval.grantLabel.replace(/^Always allow /, "")}</div>}
        </div>
        <Badge tone={riskTone(approval.risk)}>{approval.risk}</Badge>
      </div>
      {approval.preview && (
        <div className="mt-2.5">
          <DiffPreview text={approval.preview} />
        </div>
      )}
      {decision ? (
        <div className="mt-2.5 text-xs flex items-center gap-1.5">
          {decision === "deny" ? (
            <>
              <X className="size-3.5 text-err" /> <span className="text-err">Denied</span>
            </>
          ) : (
            <>
              <Check className="size-3.5 text-ok" />{" "}
              <span className="text-ok">
                {decision === "always" ? (asks ? "Approved for this session, password kept until opsdeck locks" : "Approved (always in this session)") : "Approved"}
              </span>
            </>
          )}
          {resolved?.note && <span className="text-muted">— {resolved.note}</span>}
        </div>
      ) : (
        <div className="mt-3 space-y-2">
          {asks && (
            <div>
              <label className="text-xs text-muted">{asks.label}</label>
              <Input
                type="password"
                autoFocus
                autoComplete="off"
                className="mt-1"
                value={secret}
                onChange={(e) => {
                  setSecret(e.target.value);
                  setSecretErr("");
                }}
                placeholder="Password"
                onKeyDown={(e) => e.key === "Enter" && void decide("approve")}
              />
              <div className={cx("text-[11px] mt-1", secretErr ? "text-err" : "text-faint")}>
                {secretErr || "opsdeck uses it for this action only; the agent never sees it."}
              </div>
            </div>
          )}
          {denyOpen && (
            <Input dir="auto"
              autoFocus={!asks}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="Optional: tell the agent why / what to do instead"
              onKeyDown={(e) => e.key === "Enter" && void decide("deny")}
            />
          )}
          <div className="flex items-center gap-2">
            <Button variant="primary" size="sm" icon={<Check className="size-3.5" />} loading={busy === "approve"} disabled={!!busy} onClick={() => void decide("approve")}>
              {asks ? "Unlock once" : "Approve"}
            </Button>
            {!destructive && (
              <Button
                size="sm"
                loading={busy === "always"}
                disabled={!!busy}
                onClick={() => void decide("always")}
                title={[approval.grantLabel || "Don't ask again for this tool and target in this session", asks?.rememberLabel].filter(Boolean).join(" ")}
              >
                {asks ? "Unlock for session" : "Always allow in session"}
              </Button>
            )}
            <div className="flex-1" />
            {denyOpen ? (
              <Button size="sm" variant="danger" loading={busy === "deny"} disabled={!!busy} onClick={() => void decide("deny")}>
                Deny
              </Button>
            ) : (
              <Button size="sm" variant="ghost" icon={<X className="size-3.5" />} onClick={() => setDenyOpen(true)}>
                Deny…
              </Button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
