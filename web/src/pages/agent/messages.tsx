import { useEffect, useMemo, useRef, useState } from "react";
import { Bot, Brain, ChevronDown, ChevronRight, Download, FileText, GitFork, User, Sparkles, FileClock, ScrollText, BookOpen, Check } from "lucide-react";
import { api } from "../../api/client";
import type { Attachment, RecallItem } from "../../api/types";
import { Modal } from "../../components/overlay";
import { Markdown } from "../../components/Markdown";
import { exportHtml } from "../../lib/exportDoc";
import { textDir } from "../../lib/rtl";
import { fmtBytes } from "../../lib/format";
import { UserCommand } from "./panels";
import { useOpenInTab } from "../../context/tabs";

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
export function Recall({ items, preloaded }: { items: RecallItem[]; preloaded?: string }) {
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



// uploadAll stores the files of a message and returns their ids.
export async function uploadAll(ws: string, sid: string, files: File[]): Promise<string[]> {
  const ids: string[] = [];
  for (const f of files) ids.push((await api.attach(ws, sid, f)).id);
  return ids;
}

export function Attachments({ list, url }: { list: Attachment[]; url: (aid: string, download?: boolean) => string }) {
  const [zoom, setZoom] = useState<Attachment | null>(null);
  return (
    <div className="flex flex-wrap gap-1.5 mt-2 first:mt-0" dir="ltr">
      {list.map((a) =>
        a.kind === "image" ? (
          <button key={a.id} type="button" onClick={() => setZoom(a)} title={`${a.name} · ${fmtBytes(a.size)}`} className="rounded-md overflow-hidden border border-line hover:border-line-strong">
            <img src={url(a.id)} alt={a.name} className="h-20 max-w-[200px] object-cover" loading="lazy" />
          </button>
        ) : (
          <a
            key={a.id}
            href={url(a.id, true)}
            download={a.name}
            title={`${a.name} · ${fmtBytes(a.size)}`}
            className="flex items-center gap-2 rounded-md border border-line bg-panel px-2 py-1.5 text-[12px] hover:border-line-strong max-w-[240px]"
          >
            <FileText className="size-4 text-muted shrink-0" />
            <span className="min-w-0">
              <span className="block truncate font-medium">{a.name}</span>
              <span className="block text-[10.5px] text-faint">
                {a.kind === "pdf" ? "PDF" : a.kind === "text" ? "text" : a.mime} · {fmtBytes(a.size)}
              </span>
            </span>
          </a>
        ),
      )}
      {zoom && (
        <Modal open onClose={() => setZoom(null)} title={zoom.name} width={960}>
          <img src={url(zoom.id)} alt={zoom.name} className="max-w-full mx-auto" />
        </Modal>
      )}
    </div>
  );
}

export function UserMsg({
  text,
  command,
  attachments,
  fileUrl,
  onFork,
  delivered,
}: {
  text: string;
  command?: string;
  attachments?: Attachment[];
  fileUrl: (aid: string, download?: boolean) => string;
  onFork?: () => void;
  /** sent during a turn: steered into it, or queued for the next */
  delivered?: "steered" | "queued";
}) {
  return (
    <div className="group flex gap-3">
      <span className="size-6 rounded-md bg-panel-3 grid place-items-center shrink-0 mt-0.5">
        <User className="size-3.5 text-muted" />
      </span>
      <div
        dir={textDir(text)}
        className="relative flex-1 min-w-0 rounded-lg bg-panel-2 border border-line px-3.5 py-2.5 whitespace-pre-wrap break-words text-[13.5px] text-start leading-relaxed"
      >
        {delivered && (
          <span
            className="float-end ms-2 text-[10.5px] text-faint"
            data-testid="delivered"
            title={delivered === "steered" ? "Sent while the agent worked; it joined the running turn at its next step" : "Sent while the agent worked; it waited for the turn to end"}
          >
            {delivered === "steered" ? "steered into the turn" : "sent after the turn"}
          </span>
        )}
        {command ? <UserCommand text={text} command={command} /> : text}
        {attachments && attachments.length > 0 && <Attachments list={attachments} url={fileUrl} />}
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

export function AssistantMsg({ text, streaming }: { text: string; streaming: boolean }) {
  const body = useRef<HTMLDivElement>(null);
  return (
    <div className="group/msg flex gap-3">
      <span className="size-6 rounded-md bg-agent text-agent-fg grid place-items-center shrink-0 mt-0.5">
        <Bot className="size-3.5 text-agent-fg" />
      </span>
      <div className="flex-1 min-w-0 text-[13.5px] pt-0.5">
        <div ref={body}>
          {text ? (
            <Markdown dir={textDir(text) === "rtl" ? "rtl" : "auto"} streaming={streaming}>
              {text}
            </Markdown>
          ) : (
            <span className="text-faint">…</span>
          )}
        </div>
        {streaming && <span className="inline-block w-1.5 h-4 bg-agent-ink/80 ml-0.5 align-text-bottom pulse-dot" />}
        {!streaming && text.length > 400 && (
          <button
            onClick={() => body.current && void exportHtml(body.current, body.current.querySelector("h1,h2,h3")?.textContent || "opsdeck answer")}
            title="Save this answer as a self-contained HTML file (opens in any browser, prints to PDF)"
            className="mt-1 flex items-center gap-1 text-[11px] text-faint hover:text-fg opacity-0 group-hover/msg:opacity-100 focus-visible:opacity-100 transition-opacity"
          >
            <Download className="size-3" /> Export
          </button>
        )}
      </div>
    </div>
  );
}

export function ThinkingBlock({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const words = useMemo(() => text.trim().split(/\s+/).length, [text]);
  const preview = useMemo(() => {
    const first = text.trim().split("\n")[0] || "";
    return first.slice(0, 80) + (first.length > 80 ? "…" : "");
  }, [text]);

  return (
    <div className="ml-9 rounded-lg border border-agent/20 bg-agent/5 overflow-hidden transition-all">
      <button
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center gap-2 px-3 h-8 text-left hover:bg-agent/10 text-muted transition-colors"
      >
        {open ? <ChevronDown className="size-3.5 text-agent shrink-0" /> : <ChevronRight className="size-3.5 text-agent shrink-0" />}
        <Brain className="size-3.5 text-agent shrink-0" />
        <span className="text-[12px] font-medium text-agent">Thought process</span>
        <span className="text-[11px] text-faint tabular-nums shrink-0">({words} words)</span>
        {!open && preview && (
          <span dir="auto" className="flex-1 min-w-0 truncate font-mono text-[11px] text-faint text-start">
            {preview}
          </span>
        )}
      </button>
      {open && (
        <div className="border-t border-agent/15 px-3.5 py-2.5 text-[12.5px] text-muted leading-relaxed font-sans bg-panel/30">
          <div dir="auto" className="whitespace-pre-wrap break-words border-l-2 border-agent/40 pl-3 py-0.5 text-start">
            {text}
          </div>
        </div>
      )}
    </div>
  );
}

// The wait before the agent's next words: what it is doing (from what came
// last), how long it has been, the checklist step it is on, and a way to stop.
export function Thinking({ after, step, onStop }: { after?: string; step?: string; onStop: () => void }) {
  const [sec, setSec] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setSec((s) => s + 1), 1000);
    return () => clearInterval(t);
  }, []);
  const phase =
    sec >= 30
      ? "Still on it. Some steps take a minute"
      : after === "user"
        ? sec < 3
          ? "Reading your message"
          : "Working out where to start"
        : after === "recall"
          ? "Going through what this workspace already knows"
          : after === "assistant"
            ? "Deciding the next step"
            : "Thinking it through";
  return (
    <div className="flex items-center gap-3" role="status">
      <span className="size-6 rounded-md bg-agent/15 grid place-items-center shrink-0">
        <Brain className="size-3.5 text-agent animate-pulse" />
      </span>
      <div className="min-w-0 flex-1">
        <span className="think-text text-[13px]">{phase}</span>
        {step && (
          <div dir="auto" className="text-[11.5px] text-faint truncate text-start">
            {step}
          </div>
        )}
      </div>
      <span className="text-[11px] text-faint tabular-nums" aria-hidden>
        {sec >= 60 ? `${Math.floor(sec / 60)}m ${sec % 60}s` : `${sec}s`}
      </span>
      <button onClick={onStop} className="h-6 px-2 rounded-md border border-line text-[11px] text-muted hover:text-fg hover:border-line-strong">
        Stop
      </button>
    </div>
  );
}

export function Compaction({ summary }: { summary: string }) {
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
