import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { AlertTriangle, CheckCircle2, Info, X, XCircle } from "lucide-react";
import { Button, IconButton, Input, cx } from "./ui";

export function useEscape(onClose: () => void, active = true) {
  useEffect(() => {
    if (!active) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [onClose, active]);
}

export function Modal({
  open,
  onClose,
  title,
  children,
  footer,
  width = 520,
  icon,
}: {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  width?: number;
  icon?: ReactNode;
}) {
  useEscape(onClose, open);
  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/55 p-4 pt-[8vh] backdrop-blur-[2px]" onMouseDown={onClose}>
      <div
        className="w-full rounded-xl border border-line-strong bg-panel shadow-2xl shadow-black/40 flex flex-col max-h-[84vh]"
        style={{ maxWidth: width }}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header className="flex items-center justify-between gap-2 px-4 h-12 border-b border-line shrink-0">
          <h2 className="font-semibold text-[14px] flex items-center gap-2 [&_svg]:size-4 [&_svg]:text-muted">
            {icon}
            {title}
          </h2>
          <IconButton title="Close" onClick={onClose}>
            <X className="size-4" />
          </IconButton>
        </header>
        <div className="p-4 overflow-y-auto">{children}</div>
        {footer && <footer className="flex items-center justify-end gap-2 px-4 py-3 border-t border-line shrink-0">{footer}</footer>}
      </div>
    </div>,
    document.body,
  );
}

export function Drawer({
  open,
  onClose,
  title,
  children,
  footer,
  width = 520,
  subtitle,
}: {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  subtitle?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  width?: number;
}) {
  useEscape(onClose, open);
  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-40 flex justify-end bg-black/35" onMouseDown={onClose}>
      <aside
        className="h-full w-full border-l border-line-strong bg-panel shadow-2xl flex flex-col"
        style={{ maxWidth: width }}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header className="flex items-center justify-between gap-2 px-4 h-12 border-b border-line shrink-0">
          <div className="min-w-0">
            <h2 className="font-semibold text-[14px] truncate">{title}</h2>
            {subtitle && <div className="text-[11.5px] text-muted truncate">{subtitle}</div>}
          </div>
          <IconButton title="Close" onClick={onClose}>
            <X className="size-4" />
          </IconButton>
        </header>
        <div className="flex-1 overflow-y-auto p-4">{children}</div>
        {footer && <footer className="flex items-center justify-end gap-2 px-4 py-3 border-t border-line shrink-0">{footer}</footer>}
      </aside>
    </div>,
    document.body,
  );
}

// ---------------- Toasts ----------------

type ToastKind = "success" | "error" | "info" | "warning";
type ToastItem = { id: number; kind: ToastKind; title: string; detail?: string };
type ToastApi = {
  push: (kind: ToastKind, title: string, detail?: string) => void;
  success: (title: string, detail?: string) => void;
  error: (title: string, detail?: string) => void;
  info: (title: string, detail?: string) => void;
  warning: (title: string, detail?: string) => void;
};
const ToastCtx = createContext<ToastApi | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const idRef = useRef(1);
  const remove = useCallback((id: number) => setItems((l) => l.filter((t) => t.id !== id)), []);
  const push = useCallback(
    (kind: ToastKind, title: string, detail?: string) => {
      const id = idRef.current++;
      setItems((l) => [...l.slice(-4), { id, kind, title, detail }]);
      setTimeout(() => remove(id), kind === "error" ? 8000 : 4000);
    },
    [remove],
  );
  const [apiObj] = useState<ToastApi>(() => ({
    push: (...a) => pushRef.current(...a),
    success: (t, d) => pushRef.current("success", t, d),
    error: (t, d) => pushRef.current("error", t, d),
    info: (t, d) => pushRef.current("info", t, d),
    warning: (t, d) => pushRef.current("warning", t, d),
  }));
  const pushRef = useRef(push);
  pushRef.current = push;
  const icons: Record<ToastKind, ReactNode> = {
    success: <CheckCircle2 className="size-4 text-ok" />,
    error: <XCircle className="size-4 text-err" />,
    info: <Info className="size-4 text-info" />,
    warning: <AlertTriangle className="size-4 text-warn" />,
  };
  return (
    <ToastCtx.Provider value={apiObj}>
      {children}
      {createPortal(
        <div className="fixed bottom-4 right-4 z-[70] flex w-[360px] flex-col gap-2">
          {items.map((t) => (
            <div
              key={t.id}
              className="flex items-start gap-2.5 rounded-lg border border-line-strong bg-panel px-3 py-2.5 shadow-xl shadow-black/30"
            >
              <div className="mt-0.5">{icons[t.kind]}</div>
              <div className="min-w-0 flex-1">
                <div className="text-[13px] font-medium">{t.title}</div>
                {t.detail && <div className="text-xs text-muted mt-0.5 break-words whitespace-pre-wrap">{t.detail}</div>}
              </div>
              <button className="text-faint hover:text-fg" onClick={() => remove(t.id)}>
                <X className="size-3.5" />
              </button>
            </div>
          ))}
        </div>,
        document.body,
      )}
    </ToastCtx.Provider>
  );
}

export function useToast(): ToastApi {
  const c = useContext(ToastCtx);
  if (!c) throw new Error("ToastProvider missing");
  return c;
}

// ---------------- Confirm ----------------

type ConfirmOpts = {
  title: string;
  message?: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  typeToConfirm?: string;
};
type ConfirmFn = (o: ConfirmOpts) => Promise<boolean>;
const ConfirmCtx = createContext<ConfirmFn | null>(null);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<(ConfirmOpts & { resolve: (v: boolean) => void }) | null>(null);
  const [typed, setTyped] = useState("");
  const confirm = useCallback<ConfirmFn>(
    (o) =>
      new Promise<boolean>((resolve) => {
        setTyped("");
        setState({ ...o, resolve });
      }),
    [],
  );
  const close = (v: boolean) => {
    state?.resolve(v);
    setState(null);
  };
  const blocked = !!state?.typeToConfirm && typed !== state.typeToConfirm;
  return (
    <ConfirmCtx.Provider value={confirm}>
      {children}
      <Modal
        open={!!state}
        onClose={() => close(false)}
        title={state?.title ?? ""}
        width={440}
        icon={state?.danger ? <AlertTriangle className="!text-err" /> : undefined}
        footer={
          <>
            <Button variant="ghost" onClick={() => close(false)}>
              Cancel
            </Button>
            <Button variant={state?.danger ? "danger" : "primary"} disabled={blocked} onClick={() => close(true)} autoFocus>
              {state?.confirmLabel ?? "Confirm"}
            </Button>
          </>
        }
      >
        {state?.message && <div className="text-[13px] text-muted">{state.message}</div>}
        {state?.typeToConfirm && (
          <div className="mt-3">
            <div className="text-xs text-muted mb-1">
              Type <span className="font-mono text-fg">{state.typeToConfirm}</span> to confirm
            </div>
            <Input
              mono
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !blocked) close(true);
              }}
            />
          </div>
        )}
      </Modal>
    </ConfirmCtx.Provider>
  );
}

export function useConfirm(): ConfirmFn {
  const c = useContext(ConfirmCtx);
  if (!c) throw new Error("ConfirmProvider missing");
  return c;
}

export function Popover({
  open,
  onClose,
  children,
  className,
}: {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const h = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    const t = setTimeout(() => document.addEventListener("mousedown", h), 0);
    return () => {
      clearTimeout(t);
      document.removeEventListener("mousedown", h);
    };
  }, [open, onClose]);
  useEscape(onClose, open);
  if (!open) return null;
  return (
    <div
      ref={ref}
      className={cx("absolute z-30 rounded-lg border border-line-strong bg-panel shadow-xl shadow-black/30 py-1", className)}
    >
      {children}
    </div>
  );
}

export function MenuItem({
  icon,
  children,
  onClick,
  danger,
  hint,
}: {
  icon?: ReactNode;
  children: ReactNode;
  onClick: () => void;
  danger?: boolean;
  hint?: ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      className={cx(
        "flex w-full items-center gap-2 px-3 h-8 text-[13px] text-left hover:bg-panel-3 [&_svg]:size-3.5",
        danger ? "text-err" : "text-fg [&_svg]:text-muted",
      )}
    >
      {icon}
      <span className="flex-1 truncate">{children}</span>
      {hint && <span className="text-[11px] text-faint">{hint}</span>}
    </button>
  );
}
