import { useEffect, useMemo, useState } from "react";
import { BellRing, ChevronDown, ChevronUp, CircleCheck, CircleX, ListChecks, ListTodo, Loader2, Octagon, SquareTerminal, User } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { TaskInfo, TodoItem } from "../../api/types";
import { Badge, Button, CodeBlock, cx } from "../../components/ui";
import { Modal, Popover, useToast } from "../../components/overlay";
import { fmtMs } from "../../lib/format";
import { textDir } from "../../lib/rtl";
import { TodoList } from "./tools";
import type { Ev } from "./timeline";

// ---------------------------------------------------------------- checklist

/** the agent's checklist (todo tool), pinned above the message box */
export function TodoPanel({ items, running }: { items: TodoItem[]; running: boolean }) {
  const [open, setOpen] = useState(running);
  const finished = items.filter((x) => x.status === "done" || x.status === "skipped").length;
  const current = items.find((x) => x.status === "in_progress");
  const complete = finished === items.length;
  return (
    <div className="mb-2 rounded-lg border border-line bg-panel shadow-sm">
      <button onClick={() => setOpen((o) => !o)} className="w-full flex items-center gap-2 px-3 h-9 text-left">
        <ListTodo className={cx("size-4", complete ? "text-ok" : "text-accent")} />
        <span className="text-[12.5px] font-medium">Checklist</span>
        <span className="text-[11px] text-faint tabular-nums">
          {finished}/{items.length}
        </span>
        <span className="h-1.5 w-24 rounded-full bg-panel-3 overflow-hidden shrink-0">
          <span className={cx("block h-full rounded-full transition-all", complete ? "bg-ok" : "bg-accent")} style={{ width: `${items.length ? (finished / items.length) * 100 : 0}%` }} />
        </span>
        <span dir="auto" className="flex-1 min-w-0 truncate text-[12px] text-muted text-start">
          {!open && current ? current.text : ""}
        </span>
        {open ? <ChevronDown className="size-3.5 text-faint" /> : <ChevronUp className="size-3.5 text-faint" />}
      </button>
      {open && (
        <div className="border-t border-line px-3 py-2 max-h-48 overflow-y-auto">
          <TodoList items={items} compact />
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- background tasks

const STATE: Record<TaskInfo["state"], { tone: "info" | "ok" | "err" | "neutral"; icon: React.ReactNode }> = {
  running: { tone: "info", icon: <Loader2 className="size-3.5 animate-spin text-info" /> },
  done: { tone: "ok", icon: <CircleCheck className="size-3.5 text-ok" /> },
  failed: { tone: "err", icon: <CircleX className="size-3.5 text-err" /> },
  stopped: { tone: "neutral", icon: <Octagon className="size-3.5 text-faint" /> },
};

function since(t: TaskInfo): string {
  const end = t.ended ? new Date(t.ended).getTime() : Date.now();
  return fmtMs(end - new Date(t.started).getTime());
}

/** the session's background tasks, latest state from the event stream */
export function useTasks(events: { type: string; task?: TaskInfo }[]): TaskInfo[] {
  return useMemo(() => {
    const m = new Map<string, TaskInfo>();
    for (const e of events) if (e.type === "task" && e.task) m.set(e.task.id, e.task);
    return [...m.values()].sort((a, b) => a.started.localeCompare(b.started));
  }, [events]);
}

export function TasksButton({ ws, sid, tasks, open, setOpen }: { ws: string; sid: string; tasks: TaskInfo[]; open: boolean; setOpen: (o: boolean) => void }) {
  const [viewing, setViewing] = useState<string | null>(null);
  const [, tick] = useState(0);
  const running = tasks.filter((t) => t.state === "running").length;
  useEffect(() => {
    if (!running) return;
    const i = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(i);
  }, [running]);
  if (tasks.length === 0) return null;
  return (
    <div className="relative">
      <button
        onClick={() => setOpen(!open)}
        className={cx(
          "flex items-center gap-1.5 h-7 px-2 rounded-md border text-[11.5px]",
          running ? "border-info/40 bg-info/10 text-info" : "border-line bg-panel-2 text-muted hover:text-fg",
        )}
        title="Background tasks of this session"
      >
        {running ? <Loader2 className="size-3.5 animate-spin" /> : <ListChecks className="size-3.5" />}
        {running ? `${running} running` : `${tasks.length} task${tasks.length > 1 ? "s" : ""}`}
      </button>
      <Popover open={open} onClose={() => setOpen(false)} className="right-0 mt-1 w-[440px] p-1">
        <div className="px-2.5 py-1.5 text-[11px] text-faint">Background tasks keep running after the agent's turn; it is told when they finish.</div>
        {tasks.map((t) => (
          <button key={t.id} onClick={() => setViewing(t.id)} className="w-full flex items-start gap-2.5 rounded-md px-2.5 py-2 text-left hover:bg-panel-3">
            <span className="mt-0.5">{STATE[t.state].icon}</span>
            <span className="min-w-0 flex-1">
              <span className="flex items-center gap-1.5">
                <span className="font-mono text-[11.5px] text-faint">{t.id}</span>
                <Badge mono>{t.target}</Badge>
                <span className="flex-1" />
                <span className="text-[11px] text-faint tabular-nums">{since(t)}</span>
              </span>
              <span className="block font-mono text-[12px] truncate mt-0.5">{t.command}</span>
              <span className="block text-[11px] text-muted mt-0.5">
                {t.state === "running" ? `${t.lines} lines so far` : t.error ? t.error : `exit ${t.exitCode} · ${t.lines} lines`}
                {t.matched && <span className="text-info"> · matched “{t.matched}”</span>}
              </span>
            </span>
          </button>
        ))}
      </Popover>
      {viewing && <TaskModal ws={ws} sid={sid} id={viewing} onClose={() => setViewing(null)} />}
    </div>
  );
}

function TaskModal({ ws, sid, id, onClose }: { ws: string; sid: string; id: string; onClose: () => void }) {
  const toast = useToast();
  const [data, setData] = useState<{ task: TaskInfo; output: string } | null>(null);
  const [stopping, setStopping] = useState(false);
  useEffect(() => {
    let live = true;
    let timer: ReturnType<typeof setTimeout>;
    const load = async () => {
      try {
        const d = await api.task(ws, sid, id);
        if (!live) return;
        setData(d);
        if (d.task.state === "running") timer = setTimeout(load, 1500);
      } catch (e) {
        if (live) toast.error("Cannot load the task", errMsg(e));
      }
    };
    void load();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [ws, sid, id, toast]);
  const t = data?.task;
  return (
    <Modal
      open
      onClose={onClose}
      width={880}
      title={
        <span className="flex items-center gap-2">
          {t && STATE[t.state].icon} Background task <span className="font-mono text-muted">{id}</span>
        </span>
      }
      footer={
        <>
          {t?.state === "running" && (
            <Button
              variant="danger"
              loading={stopping}
              onClick={async () => {
                setStopping(true);
                try {
                  await api.stopTask(ws, sid, id);
                } catch (e) {
                  toast.error("Stop failed", errMsg(e));
                } finally {
                  setStopping(false);
                }
              }}
            >
              Stop task
            </Button>
          )}
          <div className="flex-1" />
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        </>
      }
    >
      {!t ? (
        <div className="flex justify-center py-8">
          <Loader2 className="size-5 animate-spin text-muted" />
        </div>
      ) : (
        <div className="space-y-3">
          <div className="flex items-center gap-2 flex-wrap text-[12px]">
            <Badge tone={STATE[t.state].tone}>{t.state}</Badge>
            <Badge mono>{t.target}</Badge>
            <span className="text-faint">
              {t.tool} · started {new Date(t.started).toLocaleTimeString()} · {since(t)}
              {t.state !== "running" && ` · exit ${t.exitCode}`}
            </span>
            {t.notifyOn && <span className="text-faint">· tells the agent on /{t.notifyOn}/</span>}
          </div>
          <div className="rounded-md bg-panel-2 border border-line px-3 py-2 font-mono text-[12px] whitespace-pre-wrap break-all">
            <span className="text-accent select-none">$ </span>
            {t.command}
          </div>
          {t.error && <div className="text-[12px] text-err">{t.error}</div>}
          <CodeBlock maxHeight={460}>{data.output || "(no output yet)"}</CodeBlock>
        </div>
      )}
    </Modal>
  );
}

// ---------------------------------------------------------------- notifications and ! shell

export function NotificationMsg({ text }: { text: string }) {
  const [head, ...rest] = text.split("\nLast output:\n");
  const tail = rest.join("\nLast output:\n");
  const ok = /exit code 0\b/.test(head) || /matching/.test(head);
  return (
    <div className="flex gap-3">
      <span className={cx("size-6 rounded-md grid place-items-center shrink-0 mt-0.5", ok ? "bg-info/15" : "bg-warn/15")}>
        <BellRing className={cx("size-3.5", ok ? "text-info" : "text-warn")} />
      </span>
      <div className="flex-1 min-w-0 rounded-lg border border-dashed border-line-strong px-3.5 py-2.5">
        <div className="text-[10.5px] uppercase tracking-wide text-faint mb-0.5">opsdeck · background task</div>
        <div className="text-[13px] whitespace-pre-wrap break-words">{head}</div>
        {tail && (
          <div className="mt-2">
            <CodeBlock maxHeight={200}>{tail}</CodeBlock>
          </div>
        )}
      </div>
    </div>
  );
}

export function ShellMsg({ ev }: { ev: Ev<"shell"> }) {
  // the user ran it to see the output
  const [open, setOpen] = useState(true);
  const out = ev.output.replace(/^exit code: -?\d+ \([\d.]+s\)\n/, "");
  return (
    <div className="flex gap-3">
      <span className="size-6 rounded-md bg-panel-3 grid place-items-center shrink-0 mt-0.5">
        <User className="size-3.5 text-muted" />
      </span>
      <div className="flex-1 min-w-0 rounded-lg border border-line bg-panel overflow-hidden">
        <button onClick={() => setOpen((o) => !o)} className="w-full flex items-center gap-2 px-3 h-9 text-left hover:bg-panel-2">
          <SquareTerminal className="size-3.5 text-muted shrink-0" />
          <span className="font-mono text-[12px] truncate flex-1 min-w-0">
            <span className="text-accent select-none">! </span>
            {ev.command}
          </span>
          <Badge mono>{ev.label}</Badge>
          <Badge tone={ev.exitCode === 0 ? "ok" : "err"}>exit {ev.exitCode}</Badge>
          <span className="text-[10.5px] text-faint tabular-nums">{fmtMs(ev.durationMs)}</span>
        </button>
        {open && (
          <div className="border-t border-line px-3 py-2.5">
            <CodeBlock maxHeight={360} tone={ev.exitCode !== 0 ? "err" : undefined}>
              {out || "(no output)"}
            </CodeBlock>
            <div className="text-[11px] text-faint mt-1.5">You ran this; the output goes to the agent with your next message.</div>
          </div>
        )}
      </div>
    </div>
  );
}

export function UserCommand({ text, command }: { text: string; command: string }) {
  return (
    <span dir={textDir(text)}>
      <span className="font-mono text-accent">/{command}</span>
      {text.replace(new RegExp(`^/${command}`), "")}
    </span>
  );
}
