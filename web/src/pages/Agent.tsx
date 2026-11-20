import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Bot, Cpu, GitFork, Globe, MessageSquarePlus, MoreHorizontal, Pencil, Plug, ScrollText, Search, SquareArrowOutUpRight, Terminal, Trash2 } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { HarnessInfo, ModelInfo, SessionInfo, SessionMode } from "../api/types";
import { useWs } from "../context/app";
import { useOpenInTab, useTabTitle, useTabs } from "../context/tabs";
import { Button, IconButton, Input, cx } from "../components/ui";
import { MenuItem, Popover, useConfirm, useToast } from "../components/overlay";
import { useAsync } from "../hooks";
import { timeAgo } from "../lib/format";
import { Composer } from "./agent/composer";
import { ModePicker, ModelPicker, parseModeCommand, useCommands, EXAMPLES, NEW_COMMANDS } from "./agent/composer";

import { NoModels, RenameModal, StateDot } from "./agent/panels";
import { SessionView } from "./agent/session";
import { uploadAll } from "./agent/messages";
type DateGroup = {
  title: string;
  sessions: SessionInfo[];
};

function groupSessionsByDate(sessions: SessionInfo[]): DateGroup[] {
  const now = new Date();
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const startOfYesterday = startOfToday - 86400000;
  const startOfLast7Days = startOfToday - 6 * 86400000;

  const today: SessionInfo[] = [];
  const yesterday: SessionInfo[] = [];
  const last7Days: SessionInfo[] = [];
  const older: SessionInfo[] = [];

  for (const s of sessions) {
    const updated = s.updated ? new Date(s.updated).getTime() : 0;
    if (updated >= startOfToday) {
      today.push(s);
    } else if (updated >= startOfYesterday) {
      yesterday.push(s);
    } else if (updated >= startOfLast7Days) {
      last7Days.push(s);
    } else {
      older.push(s);
    }
  }

  const groups: DateGroup[] = [];
  if (today.length > 0) groups.push({ title: "Today", sessions: today });
  if (yesterday.length > 0) groups.push({ title: "Yesterday", sessions: yesterday });
  if (last7Days.length > 0) groups.push({ title: "Previous 7 days", sessions: last7Days });
  if (older.length > 0) groups.push({ title: "Older", sessions: older });
  return groups;
}

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

  const [filter, setFilter] = useState("");
  const sorted = useMemo(() => [...(sessions.data ?? [])].sort((a, b) => (b.updated || "").localeCompare(a.updated || "")), [sessions.data]);
  const filtered = useMemo(() => {
    if (!filter.trim()) return sorted;
    const q = filter.trim().toLowerCase();
    return sorted.filter((s) => (s.title || "Untitled session").toLowerCase().includes(q) || (s.mode || "").toLowerCase().includes(q));
  }, [sorted, filter]);
  const groups = useMemo(() => groupSessionsByDate(filtered), [filtered]);

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
        <div className="p-2 border-b border-line space-y-1.5">
          <Button className="w-full" icon={<MessageSquarePlus className="size-4" />} {...openInTab("agent")}>
            New session
          </Button>
          <div className="relative">
            <Input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Search sessions..."
              className="text-xs h-7.5 pl-7 pr-6"
            />
            <Search className="size-3.5 text-muted absolute left-2 top-1/2 -translate-y-1/2 pointer-events-none" />
            {filter && (
              <button
                onClick={() => setFilter("")}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-muted hover:text-fg text-xs"
                title="Clear search"
              >
                ×
              </button>
            )}
          </div>
        </div>
        <div className="flex-1 overflow-y-auto p-1.5 space-y-2">
          {filtered.length === 0 && !sessions.loading && (
            <div className="text-xs text-faint p-3 text-center">{filter ? "No matching sessions." : "No sessions yet."}</div>
          )}
          {groups.map((g) => (
            <div key={g.title} className="space-y-px">
              <div className="px-2 pt-1 pb-1 text-[10px] font-semibold uppercase tracking-wider text-faint">
                {g.title}
              </div>
              {g.sessions.map((s) => (
                <div
                  key={s.id}
                  {...openInTab(`agent/${encodeURIComponent(s.id)}`)}
                  title="Ctrl/⌘-click or middle-click: open in a new tab"
                  className={cx("group relative flex items-center gap-2 rounded-md px-2 py-1.5 cursor-pointer", s.id === sid ? "bg-panel-3" : "hover:bg-panel-2")}
                >
                  {s.client ? <Plug className="size-3 shrink-0 text-agent-ink" aria-label="connected agent" /> : <StateDot state={s.state} />}
                  <div className="min-w-0 flex-1">
                    <div dir="auto" className={cx("truncate text-[12.5px] text-start", s.id === sid ? "text-fg font-medium" : "text-muted group-hover:text-fg")}>
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
          ))}
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col">
        {models.data && models.data.length === 0 && !sid?.startsWith("mcp-") ? (
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

  async function start(text: string, files: File[]) {
    const m = parseModeCommand(text);
    if (m) {
      setMode(m);
      return true;
    }
    try {
      const s = await api.createSession(ws, { model: model || undefined, mode });
      try {
        const ids = await uploadAll(ws, s.id, files);
        await api.sendMessage(ws, s.id, text, ids);
      } catch (e) {
        // the empty session is of no use
        void api.deleteSession(ws, s.id).catch(() => {});
        throw e;
      }
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