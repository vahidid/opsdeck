import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { AlertTriangle, KeyRound, Lock, ServerCrash, ShieldCheck, Terminal } from "lucide-react";
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
    <div className="min-h-full flex items-center justify-center p-6 bg-bg">
      <div className="w-full max-w-[400px]">
        <div className="flex items-center gap-2.5 mb-8 justify-center">
          <Logo />
          <span className="text-[20px] font-semibold tracking-tight">opsdeck</span>
        </div>
        {phase === "loading" && (
          <div className="flex justify-center py-10">
            <Spinner className="size-6" />
          </div>
        )}
        {phase === "noauth" && (
          <Panel icon={<Terminal />} title="Open opsdeck from your terminal">
            <p className="text-muted">
              This UI is protected by a one-time token. Run <code className="font-mono text-fg">opsdeck serve</code> and open the
              link it prints (it contains <code className="font-mono text-fg">#token=…</code>).
            </p>
            <Button className="mt-4 w-full" onClick={() => void evaluate()}>
              I opened the link — retry
            </Button>
          </Panel>
        )}
        {phase === "offline" && (
          <Panel icon={<ServerCrash />} title="Server unreachable">
            <p className="text-muted">{offlineMsg}</p>
            <Button className="mt-4 w-full" onClick={() => void evaluate()}>
              Retry
            </Button>
          </Panel>
        )}
        {phase === "setup" && <Setup onDone={() => setPhase("ready")} />}
        {phase === "locked" && <Unlock onDone={() => setPhase("ready")} />}
      </div>
    </div>
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

function Panel({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <div className="rounded-xl border border-line bg-panel p-6 shadow-xl shadow-black/20">
      <div className="flex items-center gap-2.5 mb-3 [&_svg]:size-5 [&_svg]:text-accent">
        {icon}
        <h1 className="text-[15px] font-semibold">{title}</h1>
      </div>
      {children}
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
    <Panel icon={<Lock />} title="Vault locked">
      <p className="text-muted mb-4">Enter your master password to unlock your workspaces and credentials.</p>
      <form onSubmit={submit} className="space-y-3">
        <Input type="password" autoFocus placeholder="Master password" value={pw} onChange={(e) => setPw(e.target.value)} />
        {err && <div className="text-err text-xs">{err}</div>}
        <Button variant="primary" className="w-full" loading={busy} disabled={!pw} type="submit">
          Unlock
        </Button>
      </form>
    </Panel>
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
    <Panel icon={<ShieldCheck />} title="Create your vault">
      <p className="text-muted mb-4">
        Your SSH keys, passwords, tokens and VPN configs are encrypted locally with a key derived from this master password
        (Argon2id + XChaCha20-Poly1305).
      </p>
      <form onSubmit={submit} className="space-y-3">
        <div>
          <Input type="password" autoFocus placeholder="Master password (min. 10 characters)" value={pw} onChange={(e) => setPw(e.target.value)} />
          <div className="mt-1.5 flex items-center gap-2">
            <div className="flex-1 grid grid-cols-4 gap-1">
              {[0, 1, 2, 3].map((i) => (
                <div key={i} className={cx("h-1 rounded-full", pw && i < Math.max(1, strength.score) ? barColor : "bg-panel-3")} />
              ))}
            </div>
            <span className="text-[11px] text-muted w-16 text-right">{strength.label}</span>
          </div>
        </div>
        <Input type="password" placeholder="Confirm password" value={pw2} onChange={(e) => setPw2(e.target.value)} />
        {pw2 && pw !== pw2 && <div className="text-err text-xs">Passwords do not match</div>}
        <div className="flex gap-2.5 rounded-md border border-warn/30 bg-warn/8 p-2.5 text-xs">
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
        {err && <div className="text-err text-xs">{err}</div>}
        <Button variant="primary" className="w-full" loading={busy} disabled={!ok} type="submit" icon={<KeyRound className="size-4" />}>
          Create vault
        </Button>
      </form>
    </Panel>
  );
}
