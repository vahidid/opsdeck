import { forwardRef, useEffect, useRef, useState } from "react";
import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, SelectHTMLAttributes, TextareaHTMLAttributes } from "react";
import { Check, Copy, Loader2, X } from "lucide-react";

export function cx(...c: (string | false | null | undefined)[]): string {
  return c.filter(Boolean).join(" ");
}

type Variant = "primary" | "secondary" | "ghost" | "danger" | "subtle";
type Size = "xs" | "sm" | "md";
const variants: Record<Variant, string> = {
  primary: "bg-accent text-accent-fg hover:brightness-110 border border-transparent",
  secondary: "bg-panel-2 text-fg border border-line hover:bg-panel-3 hover:border-line-strong",
  ghost: "text-muted hover:text-fg hover:bg-panel-3 border border-transparent",
  subtle: "bg-panel-3 text-fg border border-transparent hover:border-line-strong",
  danger: "bg-err/15 text-err border border-err/30 hover:bg-err/25",
};
const sizes: Record<Size, string> = {
  xs: "h-6 px-2 text-[11px] gap-1 rounded",
  sm: "h-7 px-2.5 text-xs gap-1.5 rounded-md",
  md: "h-8 px-3 text-[13px] gap-2 rounded-md",
};

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  icon?: ReactNode;
};
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", size = "md", loading, icon, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      className={cx(
        "inline-flex items-center justify-center font-medium whitespace-nowrap transition-colors select-none disabled:opacity-50 disabled:pointer-events-none focus-visible:outline-2 focus-visible:outline-accent",
        variants[variant],
        sizes[size],
        className,
      )}
      disabled={disabled || loading}
      {...rest}
    >
      {loading ? <Loader2 className="size-3.5 animate-spin" /> : icon}
      {children}
    </button>
  );
});

export function IconButton({
  title,
  className,
  children,
  active,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { title: string; active?: boolean }) {
  return (
    <button
      title={title}
      aria-label={title}
      className={cx(
        "inline-flex size-7 items-center justify-center rounded-md text-muted hover:text-fg hover:bg-panel-3 disabled:opacity-40 disabled:pointer-events-none transition-colors",
        active && "bg-panel-3 text-fg",
        className,
      )}
      {...rest}
    >
      {children}
    </button>
  );
}

const hasWidth = (c?: string) => /(^|\s)(!?w-|flex-1)/.test(c ?? "");
const hasHeight = (c?: string) => /(^|\s)!?h-/.test(c ?? "");
const inputBase =
  "bg-panel-2 border border-line rounded-md px-2.5 text-[13px] placeholder:text-faint focus:outline-none focus:border-accent focus:ring-2 focus:ring-accent/20 disabled:opacity-60";

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { mono?: boolean }>(
  function Input({ className, mono, ...rest }, ref) {
    return <input ref={ref} className={cx(inputBase, !hasWidth(className) && "w-full", !hasHeight(className) && "h-8", mono && "font-mono text-xs", className)} {...rest} />;
  },
);

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean }>(
  function Textarea({ className, mono, ...rest }, ref) {
    return <textarea ref={ref} className={cx(inputBase, !hasWidth(className) && "w-full", "py-1.5 min-h-20", mono && "font-mono text-xs", className)} {...rest} />;
  },
);

export function Select({ className, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select className={cx(inputBase, !hasWidth(className) && "w-full", !hasHeight(className) && "h-8", "pr-7 cursor-pointer", className)} {...rest}>
      {children}
    </select>
  );
}

export function Label({ children, hint, required }: { children: ReactNode; hint?: ReactNode; required?: boolean }) {
  return (
    <div className="mb-1 flex items-baseline justify-between gap-2">
      <span className="text-xs font-medium text-muted">
        {children}
        {required && <span className="text-err ml-0.5">*</span>}
      </span>
      {hint && <span className="text-[11px] text-faint">{hint}</span>}
    </div>
  );
}

export function FormField({
  label,
  help,
  required,
  children,
  className,
}: {
  label: ReactNode;
  help?: ReactNode;
  required?: boolean;
  children: ReactNode;
  className?: string;
}) {
  return (
    <label className={cx("block", className)}>
      <Label required={required}>{label}</Label>
      {children}
      {help && <div className="mt-1 text-[11px] text-faint">{help}</div>}
    </label>
  );
}

export function Toggle({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: ReactNode;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className="flex items-center gap-2 text-[13px] text-left disabled:opacity-50"
    >
      <span
        className={cx(
          "relative inline-block h-4 w-7 rounded-full transition-colors",
          checked ? "bg-accent" : "bg-panel-3 border border-line-strong",
        )}
      >
        <span
          className={cx(
            "absolute top-0.5 size-3 rounded-full bg-white shadow transition-all",
            checked ? "left-3.5" : "left-0.5",
          )}
        />
      </span>
      {label}
    </button>
  );
}

export type Tone = "neutral" | "ok" | "warn" | "err" | "info" | "accent";
const tones: Record<Tone, string> = {
  neutral: "bg-panel-3 text-muted border-line",
  ok: "bg-ok/12 text-ok border-ok/25",
  warn: "bg-warn/12 text-warn border-warn/25",
  err: "bg-err/12 text-err border-err/25",
  info: "bg-info/12 text-info border-info/25",
  accent: "bg-accent/15 text-accent border-accent/30",
};
export function Badge({
  tone = "neutral",
  children,
  className,
  mono,
  title,
}: {
  tone?: Tone;
  children: ReactNode;
  className?: string;
  mono?: boolean;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cx(
        "inline-flex items-center gap-1 rounded border px-1.5 h-[18px] text-[10.5px] font-medium leading-none whitespace-nowrap",
        tones[tone],
        mono && "font-mono",
        className,
      )}
    >
      {children}
    </span>
  );
}

export function riskTone(risk?: string): Tone {
  if (risk === "read") return "ok";
  if (risk === "change") return "warn";
  if (risk === "destructive") return "err";
  return "neutral";
}

export function StatusDot({ tone, pulse }: { tone: Tone; pulse?: boolean }) {
  const color: Record<Tone, string> = {
    neutral: "bg-faint",
    ok: "bg-ok",
    warn: "bg-warn",
    err: "bg-err",
    info: "bg-info",
    accent: "bg-accent",
  };
  return <span className={cx("inline-block size-2 rounded-full shrink-0", color[tone], pulse && "pulse-dot")} />;
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cx("size-4 animate-spin text-muted", className)} />;
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-12 text-muted">
      <Spinner /> {label}
    </div>
  );
}

export function EmptyState({
  icon,
  title,
  children,
  action,
  className,
}: {
  icon?: ReactNode;
  title: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cx("flex flex-col items-center justify-center text-center py-14 px-6", className)}>
      {icon && <div className="mb-3 text-faint [&_svg]:size-8">{icon}</div>}
      <div className="text-[14px] font-medium">{title}</div>
      {children && <div className="mt-1 max-w-md text-muted">{children}</div>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function ErrorBox({ error, onRetry }: { error: string; onRetry?: () => void }) {
  return (
    <div className="m-4 rounded-md border border-err/30 bg-err/8 p-3 text-err flex items-start justify-between gap-4">
      <div className="text-[13px] whitespace-pre-wrap break-words">{error}</div>
      {onRetry && (
        <Button size="sm" variant="secondary" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  );
}

export function Card({
  title,
  actions,
  children,
  className,
  bodyClass,
}: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  bodyClass?: string;
}) {
  return (
    <section className={cx("rounded-lg border border-line bg-panel", className)}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-2 border-b border-line px-3 h-10">
          <h3 className="text-[13px] font-semibold flex items-center gap-2">{title}</h3>
          <div className="flex items-center gap-1">{actions}</div>
        </header>
      )}
      <div className={cx("p-3", bodyClass)}>{children}</div>
    </section>
  );
}

export function CopyButton({
  text,
  getText,
  className,
  label,
  clearAfterMs,
}: {
  text?: string;
  getText?: () => Promise<string>;
  className?: string;
  label?: string;
  clearAfterMs?: number;
}) {
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  async function copy(ev: React.MouseEvent) {
    ev.stopPropagation();
    ev.preventDefault();
    try {
      setBusy(true);
      const value = getText ? await getText() : (text ?? "");
      await writeClipboard(value);
      setDone(true);
      setTimeout(() => setDone(false), 1500);
      if (clearAfterMs) {
        setTimeout(() => {
          void writeClipboard("");
        }, clearAfterMs);
      }
    } catch {
      /* ignore */
    } finally {
      setBusy(false);
    }
  }
  if (label) {
    return (
      <Button
        size="xs"
        variant="ghost"
        className={className}
        onClick={copy}
        loading={busy}
        icon={done ? <Check className="size-3 text-ok" /> : <Copy className="size-3" />}
      >
        {done ? "Copied" : label}
      </Button>
    );
  }
  return (
    <IconButton title={done ? "Copied" : "Copy"} onClick={copy} className={cx("size-6", className)} disabled={busy}>
      {done ? <Check className="size-3.5 text-ok" /> : <Copy className="size-3.5" />}
    </IconButton>
  );
}

export async function writeClipboard(value: string) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const ta = document.createElement("textarea");
  ta.value = value;
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  document.execCommand("copy");
  ta.remove();
}

export function CodeBlock({
  children,
  className,
  maxHeight,
  copy = true,
  tone,
}: {
  children: string;
  className?: string;
  maxHeight?: number;
  copy?: boolean;
  tone?: "err";
}) {
  return (
    <div className={cx("group relative", className)}>
      <pre
        className={cx(
          "rounded-md border bg-panel-2 px-3 py-2 font-mono text-[11.5px] leading-relaxed overflow-auto whitespace-pre-wrap break-all",
          tone === "err" ? "border-err/30 text-err" : "border-line",
        )}
        style={maxHeight ? { maxHeight } : undefined}
      >
        {children || <span className="text-faint">(empty)</span>}
      </pre>
      {copy && children && (
        <div className="absolute top-1 right-1 opacity-0 group-hover:opacity-100 transition-opacity">
          <CopyButton text={children} />
        </div>
      )}
    </div>
  );
}

export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
  className,
}: {
  tabs: { id: T; label: ReactNode; count?: number }[];
  value: T;
  onChange: (v: T) => void;
  className?: string;
}) {
  return (
    <div className={cx("flex items-center gap-0.5 border-b border-line overflow-x-auto", className)}>
      {tabs.map((t) => (
        <button
          key={t.id}
          onClick={() => onChange(t.id)}
          className={cx(
            "relative h-9 px-3 text-[13px] whitespace-nowrap transition-colors flex items-center gap-1.5",
            value === t.id ? "text-fg" : "text-muted hover:text-fg",
          )}
        >
          {t.label}
          {t.count !== undefined && <span className="text-[11px] text-faint tabular-nums">{t.count}</span>}
          {value === t.id && <span className="absolute inset-x-2 -bottom-px h-0.5 rounded bg-accent" />}
        </button>
      ))}
    </div>
  );
}

export function Segmented<T extends string>({
  options,
  value,
  onChange,
  size = "sm",
}: {
  options: { id: T; label: ReactNode; title?: string }[];
  value: T;
  onChange: (v: T) => void;
  size?: "sm" | "xs";
}) {
  return (
    <div className="inline-flex rounded-md border border-line bg-panel-2 p-0.5">
      {options.map((o) => (
        <button
          key={o.id}
          title={o.title}
          type="button"
          onClick={() => onChange(o.id)}
          className={cx(
            "rounded px-2 font-medium transition-colors flex items-center gap-1",
            size === "sm" ? "h-6 text-xs" : "h-5 text-[11px]",
            value === o.id ? "bg-panel-3 text-fg shadow-sm" : "text-muted hover:text-fg",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function PageHeader({
  title,
  subtitle,
  actions,
  icon,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-4 px-5 pt-4 pb-3">
      <div className="min-w-0">
        <h1 className="text-[17px] font-semibold flex items-center gap-2 [&_svg]:size-4.5 [&_svg]:text-muted">
          {icon}
          {title}
        </h1>
        {subtitle && <div className="text-muted mt-0.5 truncate">{subtitle}</div>}
      </div>
      {actions && <div className="flex items-center gap-2 shrink-0">{actions}</div>}
    </div>
  );
}

export function Table({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cx("overflow-auto", className)}>
      <table className="w-full border-collapse text-[12.5px]">{children}</table>
    </div>
  );
}
export function Th({ children, className }: { children?: ReactNode; className?: string }) {
  return (
    <th
      className={cx(
        "sticky top-0 z-[1] bg-panel text-left font-medium text-[11px] uppercase tracking-wide text-faint px-3 h-8 border-b border-line whitespace-nowrap",
        className,
      )}
    >
      {children}
    </th>
  );
}
export function Td({ children, className, mono, title, colSpan }: { children?: ReactNode; className?: string; mono?: boolean; title?: string; colSpan?: number }) {
  return (
    <td title={title} colSpan={colSpan} className={cx("px-3 h-9 border-b border-line/70 align-middle", mono && "font-mono text-[11.5px]", className)}>
      {children}
    </td>
  );
}
export function Tr({
  children,
  onClick,
  selected,
  className,
}: {
  children: ReactNode;
  onClick?: () => void;
  selected?: boolean;
  className?: string;
}) {
  return (
    <tr
      onClick={onClick}
      className={cx(
        "group transition-colors",
        onClick && "cursor-pointer",
        selected ? "bg-accent/8" : "hover:bg-panel-2",
        className,
      )}
    >
      {children}
    </tr>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="inline-flex h-[18px] min-w-[18px] items-center justify-center rounded border border-line-strong bg-panel-2 px-1 font-mono text-[10px] text-muted">
      {children}
    </kbd>
  );
}

export function SearchInput({
  value,
  onChange,
  placeholder = "Search…",
  className,
  autoFocus,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  className?: string;
  autoFocus?: boolean;
}) {
  return (
    <div className={cx("relative", className)}>
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoFocus={autoFocus}
        className="pr-7"
      />
      {value && (
        <button className="absolute right-1.5 top-1.5 text-faint hover:text-fg" onClick={() => onChange("")} aria-label="Clear">
          <X className="size-4" />
        </button>
      )}
    </div>
  );
}

export function ProgressBar({ value, max, className }: { value: number; max: number; className?: string }) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0;
  const color = pct > 90 ? "bg-err" : pct > 75 ? "bg-warn" : "bg-accent";
  return (
    <div className={cx("h-1.5 w-full rounded-full bg-panel-3 overflow-hidden", className)}>
      <div className={cx("h-full rounded-full transition-all", color)} style={{ width: `${pct}%` }} />
    </div>
  );
}

export function useAutoFocus<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  useEffect(() => {
    ref.current?.focus();
  }, []);
  return ref;
}

export function ChipInput({
  value,
  onChange,
  placeholder,
  mono,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
  mono?: boolean;
}) {
  const [text, setText] = useState("");
  function commit() {
    const parts = text
      .split(/[,\s]+/)
      .map((s) => s.trim())
      .filter(Boolean)
      .filter((s) => !value.includes(s));
    if (parts.length) onChange([...value, ...parts]);
    setText("");
  }
  return (
    <div className="flex flex-wrap items-center gap-1 min-h-8 rounded-md border border-line bg-panel-2 px-1.5 py-1 focus-within:border-accent focus-within:ring-2 focus-within:ring-accent/20">
      {value.map((v) => (
        <span key={v} className={cx("inline-flex items-center gap-1 rounded bg-panel-3 px-1.5 h-5 text-[11.5px]", mono && "font-mono")}>
          {v}
          <button type="button" className="text-faint hover:text-fg" onClick={() => onChange(value.filter((x) => x !== v))}>
            <X className="size-3" />
          </button>
        </span>
      ))}
      <input
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === ",") {
            e.preventDefault();
            commit();
          } else if (e.key === "Backspace" && !text && value.length) {
            onChange(value.slice(0, -1));
          }
        }}
        onBlur={commit}
        placeholder={value.length ? "" : placeholder}
        className={cx("flex-1 min-w-24 bg-transparent outline-none text-[13px] placeholder:text-faint", mono && "font-mono text-xs")}
      />
    </div>
  );
}
