import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowUp, Loader2, Server, Slash, Square, SquareTerminal } from "lucide-react";
import { Button, cx } from "../../components/ui";

export type SlashCommand = { name: string; args?: string; description: string; workspace?: boolean };

/** commands the chat handles itself; the workspace's own (commands/*.md) go to the agent */
export const BUILTIN_COMMANDS: SlashCommand[] = [
  { name: "plan", description: "Plan mode: read-only investigation that ends with a plan" },
  { name: "ask", description: "Ask mode: every change waits for your approval" },
  { name: "auto", description: "Auto mode: this computer runs without asking" },
  { name: "full", description: "Full mode: nothing on this computer asks" },
  { name: "model", args: "<provider/model>", description: "Switch this session's model" },
  { name: "compact", description: "Summarize the conversation so far to free context" },
  { name: "fork", description: "Copy this session into a new one" },
  { name: "export", args: "[report|runbook|skill]", description: "Save this session to the workspace" },
  { name: "tasks", description: "Show the background tasks" },
  { name: "new", description: "Start a new session" },
  { name: "ssh", args: "<host>", description: "Open a terminal on a host" },
  { name: "logs", description: "Open the logs" },
  { name: "vault", description: "Open the vault" },
  { name: "docs", description: "Open the docs" },
  { name: "topology", description: "Open the topology map" },
];

/** parses "!cmd" (this computer) and "!@host cmd" */
export function parseShell(text: string): { target: string; command: string } | null {
  if (!text.startsWith("!")) return null;
  const m = /^!@(\S+)\s*([\s\S]*)$/.exec(text);
  if (m) return { target: m[1], command: m[2].trim() };
  return { target: "", command: text.slice(1).trim() };
}

type Suggestion = { key: string; label: string; hint?: string; description?: string; insert: string; submit?: boolean; workspace?: boolean };

export function Composer({
  onSend,
  disabled,
  running,
  onStop,
  left,
  placeholder,
  autoFocus,
  commands = BUILTIN_COMMANDS,
  hosts = [],
  shell = true,
  seed,
}: {
  onSend: (text: string) => Promise<boolean> | boolean;
  disabled?: boolean;
  running?: boolean;
  onStop?: () => void;
  left?: React.ReactNode;
  placeholder?: string;
  autoFocus?: boolean;
  commands?: SlashCommand[];
  hosts?: string[];
  /** ! runs commands (off in a new session) */
  shell?: boolean;
  /** prefills the box (e.g. a message forked to be edited) */
  seed?: string;
}) {
  const [text, setText] = useState(seed ?? "");
  const [sending, setSending] = useState(false);
  const [sel, setSel] = useState(0);
  const [dismissed, setDismissed] = useState("");
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
  useEffect(() => {
    if (seed) {
      setText(seed);
      ref.current?.focus();
    }
  }, [seed]);

  const sh = shell ? parseShell(text) : null;
  const suggestions = useMemo<Suggestion[]>(() => {
    if (dismissed === text) return [];
    const cm = /^\/(\S*)$/.exec(text);
    if (cm) {
      const q = cm[1].toLowerCase();
      return commands
        .filter((c) => c.name.toLowerCase().startsWith(q) || (q.length > 1 && c.name.toLowerCase().includes(q)))
        .slice(0, 12)
        .map((c) => ({ key: c.name, label: "/" + c.name, hint: c.args, description: c.description, insert: "/" + c.name + (c.args ? " " : ""), submit: !c.args, workspace: c.workspace }));
    }
    const hm = shell ? /^!@(\S*)$/.exec(text) : null;
    if (hm) {
      const q = hm[1].toLowerCase();
      return hosts
        .filter((h) => h.toLowerCase().includes(q))
        .slice(0, 12)
        .map((h) => ({ key: h, label: h, description: "run on this host", insert: `!@${h} ` }));
    }
    return [];
  }, [text, commands, hosts, shell, dismissed]);
  useEffect(() => setSel(0), [suggestions.length, text]);

  async function send(t = text.trim()) {
    if (!t || disabled || sending || (running && !(shell && t.startsWith("!")))) return;
    setSending(true);
    try {
      const ok = await onSend(t);
      if (ok !== false) setText("");
    } finally {
      setSending(false);
      ref.current?.focus();
    }
  }

  function pick(s: Suggestion) {
    if (s.submit && text.trim() === s.insert) {
      void send(s.insert);
      return;
    }
    setText(s.insert);
    setDismissed(s.insert);
    ref.current?.focus();
  }

  return (
    <div className="relative">
      {suggestions.length > 0 && (
        <div className="absolute bottom-full mb-2 left-0 right-0 z-20 rounded-lg border border-line-strong bg-panel shadow-xl shadow-black/30 py-1 max-h-72 overflow-y-auto">
          {suggestions.map((s, i) => (
            <button
              key={s.key}
              onMouseDown={(e) => {
                e.preventDefault();
                pick(s);
              }}
              onMouseEnter={() => setSel(i)}
              className={cx("w-full flex items-center gap-2.5 px-3 h-8 text-left", i === sel && "bg-panel-3")}
            >
              {text.startsWith("!") ? <Server className="size-3.5 text-muted shrink-0" /> : <Slash className="size-3.5 text-muted shrink-0" />}
              <span className="font-mono text-[12.5px] font-medium shrink-0">{s.label}</span>
              {s.hint && <span className="font-mono text-[11.5px] text-faint shrink-0">{s.hint}</span>}
              <span dir="auto" className="text-[12px] text-muted truncate flex-1 min-w-0 text-start">
                {s.description}
              </span>
              {s.workspace && <span className="text-[10.5px] text-accent shrink-0">workspace</span>}
            </button>
          ))}
        </div>
      )}
      <div
        className={cx(
          "rounded-xl border bg-panel shadow-lg shadow-black/10 transition-colors",
          sh ? "border-info/60 focus-within:border-info" : "border-line-strong focus-within:border-accent/60",
        )}
      >
        {sh && (
          <div className="flex items-center gap-1.5 px-3.5 pt-2 text-[11px] text-info">
            <SquareTerminal className="size-3.5" />
            <span>
              Shell — runs {sh.target ? <b className="font-mono font-medium">on {sh.target}</b> : <b className="font-medium">on this computer</b>} as you, without approval;
              its output goes to the agent with your next message.
            </span>
          </div>
        )}
        <textarea
          ref={ref}
          dir="auto"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (suggestions.length > 0) {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                setSel((i) => (i + (e.key === "ArrowDown" ? 1 : suggestions.length - 1)) % suggestions.length);
                return;
              }
              if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing)) {
                e.preventDefault();
                pick(suggestions[sel]);
                return;
              }
              if (e.key === "Escape") {
                e.preventDefault();
                setDismissed(text);
                return;
              }
            }
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              void send();
            }
          }}
          rows={1}
          placeholder={placeholder ?? "Ask the agent to investigate or change something…   / for commands · ! to run a command yourself"}
          className={cx("w-full resize-none bg-transparent px-3.5 pb-1 text-[13.5px] leading-relaxed outline-none placeholder:text-faint", sh ? "pt-1 font-mono text-[13px]" : "pt-3")}
          disabled={disabled}
        />
        <div className="flex items-center gap-2 px-2 pb-2">
          {left}
          <div className="flex-1" />
          <span className="text-[10.5px] text-faint hidden md:inline">
            <span className="font-mono">/</span> commands{shell && <> · <span className="font-mono">!</span> shell · <span className="font-mono">!@host</span></>}
          </span>
          {running && !sh ? (
            <Button size="sm" variant="danger" icon={<Square className="size-3 fill-current" />} onClick={onStop}>
              Stop
            </Button>
          ) : (
            <button
              onClick={() => void send()}
              disabled={!text.trim() || disabled || sending || (running && !sh)}
              className={cx("size-8 rounded-lg grid place-items-center disabled:opacity-30 hover:brightness-110 transition", sh ? "bg-info text-white" : "bg-accent text-accent-fg")}
              aria-label={sh ? "Run" : "Send"}
            >
              {sending ? <Loader2 className="size-4 animate-spin" /> : sh ? <SquareTerminal className="size-4" /> : <ArrowUp className="size-4" />}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
