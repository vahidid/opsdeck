import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation } from "react-router-dom";
import { BellRing, Check, ChevronDown, ChevronUp, ExternalLink, KeyRound, MessageCircleQuestion, ShieldAlert, X } from "lucide-react";
import { ApiError, api, errMsg } from "../api/client";
import type { Waiting } from "../api/types";
import { useTabs } from "../context/tabs";
import { DiffPreview } from "../pages/agent/tools";
import { useToast } from "./overlay";
import { Badge, Button, Input, cx, riskTone } from "./ui";

// WaitingCenter shows what agents wait for — approvals and questions, from
// any session of any workspace, the built-in agent's or an outside agent's
// (Claude Code over MCP) — on every page but the session itself, with
// Approve and Deny right there. It also tells the server whether this
// window has the focus, so the desktop is notified only when nobody looks.
export function WaitingCenter() {
  const items = useWaiting();
  usePresence();
  const location = useLocation();
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const [collapsed, setCollapsed] = useState(false);

  // what the open session already shows is left to its own card
  const here = (w: Waiting) => decodeURIComponent(location.pathname).startsWith(`/w/${w.workspace}/agent/${w.session}`);
  const shown = items.filter((w) => !here(w));
  const visible = shown.filter((w) => !hidden.has(key(w)));
  const later = shown.length - visible.length;

  // the window title counts them, also for a tab in the background
  useEffect(() => {
    const base = document.title.replace(/^\(\d+\) /, "");
    document.title = items.length ? `(${items.length}) ${base}` : base;
  }, [items.length]);

  // forget hidden items once they are gone
  useEffect(() => {
    setHidden((h) => {
      const keys = new Set(items.map(key));
      const next = new Set([...h].filter((k) => keys.has(k)));
      return next.size === h.size ? h : next;
    });
  }, [items]);

  if (!shown.length) return null;
  return (
    <div className="fixed top-14 right-4 z-[65] flex w-[400px] max-w-[calc(100vw-2rem)] flex-col gap-2" role="region" aria-label="Waiting for you">
      {(later > 0 || collapsed) && (
        <button
          type="button"
          onClick={() => {
            setHidden(new Set());
            setCollapsed(false);
          }}
          className="self-end flex items-center gap-1.5 rounded-full border border-warn/40 bg-panel px-3 py-1 text-[12px] font-medium text-warn shadow-lg shadow-black/30 hover:bg-panel-2"
        >
          <BellRing className="size-3.5" /> {collapsed ? shown.length : later} waiting for you
        </button>
      )}
      {!collapsed && visible.length > 1 && (
        <button type="button" className="self-end text-[11px] text-faint hover:text-fg" onClick={() => setCollapsed(true)}>
          Hide all
        </button>
      )}
      {!collapsed &&
        visible.slice(0, 3).map((w) => (
          <WaitingCard key={key(w)} item={w} onHide={() => setHidden((h) => new Set(h).add(key(w)))} />
        ))}
      {!collapsed && visible.length > 3 && <div className="self-end text-[11px] text-faint">and {visible.length - 3} more</div>}
    </div>
  );
}

const key = (w: Waiting) => `${w.workspace}/${w.session}/${w.id}`;

// useWaiting follows /api/waiting/stream (the whole list on every change).
function useWaiting(): Waiting[] {
  const [items, setItems] = useState<Waiting[]>([]);
  useEffect(() => {
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let closed = false;
    const connect = () => {
      es = new EventSource("/api/waiting/stream");
      es.onmessage = (m) => {
        try {
          setItems(((JSON.parse(m.data) as { items?: Waiting[] }).items ?? []) as Waiting[]);
        } catch {
          /* ignore */
        }
      };
      es.onerror = () => {
        es?.close();
        if (!closed) retry = setTimeout(connect, 5000);
      };
    };
    connect();
    return () => {
      closed = true;
      clearTimeout(retry);
      es?.close();
    };
  }, []);
  return items;
}

// usePresence reports whether this window has the focus: on every change,
// and every 30 s while it has.
function usePresence() {
  useEffect(() => {
    let last: boolean | null = null;
    const report = (force = false) => {
      const focused = document.visibilityState === "visible" && document.hasFocus();
      if (!force && focused === last) return;
      last = focused;
      void fetch("/api/presence", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ focused }),
        keepalive: true,
      }).catch(() => {});
    };
    const onChange = () => report();
    report(true);
    window.addEventListener("focus", onChange);
    window.addEventListener("blur", onChange);
    document.addEventListener("visibilitychange", onChange);
    const beat = setInterval(() => last && report(true), 30_000);
    return () => {
      clearInterval(beat);
      window.removeEventListener("focus", onChange);
      window.removeEventListener("blur", onChange);
      document.removeEventListener("visibilitychange", onChange);
    };
  }, []);
}

function WaitingCard({ item, onHide }: { item: Waiting; onHide: () => void }) {
  const tabs = useTabs();
  const open = () => tabs.show(item.workspace, `agent/${encodeURIComponent(item.session)}`);
  const who = item.client || "Agent";
  return (
    <div
      className={cx(
        "rounded-xl border bg-panel shadow-2xl shadow-black/40 overflow-hidden",
        item.kind === "question" ? "border-agent/40" : item.risk === "destructive" ? "border-err/50" : "border-warn/50",
      )}
    >
      <div className="flex items-center gap-2 px-3 py-1.5 border-b border-line bg-panel-2 text-[11.5px] text-muted">
        {item.kind === "question" ? <MessageCircleQuestion className="size-3.5 text-agent-ink" /> : <BellRing className="size-3.5 text-warn" />}
        <span className="truncate flex-1">
          <b className="text-fg font-medium">{who}</b> · {item.workspace}
          {!item.client && item.sessionTitle ? ` · ${item.sessionTitle}` : ""}
        </span>
        <button type="button" title="Open the session" className="text-faint hover:text-fg" onClick={open}>
          <ExternalLink className="size-3.5" />
        </button>
        <button type="button" title="Later" className="text-faint hover:text-fg" onClick={onHide}>
          <X className="size-3.5" />
        </button>
      </div>
      <div className="p-3">{item.kind === "question" ? <QuestionBody item={item} onOpen={open} /> : <ApprovalBody item={item} onOpen={open} />}</div>
    </div>
  );
}

function ApprovalBody({ item, onOpen }: { item: Waiting; onOpen: () => void }) {
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const [denyOpen, setDenyOpen] = useState(false);
  const [note, setNote] = useState("");
  const [secret, setSecret] = useState("");
  const [secretErr, setSecretErr] = useState("");
  const [showPreview, setShowPreview] = useState(false);
  const step = item.stage === "next";
  const destructive = item.risk === "destructive";
  const hosts = item.hosts ?? [];
  const input = useMemo(() => summarize(item.input), [item.input]);

  async function decide(d: "approve" | "deny" | "always") {
    if (item.secret && d !== "deny" && !secret) {
      setSecretErr("Enter the password, or deny.");
      return;
    }
    setBusy(d);
    try {
      await api.approve(item.workspace, item.session, item.id, d, d === "deny" ? note || undefined : undefined, item.secret && d !== "deny" ? secret : undefined);
    } catch (e) {
      if (item.secret && e instanceof ApiError && e.code === "bad_secret") setSecretErr(e.message);
      else toast.error("Decision failed", errMsg(e));
      setBusy(null);
    }
  }

  return (
    <div className="space-y-2">
      <div className="flex items-start gap-2">
        {item.secret ? <KeyRound className="size-4 shrink-0 mt-0.5 text-warn" /> : <ShieldAlert className={cx("size-4 shrink-0 mt-0.5", destructive ? "text-err" : "text-warn")} />}
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium">
            {step ? (
              item.title || "Continue?"
            ) : (
              <>
                <span className="font-mono">{item.tool}</span>
                {item.target && (
                  <>
                    {" "}
                    on <span className="font-mono">{item.target}</span>
                  </>
                )}
                {item.environment && item.environment !== "internet" && (
                  <>
                    {" "}
                    <Badge tone={/prod/i.test(item.environment) ? "err" : "neutral"}>{item.environment}</Badge>
                  </>
                )}
              </>
            )}
          </div>
          {item.reason && <div className="text-xs text-muted mt-0.5 line-clamp-3">{item.reason}</div>}
          {hosts.length > 1 && <div className="text-[11px] text-faint mt-0.5">{hosts.length} hosts: {hosts.slice(0, 4).join(", ")}{hosts.length > 4 ? "…" : ""}</div>}
        </div>
        {item.risk && <Badge tone={riskTone(item.risk)}>{item.risk}</Badge>}
      </div>
      {input && !item.preview && <pre className="rounded-md border border-line bg-panel-2 px-2 py-1.5 font-mono text-[11px] max-h-24 overflow-auto whitespace-pre-wrap break-all">{input}</pre>}
      {item.preview && (
        <div>
          <button type="button" className="flex items-center gap-1 text-[11.5px] text-muted hover:text-fg" onClick={() => setShowPreview((v) => !v)}>
            {showPreview ? <ChevronUp className="size-3.5" /> : <ChevronDown className="size-3.5" />} What changes
          </button>
          {showPreview && (
            <div className="mt-1.5 max-h-56 overflow-auto">
              <DiffPreview text={item.preview} />
            </div>
          )}
        </div>
      )}
      {item.secret && (
        <div>
          <Input
            type="password"
            autoComplete="off"
            value={secret}
            onChange={(e) => {
              setSecret(e.target.value);
              setSecretErr("");
            }}
            placeholder="Password"
            onKeyDown={(e) => e.key === "Enter" && void decide("approve")}
          />
          <div className={cx("text-[11px] mt-1", secretErr ? "text-err" : "text-faint")}>{secretErr || "Used for this action only; the agent never sees it."}</div>
        </div>
      )}
      {denyOpen && (
        <Input
          dir="auto"
          autoFocus={!item.secret}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="Optional: tell the agent why"
          onKeyDown={(e) => e.key === "Enter" && void decide("deny")}
        />
      )}
      <div className="flex items-center gap-1.5 flex-wrap pt-0.5">
        <Button variant="primary" size="sm" icon={<Check className="size-3.5" />} loading={busy === "approve"} disabled={!!busy} onClick={() => void decide("approve")}>
          {step ? "Continue" : hosts.length > 1 ? `Approve all ${hosts.length}` : "Approve"}
        </Button>
        {!destructive && !step && (
          <Button size="sm" variant="ghost" loading={busy === "always"} disabled={!!busy} onClick={() => void decide("always")} title={item.grantLabel || "Don't ask again for this tool and target in this session"}>
            Always in session
          </Button>
        )}
        <div className="flex-1" />
        {hosts.length > 1 && !step && (
          <Button size="sm" variant="ghost" onClick={onOpen} title="Approve as a canary in the session">
            Canary…
          </Button>
        )}
        {denyOpen ? (
          <Button size="sm" variant="danger" loading={busy === "deny"} disabled={!!busy} onClick={() => void decide("deny")}>
            {step ? "Stop" : "Deny"}
          </Button>
        ) : (
          <Button size="sm" variant="ghost" icon={<X className="size-3.5" />} onClick={() => setDenyOpen(true)}>
            {step ? "Stop…" : "Deny…"}
          </Button>
        )}
      </div>
    </div>
  );
}

function QuestionBody({ item, onOpen }: { item: Waiting; onOpen: () => void }) {
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const [text, setText] = useState("");
  const options = item.options ?? [];
  const ref = useRef<HTMLInputElement>(null);

  async function answer(a: { choices?: string[]; text?: string; skipped?: boolean }, id: string) {
    setBusy(id);
    try {
      await api.answer(item.workspace, item.session, item.id, a);
    } catch (e) {
      toast.error("Cannot answer", errMsg(e));
      setBusy(null);
    }
  }

  return (
    <div className="space-y-2">
      <div className="text-[13px] font-medium" dir="auto">
        {item.question}
      </div>
      {item.multiple ? (
        <Button size="sm" onClick={onOpen}>
          Choose in the session…
        </Button>
      ) : (
        <div className="flex flex-col gap-1">
          {options.map((o) => (
            <button
              key={o.label}
              type="button"
              disabled={!!busy}
              onClick={() => void answer({ choices: [o.label] }, o.label)}
              className="rounded-md border border-line px-2.5 py-1.5 text-left text-[12.5px] hover:border-agent hover:bg-agent/8 disabled:opacity-50"
            >
              <span className="font-medium" dir="auto">
                {o.label}
              </span>
              {o.description && (
                <span className="block text-[11px] text-faint" dir="auto">
                  {o.description}
                </span>
              )}
            </button>
          ))}
        </div>
      )}
      <div className="flex items-center gap-1.5">
        <Input ref={ref} dir="auto" value={text} onChange={(e) => setText(e.target.value)} placeholder="Or type an answer" onKeyDown={(e) => e.key === "Enter" && text.trim() && void answer({ text }, "text")} />
        <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => void answer({ skipped: true }, "skip")}>
          Skip
        </Button>
      </div>
    </div>
  );
}

// summarize shows a tool's input briefly: a command, a path, or the JSON.
function summarize(input: unknown): string {
  if (!input || typeof input !== "object") return "";
  const o = input as Record<string, unknown>;
  for (const k of ["command", "query", "statement", "path", "url", "action"]) {
    if (typeof o[k] === "string" && o[k]) return String(o[k]);
  }
  const s = JSON.stringify(o, null, 1);
  return s === "{}" ? "" : s.length > 600 ? s.slice(0, 600) + "…" : s;
}
