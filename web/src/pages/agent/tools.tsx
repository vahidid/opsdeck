import { useState } from "react";
import { Link } from "react-router-dom";
import {
  Bot,
  Check,
  ChevronDown,
  ChevronRight,
  CircleCheck,
  Download,
  KeyRound,
  Loader2,
  MessageCircleQuestionMark,
  Send,
  ShieldAlert,
  SkipForward,
  SquareArrowOutUpRight,
  Webhook,
  X,
} from "lucide-react";
import { ApiError, api, errMsg } from "../../api/client";
import { Badge, Button, CodeBlock, Input, cx, riskTone } from "../../components/ui";
import { useToast } from "../../components/overlay";
import { Markdown } from "../../components/Markdown";
import { textDir } from "../../lib/rtl";
import { fmtBytes, fmtMs, fmtTokens } from "../../lib/format";
import type { ApprovalItem, Block, Ev, ToolItem } from "./timeline";
import { groupBlocks, waiting } from "./timeline";
import { ToolIcon, inputCommand, inputSummary, toolLabel } from "./toolmeta";
import { ToolViewBlock, viewIsEnough } from "./views";

type Ctx = { ws: string; sid: string; progress: Record<string, string> };

// ToolGroup is a run of tool calls between two messages: one line with
// what ran and how it went; open, one compact line per call. It stays open
// while it is running or waits for the user.
export function ToolGroup({ items, ctx, live, expandAll, flush }: { items: ToolItem[]; ctx: Ctx; live: boolean; expandAll: boolean; flush?: boolean }) {
  // Detailed: open the calls that have something to show (charts, results)
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const w = waiting(items);
  const pending = w.approvals + w.questions;
  const failed = items.filter((t) => t.result?.isError || t.approvals.some((a) => a.resolved?.decision === "deny")).length;
  const runningCount = items.filter((t) => !t.result && !t.approvals.some((a) => a.resolved?.decision === "deny")).length;
  if (items.length === 1) return <ToolCard item={items[0]} ctx={ctx} live={live} flush={flush} openViews={expandAll} />;
  const open = expandAll || pending > 0 || (userOpen ?? live);
  const counts = new Map<string, number>();
  for (const t of items) {
    const n = toolLabel(t.call?.tool ?? t.approvals[0]?.req.tool ?? "tool");
    counts.set(n, (counts.get(n) ?? 0) + 1);
  }
  const total = items.reduce((n, t) => n + (t.result?.durationMs ?? 0), 0);
  const parallel = items.length > 1 && items.every((t) => t.call?.risk === "read") && live && runningCount > 1;
  return (
    <div className={cx(!flush && "ml-9", "rounded-lg border bg-panel overflow-hidden", pending ? "border-warn/50" : failed ? "border-err/30" : "border-line")}>
      <button onClick={() => setUserOpen(!open)} className="w-full flex items-center gap-2 px-3 h-9 text-left hover:bg-panel-2">
        {open ? <ChevronDown className="size-3.5 text-faint" /> : <ChevronRight className="size-3.5 text-faint" />}
        <span className="text-[12.5px] font-medium">{items.length} tool calls</span>
        <span className="flex-1 min-w-0 truncate font-mono text-[11.5px] text-muted">
          {Array.from(counts)
            .map(([n, c]) => (c > 1 ? `${n} ×${c}` : n))
            .join(" · ")}
        </span>
        {parallel && <Badge tone="info">{runningCount} in parallel</Badge>}
        {pending > 0 && <Badge tone="warn">{pending} waiting for you</Badge>}
        {failed > 0 && <Badge tone="err">{failed} failed</Badge>}
        {total > 0 && <span className="text-[10.5px] text-faint tabular-nums">{fmtMs(total)}</span>}
        {runningCount > 0 && live ? <Loader2 className="size-3.5 animate-spin text-info" /> : failed === 0 && pending === 0 ? <Check className="size-3.5 text-ok" /> : null}
      </button>
      {open && (
        <div className="border-t border-line divide-y divide-line">
          {items.map((t) => (
            <ToolCard key={t.id} item={t} ctx={ctx} nested live={live} openViews={expandAll} />
          ))}
        </div>
      )}
    </div>
  );
}

/** a short summary of a tool's output for its collapsed line */
function outputSummary(item: ToolItem): string {
  const r = item.result;
  if (!r?.output) return "";
  const v = r.view;
  if (v?.kind === "search") return `${v.results.length} results`;
  if (v?.kind === "page") return v.title || v.final;
  if (v?.kind === "http") return `HTTP ${v.status}`;
  if (v?.kind === "timeseries") return `${v.total} series`;
  if (v?.kind === "vector") return `${v.total} series`;
  if (v?.kind === "logs") return `${v.lines.length} lines`;
  if (v?.kind === "alerts") return `${v.alerts.length} alerts`;
  if (v?.kind === "vulns") return `${v.vulns.length} vulnerabilities`;
  if (v?.kind === "dns") return `${v.records.length} records · ${v.zone}`;
  if (v?.kind === "deploy") return v.targets ? `${v.targets.length} targets · ${v.platform}` : v.detail ? `${v.detail.name} · ${v.detail.status}` : v.platform;
  if (item.call?.tool === "delegate" && item.sub?.end) return `${item.sub.end.toolCalls ?? 0} calls · ${item.sub.end.state}`;
  if (item.call?.tool === "todo" && item.todo) return `${item.todo.filter((x) => x.status === "done" || x.status === "skipped").length}/${item.todo.length} done`;
  const hostParts = (r.output.match(/^=== .+ ===$/gm) ?? []).length;
  if (hostParts > 1) {
    const bad = (r.output.match(/^exit code: [1-9]/gm) ?? []).length;
    return `${hostParts} hosts${bad ? ` · ${bad} failed` : ""}`;
  }
  const output = r.output;
  const lines = output.split("\n").filter((l) => l.trim() !== "");
  const exit = /^exit code: (-?\d+)/.exec(output);
  const first = lines.find((l) => !l.startsWith("exit code:") && !/^(stdout|stderr):$/.test(l.trim())) ?? "";
  const parts = [];
  if (exit) parts.push(`exit ${exit[1]}`);
  else if (r.isError) parts.push(first.replace(/^error:\s*/, "").slice(0, 80));
  else if (first) parts.push(first.slice(0, 60));
  if (lines.length > 1) parts.push(`${lines.length} lines`);
  return parts.join(" · ");
}

export function ToolCard({ item, ctx, nested, live, flush, openViews }: { item: ToolItem; ctx: Ctx; nested?: boolean; live?: boolean; flush?: boolean; openViews?: boolean }) {
  const { call, result } = item;
  const first = item.approvals[0]?.req;
  const tool = call?.tool ?? first?.tool ?? "tool";
  const input = call?.input ?? first?.input;
  const risk = call?.risk ?? first?.risk;
  const target = call?.target || first?.target;
  const env = call?.environment || first?.environment;
  const pendingApproval = item.approvals.some((a) => !a.resolved);
  const pendingQuestion = !!item.question && !item.answer && !result;
  const subWaiting = item.sub ? waiting(item.sub.items) : { approvals: 0, questions: 0 };
  const pending = pendingApproval || pendingQuestion || subWaiting.approvals + subWaiting.questions > 0;
  const denied = item.approvals.some((a) => a.resolved?.decision === "deny") && result?.isError;
  const done = !!result;
  const failed = result?.isError;
  const isSub = tool === "delegate";
  const [open, setOpen] = useState<boolean | null>(null);
  const expanded = open ?? (pending || !!failed || !!denied || (isSub && !done && !!live) || (!!openViews && (!!result?.view || isSub)));
  const cmd = inputCommand(input);
  const summary = inputSummary(tool, input);
  const progress = !done ? ctx.progress[item.id] : undefined;
  const blocked = item.hooks.find((h) => h.outcome === "deny");

  let statusEl: React.ReactNode;
  if (pendingQuestion) statusEl = <Badge tone="warn">needs your answer</Badge>;
  else if (pending) statusEl = <Badge tone="warn">needs approval</Badge>;
  else if (denied) statusEl = <Badge tone="err">{blocked ? "blocked" : "denied"}</Badge>;
  else if (!done) statusEl = <Loader2 className="size-3.5 animate-spin text-info" />;
  else if (failed) statusEl = <X className="size-3.5 text-err" />;
  else statusEl = <Check className="size-3.5 text-ok" />;

  const label = isSub ? `${item.sub?.start?.agent ?? (input as { agent?: string })?.agent ?? "subagent"}` : toolLabel(tool);
  return (
    <div
      className={cx(
        nested ? "bg-panel" : cx(!flush && "ml-9", "rounded-lg border bg-panel overflow-hidden"),
        !nested && (pending ? (risk === "destructive" ? "border-err/50 ring-1 ring-err/20" : "border-warn/50 ring-1 ring-warn/20") : failed || denied ? "border-err/30" : "border-line"),
        nested && pending && "ring-1 ring-inset ring-warn/40",
      )}
      {...(pendingApproval || pendingQuestion ? { "data-pending-approval": true } : {})}
    >
      <button onClick={() => setOpen(!expanded)} className={cx("w-full flex items-center gap-2 px-3 text-left hover:bg-panel-2", nested ? "h-8" : "h-9")}>
        {expanded ? <ChevronDown className="size-3.5 text-faint shrink-0" /> : <ChevronRight className="size-3.5 text-faint shrink-0" />}
        <ToolIcon tool={tool} className={cx("size-3.5 shrink-0", isSub ? "text-agent-ink" : "text-muted")} />
        <span className="font-mono text-[12px] font-medium shrink-0">{label}</span>
        {isSub && <Badge tone={item.sub?.start?.readOnly === false ? "warn" : "info"}>{item.sub?.start?.readOnly === false ? "can change" : "read-only"}</Badge>}
        {target && !isSub && (
          <Badge mono className="max-w-48 truncate">
            {target}
          </Badge>
        )}
        <span dir="auto" className="font-mono text-[11.5px] text-muted truncate flex-1 min-w-0 text-start">
          {isSub ? ((input as { task?: string })?.task ?? "") : summary}
        </span>
        {progress && !expanded && <span className="hidden md:inline text-[11px] text-info truncate max-w-64">{progress}</span>}
        {result && !expanded && (
          <span className={cx("hidden md:inline font-mono text-[11px] truncate max-w-56", result.isError ? "text-err" : "text-faint")}>{outputSummary(item)}</span>
        )}
        {item.hooks.length > 0 && <Webhook className={cx("size-3.5 shrink-0", blocked ? "text-err" : "text-faint")} aria-label="hook" />}
        {env && env !== "internet" && <Badge tone={/prod/i.test(env) ? "err" : "neutral"}>{env}</Badge>}
        {risk && !isSub && <Badge tone={riskTone(risk)}>{risk}</Badge>}
        {result?.durationMs !== undefined && <span className="text-[10.5px] text-faint tabular-nums shrink-0">{fmtMs(result.durationMs)}</span>}
        {statusEl}
      </button>
      {expanded && (
        <div className="border-t border-line px-3 py-2.5 space-y-2.5">
          {isSub ? (
            <SubagentBody item={item} ctx={ctx} live={!!live} />
          ) : (
            <>
              <CallInput tool={tool} input={input} cmd={cmd} hasView={!!result?.view && !result.isError} />
              {item.hooks.map((h) => (
                <HookNote key={h.id} h={h} />
              ))}
              {item.approvals.map((a) => (
                <ApprovalBox key={a.req.approvalId} ctx={ctx} item={a} />
              ))}
              {item.question && <QuestionBox ctx={ctx} q={item.question} answer={item.answer} done={!!result} />}
              {progress && (
                <div className="flex items-center gap-2 text-[12px] text-info">
                  <Loader2 className="size-3.5 animate-spin" /> {progress}
                </div>
              )}
              {tool === "ansible_run" && !pendingApproval && !denied && <AnsibleRunLink ws={ctx.ws} output={result?.output} />}
              {result && tool === "todo" && item.todo && <TodoList items={item.todo} />}
              {result && result.view && <ToolViewBlock view={result.view} />}
              {result && !(tool === "ask_user" && !result.isError) && !(tool === "todo" && !result.isError) && (
                <ToolOutput output={result.output} isError={result.isError} collapsed={viewIsEnough(result.view)} full={result.outputId ? { ...ctx, id: result.outputId, size: result.outputSize } : undefined} />
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
}

function CallInput({ tool, input, cmd, hasView }: { tool: string; input: unknown; cmd: string | null; hasView: boolean }) {
  if (cmd)
    return (
      <div className="rounded-md bg-panel-2 border border-line px-3 py-2 font-mono text-[12px] whitespace-pre-wrap break-all">
        <span className="text-accent select-none">$ </span>
        {cmd}
      </div>
    );
  // the header already shows what these do, and a view shows what was asked
  if (hasView || ["ask_user", "todo", "web_fetch", "web_search", "read_output", "tasks"].includes(tool) || input === undefined) return null;
  return <CodeBlock maxHeight={200}>{JSON.stringify(input, null, 2)}</CodeBlock>;
}

function HookNote({ h }: { h: Ev<"hook"> }) {
  const tone = h.outcome === "deny" || h.outcome === "error" ? "text-err" : h.outcome === "ask" ? "text-warn" : "text-muted";
  const what = { deny: "blocked this call", ask: "asked for approval", allow: "allowed this call", ok: "ran", error: "failed" }[h.outcome];
  return (
    <div className={cx("flex items-start gap-2 text-[12px]", tone)}>
      <Webhook className="size-3.5 shrink-0 mt-0.5" />
      <span>
        Hook <b className="font-medium">{h.hook}</b> ({h.event.replace("_", " ")}) {what}
        {h.message && <span className="text-muted"> — {h.message}</span>}
      </span>
    </div>
  );
}

export function TodoList({ items, compact }: { items: Ev<"todo">["items"]; compact?: boolean }) {
  return (
    <ul className={cx("space-y-0.5", compact ? "text-[12px]" : "text-[12.5px]")}>
      {items.map((it, i) => (
        <li key={i} className="flex items-start gap-2">
          <span className="mt-[3px] shrink-0">
            {it.status === "done" ? (
              <CircleCheck className="size-3.5 text-ok" />
            ) : it.status === "in_progress" ? (
              <Loader2 className="size-3.5 text-info animate-spin" />
            ) : it.status === "skipped" ? (
              <SkipForward className="size-3.5 text-faint" />
            ) : (
              <span className="block size-3.5 rounded-full border border-line-strong" />
            )}
          </span>
          <span
            dir="auto"
            className={cx(
              "text-start",
              it.status === "done" && "text-muted",
              it.status === "skipped" && "text-faint line-through",
              it.status === "in_progress" && "text-fg font-medium",
            )}
          >
            {it.text}
          </span>
        </li>
      ))}
    </ul>
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

function ToolOutput({ output, isError, collapsed, full }: { output: string; isError: boolean; collapsed?: boolean; full?: { ws: string; sid: string; id: string; size?: number } }) {
  const [all, setAll] = useState(false);
  const [show, setShow] = useState(!collapsed || isError);
  const lines = (output ?? "").split("\n");
  const long = lines.length > 40;
  const shown = all || !long ? output : lines.slice(0, 40).join("\n");
  if (!show)
    return (
      <button onClick={() => setShow(true)} className="text-[11px] text-faint hover:text-fg">
        ▸ Text sent to the agent ({lines.length} lines)
      </button>
    );
  return (
    <div>
      <div className="flex items-center gap-2 mb-1">
        <span className="text-[10.5px] uppercase tracking-wide text-faint">{isError ? "Error" : collapsed ? "Text sent to the agent" : "Output"}</span>
        <span className="flex-1" />
        {full && (
          <a href={api.outputUrl(full.ws, full.sid, full.id, true)} className="inline-flex items-center gap-1 text-[11px] text-accent hover:underline" title="The model got the start and the end; it can read the rest with read_output">
            <Download className="size-3" /> Full output{full.size ? ` (${fmtBytes(full.size)})` : ""}
          </a>
        )}
      </div>
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

export function DiffPreview({ text }: { text: string }) {
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

function ApprovalBox({ ctx, item }: { ctx: Ctx; item: ApprovalItem }) {
  const { req: approval, resolved } = item;
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const [denyOpen, setDenyOpen] = useState(false);
  const [note, setNote] = useState("");
  const [local, setLocal] = useState<{ d: string; canary?: boolean } | null>(null);
  const [secret, setSecret] = useState("");
  const [secretErr, setSecretErr] = useState("");
  const decision = resolved?.decision ?? local?.d;
  const canaryChosen = resolved?.canary ?? local?.canary;
  const asks = approval.secret;
  const step = approval.stage === "next";
  const hosts = approval.hosts ?? [];
  const canary = !step && hosts.length > 1;
  const canaryAsked = canary && !!approval.canary;

  async function decide(d: "approve" | "deny" | "always", asCanary?: boolean) {
    if (asks && d !== "deny" && !secret) {
      setSecretErr("Enter the password, or deny.");
      return;
    }
    setBusy(asCanary ? "canary" : d);
    setSecretErr("");
    try {
      await api.approve(ctx.ws, ctx.sid, approval.approvalId, d, d === "deny" ? note || undefined : undefined, asks && d !== "deny" ? secret : undefined, asCanary);
      setLocal({ d, canary: asCanary });
      setSecret("");
    } catch (e) {
      if (asks && e instanceof ApiError && e.code === "bad_secret") setSecretErr(e.message);
      else toast.error("Decision failed", errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  const destructive = approval.risk === "destructive";
  const failedCanary = step && /FAILED/.test(approval.reason);
  return (
    <div className={cx("rounded-md border p-3", destructive || failedCanary ? "border-err/40 bg-err/5" : step ? "border-info/40 bg-info/5" : "border-warn/40 bg-warn/5")}>
      <div className="flex items-start gap-2">
        {asks ? (
          <KeyRound className="size-4 shrink-0 mt-0.5 text-warn" />
        ) : (
          <ShieldAlert className={cx("size-4 shrink-0 mt-0.5", destructive || failedCanary ? "text-err" : step ? "text-info" : "text-warn")} />
        )}
        <div className="flex-1 min-w-0">
          <div className="text-[13px] font-medium">
            {step ? (
              approval.title || "Continue?"
            ) : (
              <>
                {decision ? "Approval" : asks ? "The agent needs a password for this" : approval.risk === "read" ? "The agent wants to read this" : "The agent wants to run this"}
                {approval.target && (
                  <>
                    {" "}
                    on <span className="font-mono">{approval.target}</span>
                  </>
                )}
                {approval.environment && approval.environment !== "internet" && (
                  <>
                    {" "}
                    in <Badge tone={/prod/i.test(approval.environment) ? "err" : "neutral"}>{approval.environment}</Badge>
                  </>
                )}
              </>
            )}
          </div>
          {approval.reason && <div className={cx("text-xs mt-0.5", failedCanary ? "text-err" : "text-muted")}>{approval.reason}</div>}
          {approval.grantLabel && !destructive && !step && <div className="text-[11px] text-faint mt-0.5">“Always allow” = {approval.grantLabel.replace(/^Always allow /, "")}</div>}
        </div>
        <Badge tone={riskTone(approval.risk)}>{approval.risk}</Badge>
      </div>
      {canary && (
        <div className="mt-2 flex flex-wrap items-center gap-1">
          <span className="text-[11px] text-faint mr-1">{hosts.length} hosts</span>
          {hosts.map((h, i) => (
            <Badge key={h} mono tone={i === 0 ? "accent" : "neutral"}>
              {h}
            </Badge>
          ))}
        </div>
      )}
      {approval.preview && (
        <div className="mt-2.5">
          <DiffPreview text={approval.preview} />
        </div>
      )}
      {decision ? (
        <div className="mt-2.5 text-xs flex items-center gap-1.5">
          {decision === "deny" ? (
            <>
              <X className="size-3.5 text-err" /> <span className="text-err">{step ? "Stopped here" : "Denied"}</span>
            </>
          ) : (
            <>
              <Check className="size-3.5 text-ok" />{" "}
              <span className="text-ok">
                {step
                  ? "Continued"
                  : canaryChosen
                    ? `Approved as a canary: ${hosts[0]} first`
                    : decision === "always"
                      ? asks
                        ? "Approved for this session, password kept until opsdeck locks"
                        : "Approved (always in this session)"
                      : "Approved"}
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
              <div className={cx("text-[11px] mt-1", secretErr ? "text-err" : "text-faint")}>{secretErr || "opsdeck uses it for this action only; the agent never sees it."}</div>
            </div>
          )}
          {denyOpen && (
            <Input
              dir="auto"
              autoFocus={!asks}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={step ? "Optional: tell the agent why" : "Optional: tell the agent why / what to do instead"}
              onKeyDown={(e) => e.key === "Enter" && void decide("deny")}
            />
          )}
          <div className="flex items-center gap-2 flex-wrap">
            <Button variant="primary" size="sm" icon={<Check className="size-3.5" />} loading={busy === "approve"} disabled={!!busy} onClick={() => void decide("approve")}>
              {step ? `Continue` : asks ? "Unlock once" : canaryAsked ? `Approve: ${hosts[0]} first, then ask` : canary ? `Approve all ${hosts.length}` : "Approve"}
            </Button>
            {canary && !canaryAsked && (
              <Button size="sm" loading={busy === "canary"} disabled={!!busy} onClick={() => void decide("approve", true)} title={`Run on ${hosts[0]} first; you decide on the other ${hosts.length - 1} after seeing the result`}>
                Canary: {hosts[0]} first
              </Button>
            )}
            {!destructive && !step && (
              <Button
                size="sm"
                variant={canary ? "ghost" : "secondary"}
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
                {step ? "Stop here" : "Deny"}
              </Button>
            ) : (
              <Button size="sm" variant="ghost" icon={<X className="size-3.5" />} onClick={() => setDenyOpen(true)}>
                {step ? "Stop here…" : "Deny…"}
              </Button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function QuestionBox({ ctx, q, answer, done }: { ctx: Ctx; q: Ev<"question">; answer?: Ev<"question_answered">; done: boolean }) {
  const toast = useToast();
  const [picked, setPicked] = useState<string[]>([]);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState<{ choices: string[]; text: string; skipped?: boolean } | null>(null);
  const a = answer ?? sent;

  async function send(skipped = false, choices = picked) {
    setBusy(true);
    try {
      const body = skipped ? { skipped: true } : { choices, text: text.trim() };
      await api.answer(ctx.ws, ctx.sid, q.questionId, body);
      setSent({ choices: skipped ? [] : choices, text: skipped ? "" : text.trim(), skipped });
    } catch (e) {
      toast.error("Answer failed", errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="rounded-md border border-accent/40 bg-accent/5 p-3">
      <div className="flex items-start gap-2">
        <MessageCircleQuestionMark className="size-4 shrink-0 mt-0.5 text-accent" />
        <div dir={textDir(q.question)} className="flex-1 text-[13.5px] font-medium text-start whitespace-pre-wrap">
          {q.question}
        </div>
      </div>
      {a ? (
        <div className="mt-2 ml-6 text-[12.5px] flex items-start gap-1.5">
          {a.skipped ? (
            <span className="text-muted">Skipped — the agent decides itself</span>
          ) : (
            <>
              <Check className="size-3.5 text-ok mt-0.5 shrink-0" />
              <span dir="auto" className="text-start">
                {[...(a.choices ?? []), a.text].filter(Boolean).join(" — ")}
              </span>
            </>
          )}
        </div>
      ) : done ? (
        <div className="mt-2 ml-6 text-[12px] text-faint">Not answered (the turn ended).</div>
      ) : (
        <div className="mt-2.5 ml-6 space-y-2">
          {q.options.length > 0 && (
            <div className="grid gap-1.5">
              {q.options.map((o) => {
                const on = picked.includes(o.label);
                return (
                  <button
                    key={o.label}
                    disabled={busy}
                    onClick={() => {
                      if (!q.multiple && !q.allowText) return void send(false, [o.label]);
                      setPicked(q.multiple ? (on ? picked.filter((x) => x !== o.label) : [...picked, o.label]) : on ? [] : [o.label]);
                    }}
                    className={cx(
                      "flex items-start gap-2.5 rounded-md border px-3 py-2 text-left transition-colors",
                      on ? "border-accent bg-accent/10" : "border-line bg-panel hover:border-line-strong hover:bg-panel-2",
                    )}
                  >
                    <span className={cx("mt-0.5 size-3.5 shrink-0 border grid place-items-center", q.multiple ? "rounded" : "rounded-full", on ? "border-accent bg-accent text-accent-fg" : "border-line-strong")}>
                      {on && <Check className="size-2.5" />}
                    </span>
                    <span className="min-w-0">
                      <span dir="auto" className="block text-[12.5px] font-medium text-start">
                        {o.label}
                      </span>
                      {o.description && (
                        <span dir="auto" className="block text-[11.5px] text-muted text-start">
                          {o.description}
                        </span>
                      )}
                    </span>
                  </button>
                );
              })}
            </div>
          )}
          {q.allowText && (
            <Input
              dir="auto"
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder={q.options.length ? "Or type your own answer…" : "Your answer…"}
              onKeyDown={(e) => e.key === "Enter" && (picked.length || text.trim()) && void send()}
            />
          )}
          <div className="flex items-center gap-2">
            {(q.multiple || q.allowText) && (
              <Button size="sm" variant="primary" icon={<Send className="size-3.5" />} loading={busy} disabled={!picked.length && !text.trim()} onClick={() => void send()}>
                Answer
              </Button>
            )}
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => void send(true)}>
              Skip
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- subagents

function SubagentBody({ item, ctx, live }: { item: ToolItem; ctx: Ctx; live: boolean }) {
  const [showTask, setShowTask] = useState(false);
  const input = (item.call?.input ?? {}) as { task?: string; context?: string };
  const start = item.sub?.start;
  const end = item.sub?.end;
  const report = item.result && !item.result.isError ? item.result.output.replace(/^Report of subagent [^\n]*\n+/, "") : "";
  const blocks: Block[] = groupBlocks(item.sub?.items ?? []);
  // the report is the subagent's last message: show it once, as the report
  const last = blocks[blocks.length - 1];
  if (report && last?.kind === "assistant" && last.text.trim() === report.trim()) blocks.pop();
  const running = !item.result;
  return (
    <div className="space-y-2.5">
      <div className="flex items-center gap-2 text-[11.5px] text-muted flex-wrap">
        <Bot className="size-3.5 text-agent-ink" />
        <span>
          Subagent <b className="text-fg font-medium">{start?.agent ?? "…"}</b>
          {start?.model && (
            <>
              {" "}
              on <span className="font-mono">{start.model}</span>
            </>
          )}
        </span>
        {end && (
          <span className="text-faint">
            · {end.steps} steps · {end.toolCalls} tool calls · {fmtTokens(end.inputTokens)} in / {fmtTokens(end.outputTokens)} out
          </span>
        )}
        <span className="flex-1" />
        <button onClick={() => setShowTask((s) => !s)} className="text-[11px] text-accent hover:underline">
          {showTask ? "Hide task" : "Task"}
        </button>
      </div>
      {showTask && (
        <div dir="auto" className="rounded-md border border-line bg-panel-2 px-3 py-2 text-[12.5px] whitespace-pre-wrap text-start">
          {input.task}
          {input.context && <div className="mt-2 text-muted">{input.context}</div>}
        </div>
      )}
      {blocks.length > 0 && (
        <div className="border-l-2 border-agent/35 pl-3 space-y-2 -ml-0.5">
          {blocks.map((b, i) =>
            b.kind === "tools" ? (
              <ToolGroup key={b.id} items={b.items} ctx={ctx} live={live && running && i === blocks.length - 1} expandAll={false} flush />
            ) : b.kind === "assistant" ? (
              <div key={b.id} className="text-[12.5px] text-muted">
                {b.text ? <Markdown dir={textDir(b.text) === "rtl" ? "rtl" : "auto"}>{b.text}</Markdown> : <span className="text-faint">…</span>}
                {b.streaming && <span className="inline-block w-1.5 h-3.5 bg-agent-ink/70 ml-0.5 align-text-bottom pulse-dot" />}
              </div>
            ) : b.kind === "error" ? (
              <div key={b.id} className="text-[12px] text-err">
                {b.message}
              </div>
            ) : null,
          )}
        </div>
      )}
      {running && live && (
        <div className="flex items-center gap-2 text-muted text-xs">
          <Loader2 className="size-3.5 animate-spin" /> Working…
        </div>
      )}
      {report && (
        <div className="rounded-md border border-line bg-panel-2 p-3">
          <div className="text-[10.5px] uppercase tracking-wide text-faint mb-1">Report</div>
          <div className="text-[13px]">
            <Markdown dir={textDir(report) === "rtl" ? "rtl" : "auto"}>{report}</Markdown>
          </div>
        </div>
      )}
      {item.result?.isError && <ToolOutput output={item.result.output} isError />}
    </div>
  );
}
