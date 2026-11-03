import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowUp, FileText, Loader2, Paperclip, Server, Slash, Square, SquareTerminal, X } from "lucide-react";
import { Button, cx } from "../../components/ui";
import { useToast } from "../../components/overlay";
import { fmtBytes } from "../../lib/format";

/** a file picked for the next message, not uploaded yet */
type Pending = { key: string; file: File; url?: string };

const MAX_FILE = 20 << 20;
const MAX_IMAGE = 5 << 20;
const MAX_FILES = 10;

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
  /** files: what the user attached (only with a message, not with / or !) */
  onSend: (text: string, files: File[]) => Promise<boolean> | boolean;
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
  const toast = useToast();
  const [text, setText] = useState(seed ?? "");
  const [files, setFiles] = useState<Pending[]>([]);
  const [dragging, setDragging] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const filesRef = useRef(files);
  filesRef.current = files;
  // previews are object URLs: free them
  useEffect(() => () => filesRef.current.forEach((f) => f.url && URL.revokeObjectURL(f.url)), []);

  function addFiles(list: FileList | File[] | null) {
    if (!list) return;
    const add: Pending[] = [];
    for (const f of Array.from(list)) {
      const image = /^image\/(png|jpeg|gif|webp)$/.test(f.type);
      if (f.size > (image ? MAX_IMAGE : MAX_FILE)) {
        toast.error(`${f.name || "The file"} is too large`, image ? "Images can be up to 5 MB." : "Files can be up to 20 MB.");
        continue;
      }
      if (f.size === 0) continue;
      add.push({ key: `${Date.now()}-${Math.random()}`, file: f, url: image ? URL.createObjectURL(f) : undefined });
    }
    setFiles((cur) => {
      const next = [...cur, ...add];
      if (next.length > MAX_FILES) {
        toast.warning(`At most ${MAX_FILES} files per message`);
        next.slice(MAX_FILES).forEach((p) => p.url && URL.revokeObjectURL(p.url));
      }
      return next.slice(0, MAX_FILES);
    });
  }

  function removeFile(key: string) {
    setFiles((cur) => {
      cur.filter((p) => p.key === key).forEach((p) => p.url && URL.revokeObjectURL(p.url));
      return cur.filter((p) => p.key !== key);
    });
  }
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
    // files go with a message to the agent, not with a command
    const withFiles = files.length > 0 && !(t.startsWith("/") || (shell && t.startsWith("!")));
    if ((!t && !withFiles) || disabled || sending || (running && !(shell && t.startsWith("!")))) return;
    setSending(true);
    try {
      const ok = await onSend(t, withFiles ? files.map((p) => p.file) : []);
      if (ok !== false) {
        setText("");
        if (withFiles) {
          files.forEach((p) => p.url && URL.revokeObjectURL(p.url));
          setFiles([]);
        }
      }
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
          sh ? "border-info/60 focus-within:border-info" : "border-line-strong focus-within:border-agent/70 focus-within:ring-2 focus-within:ring-agent/15",
          dragging && "border-agent ring-2 ring-agent/25",
        )}
        onDragOver={(e) => {
          if (disabled || !e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false);
        }}
        onDrop={(e) => {
          if (disabled || !e.dataTransfer.files.length) return;
          e.preventDefault();
          setDragging(false);
          addFiles(e.dataTransfer.files);
        }}
      >
        {files.length > 0 && (
          <div className="flex flex-wrap gap-1.5 px-3 pt-2.5">
            {files.map((p) => (
              <div key={p.key} className="group relative flex items-center gap-2 rounded-lg border border-line bg-panel-2 pe-2 max-w-[220px]" title={`${p.file.name} · ${fmtBytes(p.file.size)}`}>
                {p.url ? (
                  <img src={p.url} alt={p.file.name} className="size-10 rounded-s-lg object-cover" />
                ) : (
                  <span className="grid size-10 place-items-center rounded-s-lg bg-panel-3 text-muted">
                    <FileText className="size-4" />
                  </span>
                )}
                <span className="min-w-0 py-1">
                  <span className="block truncate text-[11.5px] font-medium">{p.file.name || "pasted image"}</span>
                  <span className="block text-[10.5px] text-faint">{fmtBytes(p.file.size)}</span>
                </span>
                <button
                  type="button"
                  aria-label={`Remove ${p.file.name}`}
                  onClick={() => removeFile(p.key)}
                  className="absolute -top-1.5 -end-1.5 hidden group-hover:grid size-4 place-items-center rounded-full bg-panel border border-line-strong text-muted hover:text-fg"
                >
                  <X className="size-2.5" />
                </button>
              </div>
            ))}
          </div>
        )}
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
          onPaste={(e) => {
            // a screenshot or files from the clipboard become attachments
            const pasted = Array.from(e.clipboardData.files);
            if (pasted.length) {
              e.preventDefault();
              addFiles(pasted.map((f, i) => (f.name && f.name !== "image.png" ? f : new File([f], `pasted-${new Date().toISOString().slice(11, 19).replace(/:/g, "")}${i ? `-${i}` : ""}.${f.type.split("/")[1] || "png"}`, { type: f.type }))));
            }
          }}
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
          <input
            ref={fileInput}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              addFiles(e.target.files);
              e.target.value = "";
            }}
          />
          <button
            type="button"
            title="Attach files or images (or paste, or drop them here)"
            aria-label="Attach files"
            disabled={disabled}
            onClick={() => fileInput.current?.click()}
            className="grid size-7 place-items-center rounded-md text-muted hover:text-fg hover:bg-panel-3 disabled:opacity-40"
          >
            <Paperclip className="size-4" />
          </button>
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
              disabled={(!text.trim() && !files.length) || disabled || sending || (running && !sh)}
              className={cx("size-8 rounded-lg grid place-items-center disabled:opacity-30 hover:brightness-110 transition", sh ? "bg-info text-white" : "bg-agent text-agent-fg shadow-sm shadow-agent/30")}
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
