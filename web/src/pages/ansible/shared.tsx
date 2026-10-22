import { createContext, useCallback, useContext, useRef, useState } from "react";
import type { ReactNode } from "react";
import { BookOpen, Braces, FileCode2, FileCog, FileLock2, FileText, KeyRound, ListTree, Package, Play, Settings2 } from "lucide-react";
import { ApiError } from "../../api/client";
import type { AnsibleFile, AnsibleFileType, AnsibleProject, AnsibleRunStatus } from "../../api/types";
import { Modal } from "../../components/overlay";
import { Badge, Button, Input, Toggle, cx } from "../../components/ui";
import type { Tone } from "../../components/ui";

// ---------------------------------------------------------------------------
// Page context

export type AnsibleCtx = {
  ws: string;
  project: AnsibleProject;
  reload: () => Promise<void>;
  /** go to a tab, with extra URL parameters (path, playbook, run…) */
  goto: (tab: string, extra?: Record<string, string>) => void;
  /** runs an action that may need a vault password, asking for it */
  withPassword: <T>(fn: (pw?: { password: string; vaultId: string }) => Promise<T>) => Promise<T | undefined>;
};

const Ctx = createContext<AnsibleCtx | null>(null);
export const AnsibleProvider = Ctx.Provider;
export function useAnsible(): AnsibleCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("AnsibleProvider missing");
  return c;
}

// ---------------------------------------------------------------------------
// Vault password prompt

type Ask = { vaultId: string; project: string; error?: string; n: number };
export type PasswordAnswer = { password: string; keep: boolean; save: boolean; agentUse: boolean };

/**
 * usePasswordPrompt returns withPassword (see AnsibleCtx) and the dialog to
 * render. The dialog asks for the vault id the server named; the password
 * is used once, or kept in memory until opsdeck locks, or also saved in
 * the vault.
 */
export function usePasswordPrompt(
  project: string,
  unlock: (vaultId: string, a: PasswordAnswer) => Promise<void>,
): { withPassword: AnsibleCtx["withPassword"]; dialog: ReactNode } {
  const [ask, setAsk] = useState<Ask | null>(null);
  const answer = useRef<((a: PasswordAnswer | null) => void) | null>(null);
  const attempt = useRef(0);

  const prompt = useCallback((a: Omit<Ask, "n">) => {
    setAsk({ ...a, n: ++attempt.current });
    return new Promise<PasswordAnswer | null>((resolve) => {
      answer.current = resolve;
    });
  }, []);

  const withPassword = useCallback(
    async <T,>(fn: (pw?: { password: string; vaultId: string }) => Promise<T>): Promise<T | undefined> => {
      let pw: { password: string; vaultId: string } | undefined;
      let error = "";
      for (let i = 0; i < 10; i++) {
        try {
          const out = await fn(pw);
          setAsk(null);
          return out;
        } catch (e) {
          if (!(e instanceof ApiError)) throw e;
          let vaultId = pw?.vaultId ?? "default";
          if (e.code === "ansible_password") {
            vaultId = String(e.data.vaultId ?? "default");
            error = /does not open/.test(e.message) ? "The password at hand does not open it." : "";
          } else if (e.code === "ansible_wrong_password") {
            error = "Wrong password.";
          } else {
            setAsk(null);
            throw e;
          }
          for (;;) {
            const a = await prompt({ vaultId, project, error });
            if (!a) {
              setAsk(null);
              return undefined;
            }
            if (a.keep || a.save) {
              try {
                await unlock(vaultId, a);
                pw = undefined;
              } catch (ue) {
                if (ue instanceof ApiError && (ue.code === "ansible_wrong_password" || ue.status === 422)) {
                  error = "Wrong password.";
                  continue;
                }
                setAsk(null);
                throw ue;
              }
            } else {
              pw = { password: a.password, vaultId };
            }
            break;
          }
        }
      }
      setAsk(null);
      return undefined;
    },
    [prompt, project, unlock],
  );

  const dialog = ask ? (
    <PasswordDialog
      key={ask.n}
      ask={ask}
      onAnswer={(a) => {
        const r = answer.current;
        answer.current = null;
        if (!a) setAsk(null);
        r?.(a);
      }}
    />
  ) : null;
  return { withPassword, dialog };
}

function PasswordDialog({ ask, onAnswer }: { ask: Ask; onAnswer: (a: PasswordAnswer | null) => void }) {
  const [password, setPassword] = useState("");
  const [keep, setKeep] = useState(true);
  const [save, setSave] = useState(false);
  const [agentUse, setAgentUse] = useState(true);
  const [busy, setBusy] = useState(false);
  const submit = () => {
    if (!password) return;
    setBusy(true);
    onAnswer({ password, keep, save, agentUse });
  };
  return (
    <Modal
      open
      onClose={() => onAnswer(null)}
      title="Ansible Vault password"
      icon={<KeyRound />}
      width={460}
      footer={
        <>
          <Button variant="ghost" onClick={() => onAnswer(null)}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!password} onClick={submit}>
            Unlock
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">
          <span className="font-medium text-fg">{ask.project}</span> needs the password of vault id <Badge mono>{ask.vaultId}</Badge>. It never leaves this computer and
          is not written to disk unless you save it in the vault.
        </p>
        <Input
          type="password"
          autoFocus
          autoComplete="off"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && submit()}
          placeholder="Vault password"
        />
        {ask.error && <div className="text-xs text-err">{ask.error}</div>}
        <div className="space-y-2 pt-1">
          <Toggle checked={keep || save} disabled={save} onChange={setKeep} label="Keep it in memory until opsdeck locks" />
          <Toggle checked={save} onChange={setSave} label="Save it in the vault of this workspace" />
          {save && (
            <div className="pl-9">
              <Toggle checked={agentUse} onChange={setAgentUse} label={<span className="text-muted">The agent may use it for runs (it never sees it)</span>} />
            </div>
          )}
        </div>
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Files

export const TYPE_LABEL: Record<AnsibleFileType, string> = {
  playbook: "Playbook",
  inventory: "Inventory",
  vars: "Variables",
  role: "Role",
  template: "Template",
  config: "Config",
  requirements: "Requirements",
  file: "File",
};

export function FileIcon({ f, className = "size-3.5" }: { f: Pick<AnsibleFile, "type" | "encrypted" | "path">; className?: string }) {
  if (f.encrypted) return <FileLock2 className={cx(className, "text-warn")} />;
  switch (f.type) {
    case "playbook":
      return <Play className={cx(className, "text-accent")} />;
    case "inventory":
      return <ListTree className={cx(className, "text-info")} />;
    case "vars":
      return <Braces className={cx(className, "text-violet-400")} />;
    case "role":
      return <BookOpen className={cx(className, "text-faint")} />;
    case "template":
      return <FileCode2 className={cx(className, "text-faint")} />;
    case "config":
      return <Settings2 className={cx(className, "text-faint")} />;
    case "requirements":
      return <Package className={cx(className, "text-faint")} />;
  }
  return /\.(cfg|ini|conf)$/i.test(f.path) ? <FileCog className={cx(className, "text-faint")} /> : <FileText className={cx(className, "text-faint")} />;
}

// ---------------------------------------------------------------------------
// Runs

export const STATUS_TONE: Record<AnsibleRunStatus, Tone> = {
  running: "info",
  ok: "ok",
  failed: "err",
  unreachable: "err",
  canceled: "neutral",
  error: "err",
  interrupted: "warn",
};

export function StatusBadge({ status }: { status: AnsibleRunStatus }) {
  return (
    <Badge tone={STATUS_TONE[status] ?? "neutral"}>
      {status === "running" && <span className="size-1.5 rounded-full bg-current animate-pulse" />}
      {status}
    </Badge>
  );
}

export function actorLabel(actor: string): string {
  return actor.startsWith("agent:") ? "agent" : actor;
}

export function Section({ title, actions, children, className }: { title: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("rounded-lg border border-line bg-panel", className)}>
      <header className="flex items-center gap-2 px-3 h-10 border-b border-line">
        <h3 className="text-[13px] font-medium flex-1 min-w-0 truncate">{title}</h3>
        {actions}
      </header>
      {children}
    </section>
  );
}
