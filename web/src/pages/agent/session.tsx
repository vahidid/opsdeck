import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { AlertTriangle, BookOpen, CircleSlash, FileClock, GitFork, Loader2, Minimize2, MoreHorizontal, Pencil, Plug, ShieldAlert, Sparkles } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { AgentEvent, ContextUse, DeliverMode, ModelInfo, PendingMessage, SessionInfo, SessionMode, SessionState, Usage } from "../../api/types";
import { useWs } from "../../context/app";
import { useTabs } from "../../context/tabs";
import { useTerminals } from "../../context/terminals";
import { Badge, Button, EmptyState, IconButton, Input, Segmented } from "../../components/ui";
import { MenuItem, Popover, useToast } from "../../components/overlay";
import { ActionCtx } from "../../components/rich";
import { useAsync, useLocalStorage } from "../../hooks";
import { fmtTokens } from "../../lib/format";
import { Composer, parseShell } from "./composer";
import { NotificationMsg, PendingList, ShellMsg, TasksButton, TodoPanel, useTasks } from "./panels";
import { buildTimeline, callKey, groupBlocks, waiting } from "./timeline";
import { ToolGroup } from "./tools";
import { ContextMeter, LimitsButton } from "./usage";
import { Recall, ThinkingBlock, AssistantMsg, Compaction, Thinking, UserMsg, uploadAll } from "./messages";
import { ModePicker, ModelPicker, useCommands } from "./composer";
import { StateDot } from "./panels";

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

export function SessionView({ ws, sid, models, onChanged }: { ws: string; sid: string; models: ModelInfo[]; onChanged: () => void }) {
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
  // bumps on every usage event: the limits popover refreshes while open
  const [usageTick, setUsageTick] = useState(0);
  // messages sent during a turn that wait (queued or steering), and text a
  // stopped turn gave back to the composer
  const [pending, setPending] = useState<PendingMessage[]>([]);
  const [refill, setRefill] = useState<{ text: string; n: number }>({ text: "", n: 0 });
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
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [editTitle, setEditTitle] = useState("");

  async function saveTitle(e?: React.FormEvent) {
    e?.preventDefault();
    const t = editTitle.trim();
    if (!t || t === session?.title) {
      setIsEditingTitle(false);
      return;
    }
    try {
      await api.updateSession(ws, sid, { title: t });
      setSession((prev) => (prev ? { ...prev, title: t } : prev));
      setIsEditingTitle(false);
      onChangedRef.current?.();
    } catch (err) {
      toast.error("Rename failed", errMsg(err));
    }
  }

  useEffect(() => {
    if (params.get("draft")) {
      params.delete("draft");
      setParams(params, { replace: true });
    }
  }, [params, setParams]);

  // The newest state, tokens and context the stream sent, with the id
  // number of their event. A reload whose snapshot is older (it was taken
  // before, or arrives after them) must not undo them; events that arrive
  // before the first load are kept here too.
  const live = useRef<{ seq: number; state?: SessionState; usage?: Usage; context?: ContextUse }>({ seq: 0 });
  const load = useCallback(async () => {
    try {
      const r = await api.session(ws, sid);
      const l = live.current;
      const stale = r.seq !== undefined && r.seq < l.seq;
      setSession(
        stale
          ? { ...r.session, state: l.state ?? r.session.state, usage: l.usage ?? r.session.usage, context: l.context ?? r.session.context }
          : r.session,
      );
      setPending(r.pending);
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
      if (ev.type === "pending") {
        setPending(ev.messages ?? []);
        if (ev.returned?.length) {
          const text = ev.returned.map((p) => p.text).join("\n\n");
          setRefill((r) => ({ text, n: r.n + 1 }));
        }
        return;
      }
      if (ev.type === "usage" || ev.type === "status") {
        const n = Number(ev.id?.slice(1)) || 0;
        const l = live.current;
        if (n >= l.seq) {
          live.current = {
            seq: n,
            state: ev.type === "status" ? ev.state : l.state,
            usage: ev.usage ?? l.usage,
            context: ev.context ?? l.context,
          };
        }
      }
      if (ev.type === "usage") {
        setSession((s) => (s ? { ...s, usage: ev.usage, context: ev.context ?? s.context } : s));
        setUsageTick((n) => n + 1);
        return;
      }
      if (ev.type === "status") {
        setSession((s) => (s ? { ...s, state: ev.state, usage: ev.usage ?? s.usage, context: ev.context ?? s.context } : s));
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

  // an action button of an answer sends its prompt as the next message
  const sendRef = useRef<(text: string) => void>(() => {});
  sendRef.current = (text) => void send(text);
  const sendAction = useCallback((text: string) => sendRef.current(text), []);

  async function send(text: string, files: File[] = [], mode?: DeliverMode) {
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
      const ids = await uploadAll(ws, sid, files);
      const r = await api.sendMessage(ws, sid, text, ids, mode);
      if (r?.pending) {
        const p = r.pending;
        setPending((list) => (list.some((x) => x.id === p.id) ? list : [...list, p]));
      } else setSession((s) => (s ? { ...s, state: "running" } : s));
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
          {isEditingTitle ? (
            <form onSubmit={saveTitle} className="flex items-center gap-1.5 max-w-md flex-1">
              <Input
                autoFocus
                value={editTitle}
                onChange={(e) => setEditTitle(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") setIsEditingTitle(false);
                }}
                className="h-7 text-xs font-medium"
              />
              <Button size="xs" variant="primary" type="submit">
                Save
              </Button>
              <Button size="xs" variant="ghost" onClick={() => setIsEditingTitle(false)}>
                Cancel
              </Button>
            </form>
          ) : (
            <div className="group/title flex items-center gap-1.5 min-w-0">
              <div
                dir="auto"
                onClick={() => {
                  setEditTitle(session?.title || "");
                  setIsEditingTitle(true);
                }}
                className="truncate font-medium cursor-pointer hover:text-fg/80"
                title="Click to rename"
              >
                {session?.title || "Untitled session"}
              </div>
              <button
                onClick={() => {
                  setEditTitle(session?.title || "");
                  setIsEditingTitle(true);
                }}
                className="opacity-0 group-hover/title:opacity-100 p-0.5 text-muted hover:text-fg rounded transition-opacity"
                title="Rename session"
              >
                <Pencil className="size-3" />
              </button>
            </div>
          )}
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
        {session && <ContextMeter context={session.context} model={session.model} className="hidden md:inline-flex" />}
        {session && (
          <span className="text-[11px] text-faint tabular-nums" title="Tokens of this session: in / out" data-testid="session-tokens">
            {fmtTokens(session.usage?.inputTokens)} in · {fmtTokens(session.usage?.outputTokens)} out
          </span>
        )}
        {session && <LimitsButton provider={session.model.split("/")[0]} tick={usageTick} />}
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
            <MenuItem
              icon={<Pencil />}
              onClick={() => {
                setMenu(false);
                setEditTitle(session?.title || "");
                setIsEditingTitle(true);
              }}
            >
              Rename session
            </MenuItem>
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
                return (
                  <UserMsg
                    key={t.id}
                    text={t.text}
                    command={t.command}
                    attachments={t.attachments}
                    fileUrl={(aid, dl) => api.attachmentUrl(ws, sid, aid, dl)}
                    onFork={t.forkable && !running ? () => void fork(t.id) : undefined}
                    delivered={t.steered ? "steered" : t.queued ? "queued" : undefined}
                  />
                );
              case "notification":
                return <NotificationMsg key={t.id} text={t.text} />;
              case "shell":
                return <ShellMsg key={t.id} ev={t.ev} />;
              case "recall":
                return <Recall key={t.id} items={t.items} preloaded={t.preloaded} />;
              case "thinking":
                return <ThinkingBlock key={t.id} text={t.text} />;
              case "assistant":
                return (
                  <ActionCtx.Provider key={t.id} value={sendAction}>
                    <AssistantMsg text={t.text} streaming={t.streaming} />
                  </ActionCtx.Provider>
                );
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
            <Thinking key={blocks[blocks.length - 1]?.id} after={blocks[blocks.length - 1]?.kind} step={todo?.find((t) => t.status === "in_progress")?.text} onStop={() => void stop()} />
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
          {pending.length > 0 && (
            <PendingList
              items={pending}
              onCancel={async (id) => {
                try {
                  await api.cancelPending(ws, sid, id);
                  setPending((list) => list.filter((p) => p.id !== id));
                } catch (e) {
                  toast.error("Could not cancel", errMsg(e));
                }
              }}
            />
          )}
          {session?.client ? (
            <ClientNotice session={session} running={running} onStop={stop} onMode={(m) => void updateSession({ mode: m })} />
          ) : (
          <Composer
            onSend={send}
            running={running}
            deliver
            refill={refill}
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
          )}
        </div>
      </div>
    </div>
  );
}

// ClientNotice replaces the composer in the session of an agent connected
// through MCP: it does the thinking; here you watch and approve its calls.
function ClientNotice({ session, running, onStop, onMode }: { session: SessionInfo; running: boolean; onStop: () => void; onMode: (m: SessionMode) => void }) {
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-xl border border-agent/30 bg-agent/[0.07] px-4 py-3">
      <span className="grid size-8 place-items-center rounded-lg bg-agent text-agent-fg shrink-0">
        <Plug className="size-4" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium">{session.client} works here through MCP</div>
        <div className="text-[11.5px] text-muted">
          Its tool calls show up above and wait here for your approval. To talk to it, use {session.client}. Manage its key in{" "}
          <Link to="/settings" className="text-accent hover:underline">
            App settings › Agent access
          </Link>
          .
        </div>
      </div>
      <ModePicker value={session.mode} onChange={onMode} />
      {running && (
        <Button size="sm" variant="secondary" onClick={onStop}>
          Stop its calls
        </Button>
      )}
    </div>
  );
}
