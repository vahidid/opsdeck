import { useEffect, useState } from "react";
import { MessageSquareText, Smartphone } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { CodeWait, PendingCode } from "../api/types";
import { useInterval } from "../hooks";
import { Modal } from "./overlay";
import { Button, Input } from "./ui";

// One-time codes that a VPN gateway sends for each login (FortiGate by SMS
// or email, a second password or challenge on AnyConnect): typed in here, or
// filled in by themselves when they arrive from the phone (App settings →
// VPN). A gateway can also ask to pick one of its choices (the GROUP of a
// Cisco ASA), which is answered here too.

function useSecondsLeft(until: string): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  return Math.max(0, Math.round((Date.parse(until) - now) / 1000));
}

export function CodeEntry({ ws, name, wait, autoFocus, onSent }: { ws: string; name: string; wait: CodeWait; autoFocus?: boolean; onSent?: () => void }) {
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const left = useSecondsLeft(wait.expires);
  // another attempt starts empty
  useEffect(() => {
    setCode("");
    setErr("");
  }, [wait.since]);

  const [picked, setPicked] = useState("");

  async function answer(value: string) {
    setBusy(true);
    setErr("");
    try {
      await api.networkCode(ws, name, value);
      onSent?.();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  function send(e: React.FormEvent) {
    e.preventDefault();
    if (code.trim()) void answer(code);
  }

  const timeLeft = (
    <span className="text-faint tabular-nums" title="opsdeck waits this long; the gateway may let the code expire sooner">
      {Math.floor(left / 60)}:{String(left % 60).padStart(2, "0")} left
    </span>
  );

  if (wait.choices?.length) {
    const what = wait.label || "option";
    return (
      <div className="space-y-2 text-xs">
        <div dir="auto">
          {wait.message ? `${wait.message} ` : ""}Choose the {what} to log in to:
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {wait.choices.map((c) => (
            <Button
              key={c}
              size="sm"
              variant="secondary"
              loading={busy && picked === c}
              disabled={busy}
              onClick={() => {
                setPicked(c);
                void answer(c);
              }}
            >
              {c}
            </Button>
          ))}
          {timeLeft}
        </div>
        <div className="text-faint">Set it as the profile's group (Edit profile → Group / realm) to skip this question next time.</div>
        {err && <div className="text-err">{err}</div>}
      </div>
    );
  }

  return (
    <form onSubmit={send} className="space-y-2 text-xs">
      {wait.retry && <div className="text-err">The gateway did not accept the code. Check it and enter it again.</div>}
      <div dir="auto">{wait.message || "The gateway sent a one-time code (by SMS or email). Enter it to finish connecting."}</div>
      <div className="flex items-center gap-2">
        {wait.label && <span className="text-muted">{wait.label}</span>}
        <Input
          autoFocus={autoFocus}
          value={code}
          onChange={(e) => setCode(e.target.value)}
          inputMode="numeric"
          autoComplete="one-time-code"
          placeholder="code"
          dir="ltr"
          className="w-36 font-mono tracking-widest"
        />
        <Button type="submit" variant="primary" size="sm" loading={busy} disabled={!code.trim()}>
          Connect
        </Button>
        {timeLeft}
      </div>
      {!!wait.sources?.length && (
        <div className="flex items-center gap-1.5 text-faint">
          <Smartphone className="size-3.5 shrink-0" /> Filled in by itself when it arrives from {wait.sources.join(" or ")}.
        </div>
      )}
      {err && <div className="text-err">{err}</div>}
    </form>
  );
}

const promptKey = (p: PendingCode) => `${p.workspace}/${p.name}/${p.since}`;

// VPNCodePrompts asks for a code on any page as soon as a VPN waits for one,
// also when the agent or a connection through the VPN started it.
export function VPNCodePrompts() {
  const [pending, setPending] = useState<PendingCode[]>([]);
  const [closed, setClosed] = useState<string[]>([]);
  const load = () =>
    api
      .vpnCodes()
      .then(setPending)
      .catch(() => {});
  useEffect(() => {
    void load();
  }, []);
  useInterval(() => void load(), 2500);
  const cur = pending.find((p) => !closed.includes(promptKey(p)));
  useEffect(() => {
    if (!cur) return;
    const title = document.title;
    document.title = `${cur.choices?.length ? "Choice" : "Code"} for ${cur.name} · ${title}`;
    return () => {
      document.title = title;
    };
  }, [cur?.name]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!cur) return null;
  const k = promptKey(cur);
  return (
    <Modal
      open
      onClose={() => setClosed((c) => [...c, k])}
      title={cur.choices?.length ? `${cur.label || "Choice"} for ${cur.name}` : `One-time code for ${cur.name}`}
      icon={<MessageSquareText />}
      width={460}
    >
      <div className="text-[11px] text-faint mb-3">
        VPN <span className="font-mono">{cur.name}</span> in workspace <span className="font-mono">{cur.workspace}</span> is waiting for it.
      </div>
      <CodeEntry
        ws={cur.workspace}
        name={cur.name}
        wait={cur}
        autoFocus
        onSent={() => {
          setPending((l) => l.filter((p) => promptKey(p) !== k));
          setTimeout(() => void load(), 1500);
        }}
      />
      <div className="text-[11px] text-faint mt-3">
        Closing this keeps the VPN waiting: the {cur.choices?.length ? "choice" : "code"} can also be made on the VPN page.
      </div>
    </Modal>
  );
}
