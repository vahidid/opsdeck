import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { AlertCircle, AlertTriangle, Eye, EyeOff, KeyRound, Lock, ServerCrash, ShieldCheck, Terminal } from "lucide-react";
import { ApiError, api, errMsg, onGlobalSignal } from "../api/client";
import { Button, Input, Spinner, cx } from "../components/ui";
import { passwordStrength } from "../lib/format";
import { useInterval } from "../hooks";

type Phase = "loading" | "noauth" | "offline" | "setup" | "locked" | "ready";

export function Boot({ children }: { children: (lock: () => void) => ReactNode }) {
  const [phase, setPhase] = useState<Phase>("loading");
  const [offlineMsg, setOfflineMsg] = useState("");

  const evaluate = useCallback(async () => {
    try {
      const hash = new URLSearchParams(location.hash.replace(/^#/, ""));
      const token = hash.get("token");
      if (token) {
        try {
          await api.login(token);
        } finally {
          history.replaceState(null, "", location.pathname + location.search);
        }
      }
      await api.authCheck();
      const st = await api.vaultStatus();
      if (!st.initialized) setPhase("setup");
      else if (!st.unlocked) setPhase("locked");
      else setPhase("ready");
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) setPhase("noauth");
      else if (e instanceof ApiError && e.status === 423) setPhase(e.code === "vault_uninitialized" ? "setup" : "locked");
      else {
        setOfflineMsg(errMsg(e));
        setPhase("offline");
      }
    }
  }, []);

  useEffect(() => {
    void evaluate();
  }, [evaluate]);

  useEffect(
    () =>
      onGlobalSignal((s) => {
        if (s === "unauthorized") setPhase("noauth");
        else if (s === "locked") setPhase((p) => (p === "ready" ? "locked" : p));
        else if (s === "uninitialized") setPhase("setup");
      }),
    [],
  );

  // detect server-side auto-lock proactively
  useInterval(
    () => {
      api
        .vaultStatus()
        .then((st) => {
          if (!st.unlocked) setPhase("locked");
        })
        .catch(() => {});
    },
    phase === "ready" ? 30000 : null,
  );

  const lock = useCallback(() => setPhase("locked"), []);

  if (phase === "ready") return <>{children(lock)}</>;

  return (
    <GateShell>
      {phase === "loading" && (
        <div className="flex justify-center py-10">
          <Spinner className="size-6" />
        </div>
      )}
      {phase === "noauth" && (
        <GateCard icon={<Terminal />} eyebrow="Sign-in required" title="Open opsdeck from your terminal">
          <p className="text-muted text-center">
            This UI is protected by a one-time token. Run <code className="font-mono text-fg">opsdeck serve</code> and open the
            link it prints (it contains <code className="font-mono text-fg">#token=…</code>).
          </p>
          <Button className="mt-5 w-full h-10" onClick={() => void evaluate()}>
            I opened the link — retry
          </Button>
        </GateCard>
      )}
      {phase === "offline" && (
        <GateCard icon={<ServerCrash />} eyebrow="Connection" title="Server unreachable">
          <p className="text-muted break-words text-center">{offlineMsg}</p>
          <Button className="mt-5 w-full h-10" onClick={() => void evaluate()}>
            Retry
          </Button>
        </GateCard>
      )}
      {phase === "setup" && <Setup onDone={() => setPhase("ready")} />}
      {phase === "locked" && <Unlock onDone={() => setPhase("ready")} />}
    </GateShell>
  );
}

export function Logo({ size = 28 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden>
      <rect width="32" height="32" rx="8" fill="var(--c-accent)" />
      <path d="M8.5 11l6 5-6 5M16.5 22h7" stroke="var(--c-accent-fg)" strokeWidth="2.8" fill="none" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

/** The gate frame: a quiet backdrop, the brand, and the state's card. */
function GateShell({ children }: { children: ReactNode }) {
  return (
    <div className="relative min-h-full flex items-center justify-center p-6 overflow-hidden bg-bg">
      {/* a faint pool of accent light behind the card; the only decoration */}
      <div
        aria-hidden
        className="absolute inset-0 pointer-events-none"
        style={{ background: "radial-gradient(560px 380px at 50% 44%, color-mix(in srgb, var(--c-accent) 6%, transparent), transparent 70%)" }}
      />
      <div className="relative w-full max-w-[400px]">
        <div className="flex items-center gap-2.5 mb-7 justify-center">
          <Logo />
          <span className="text-[20px] font-semibold tracking-tight">opsdeck</span>
        </div>
        {children}
      </div>
    </div>
  );
}

function GateCard({ icon, eyebrow, title, description, children }: { icon: ReactNode; eyebrow: string; title: string; description?: string; children: ReactNode }) {
  return (
    <div className="rounded-xl border border-line bg-panel/90 backdrop-blur-sm shadow-xl shadow-black/20">
      <div className="p-6">
        <div className="flex flex-col items-center text-center">
          <div className="size-11 rounded-full bg-panel-3 ring-1 ring-accent/25 flex items-center justify-center [&_svg]:size-5 [&_svg]:text-accent mb-4">
            {icon}
          </div>
          <div className="text-[10.5px] uppercase tracking-[0.14em] text-faint">{eyebrow}</div>
          <h1 className="text-[16px] font-semibold mt-1">{title}</h1>
          {description && <p className="text-[12.5px] text-muted leading-relaxed mt-1.5">{description}</p>}
        </div>
        <div className={cx(description && "mt-5")}>{children}</div>
      </div>
    </div>
  );
}

function ErrBox({ id, children }: { id: string; children: ReactNode }) {
  return (
    <div id={id} role="alert" className="flex items-start gap-2 rounded-md border border-err/25 bg-err/10 px-2.5 py-2 text-xs text-err">
      <AlertCircle className="size-3.5 shrink-0 mt-px" />
      <span>{children}</span>
    </div>
  );
}

function PasswordField({
  value,
  onChange,
  placeholder,
  autoComplete,
  invalid,
  describedBy,
  autoFocus,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
  autoComplete: "current-password" | "new-password";
  invalid?: boolean;
  describedBy?: string;
  autoFocus?: boolean;
}) {
  const [show, setShow] = useState(false);
  return (
    <div className="relative">
      <Input
        type={show ? "text" : "password"}
        autoFocus={autoFocus}
        placeholder={placeholder}
        autoComplete={autoComplete}
        aria-label={placeholder}
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={cx("h-10 pr-10 text-[14px]", invalid && "border-err focus:border-err focus:ring-err/20")}
      />
      <button
        type="button"
        onClick={() => setShow(!show)}
        aria-label={show ? "Hide password" : "Show password"}
        className="absolute right-1.5 top-1/2 -translate-y-1/2 size-7 flex items-center justify-center rounded text-faint hover:text-fg hover:bg-panel-2 transition-colors"
      >
        {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  );
}

function Unlock({ onDone }: { onDone: () => void }) {
  const [pw, setPw] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      const st = await api.vaultUnlock(pw);
      if (st.unlocked) onDone();
      else setErr("Vault is still locked");
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
      setPw("");
    }
  }
  return (
    <GateCard icon={<Lock />} eyebrow="Vault locked" title="Enter your master password" description="This unlocks your workspaces and credentials.">
      <form onSubmit={submit} className="space-y-3">
        <PasswordField autoFocus value={pw} onChange={setPw} placeholder="Master password" autoComplete="current-password" invalid={!!err} describedBy={err ? "unlock-error" : undefined} />
        {err && <ErrBox id="unlock-error">{err}</ErrBox>}
        <Button variant="primary" className="w-full h-10" loading={busy} disabled={!pw} type="submit">
          Unlock
        </Button>
      </form>
    </GateCard>
  );
}

function Setup({ onDone }: { onDone: () => void }) {
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [ack, setAck] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const strength = passwordStrength(pw);
  const ok = pw.length >= 10 && pw === pw2 && ack;
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!ok) return;
    setBusy(true);
    setErr("");
    try {
      await api.vaultInit(pw);
      const st = await api.vaultStatus();
      if (!st.unlocked) await api.vaultUnlock(pw);
      onDone();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  const barColor = ["bg-err", "bg-err", "bg-warn", "bg-ok", "bg-ok"][strength.score];
  return (
    <GateCard
      icon={<ShieldCheck />}
      eyebrow="Vault setup"
      title="Create your vault"
      description="Your SSH keys, passwords, tokens and VPN configs are encrypted locally with a key derived from this master password (Argon2id + XChaCha20-Poly1305)."
    >
      <form onSubmit={submit} className="space-y-3">
        <div>
          <PasswordField autoFocus value={pw} onChange={setPw} placeholder="Master password (min. 10 characters)" autoComplete="new-password" />
          <div className="mt-2 flex items-center gap-2">
            <div className="flex-1 grid grid-cols-4 gap-1">
              {[0, 1, 2, 3].map((i) => (
                <div key={i} className={cx("h-1.5 rounded-full transition-colors", pw && i < Math.max(1, strength.score) ? barColor : "bg-panel-3")} />
              ))}
            </div>
            <span className="text-[11px] text-muted w-16 text-right">{strength.label}</span>
          </div>
        </div>
        <PasswordField value={pw2} onChange={setPw2} placeholder="Confirm password" autoComplete="new-password" invalid={!!pw2 && pw !== pw2} describedBy={pw2 && pw !== pw2 ? "setup-match" : undefined} />
        {pw2 && pw !== pw2 && (
          <div id="setup-match" className="text-err text-xs">
            Passwords do not match
          </div>
        )}
        <div className="flex gap-2.5 rounded-lg border border-warn/30 bg-warn/8 p-3 text-xs">
          <AlertTriangle className="size-4 text-warn shrink-0 mt-0.5" />
          <div>
            <div className="text-fg font-medium">There is no recovery.</div>
            <div className="text-muted">If you forget this password, the vault cannot be decrypted by anyone — including us.</div>
            <label className="mt-2 flex items-center gap-2 text-fg cursor-pointer">
              <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} className="accent-[var(--c-accent)]" />I
              understand
            </label>
          </div>
        </div>
        {err && <ErrBox id="setup-error">{err}</ErrBox>}
        <Button variant="primary" className="w-full h-10" loading={busy} disabled={!ok} type="submit" icon={<KeyRound className="size-4" />}>
          Create vault
        </Button>
      </form>
    </GateCard>
  );
}
