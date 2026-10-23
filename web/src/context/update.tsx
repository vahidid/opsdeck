import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Download, ExternalLink, RefreshCw, Sparkles } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { UpdateStatus } from "../api/types";
import { Button, cx } from "../components/ui";
import { Modal } from "../components/overlay";
import { Markdown } from "../components/Markdown";

// Updates: the server checks GitHub releases (hourly at most); the UI asks
// it every few hours, shows a box in the sidebar when a new version is out,
// and installs it from the update dialog.

type UpdateApi = {
  status: UpdateStatus | null;
  checking: boolean;
  check: () => Promise<void>;
  install: () => Promise<void>;
  showDialog: () => void;
  dismissed: boolean;
  dismiss: () => void;
};

const Ctx = createContext<UpdateApi | null>(null);
const POLL = 6 * 3600_000;
const DISMISSED = "opsdeck.update.dismissed";

const installing = (s: UpdateStatus | null) => s?.state === "downloading" || s?.state === "installing" || s?.state === "restarting";

export function UpdateProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [checking, setChecking] = useState(false);
  const [dialog, setDialog] = useState(false);
  const [installErr, setInstallErr] = useState("");
  const [dismissedVer, setDismissedVer] = useState(() => {
    try {
      return localStorage.getItem(DISMISSED) ?? "";
    } catch {
      return "";
    }
  });
  const busy = installing(status);
  const statusRef = useRef(status);
  statusRef.current = status;

  const load = useCallback(async (refresh = false) => {
    try {
      setStatus(await api.updateStatus(refresh));
    } catch {
      // the app restarting for an update, or offline: keep the last status
    }
  }, []);

  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), POLL);
    return () => clearInterval(t);
  }, [load]);

  // follow an install closely
  useEffect(() => {
    if (!busy) return;
    const t = setInterval(() => void load(), 1000);
    return () => clearInterval(t);
  }, [busy, load]);

  const check = useCallback(async () => {
    setChecking(true);
    await load(true);
    setChecking(false);
  }, [load]);

  const install = useCallback(async () => {
    setInstallErr("");
    try {
      setStatus(await api.installUpdate());
    } catch (e) {
      setInstallErr(errMsg(e));
    }
  }, []);

  const dismiss = useCallback(() => {
    const v = statusRef.current?.latest?.version ?? "";
    setDismissedVer(v);
    try {
      localStorage.setItem(DISMISSED, v);
    } catch {
      /* ignore */
    }
  }, []);

  const value: UpdateApi = {
    status,
    checking,
    check,
    install,
    showDialog: () => setDialog(true),
    dismissed: !!status?.latest && dismissedVer === status.latest.version,
    dismiss,
  };

  return (
    <Ctx.Provider value={value}>
      {children}
      <UpdateDialog open={dialog || busy} onClose={() => setDialog(false)} status={status} error={installErr} onInstall={install} />
    </Ctx.Provider>
  );
}

export function useUpdate(): UpdateApi {
  const c = useContext(Ctx);
  if (!c) throw new Error("useUpdate outside UpdateProvider");
  return c;
}

const METHOD_HINT: Record<string, string> = {
  installer: "The installer runs, Windows asks for permission, and opsdeck starts again.",
  portable: "opsdeck replaces this program file and starts again.",
  app: "opsdeck replaces the app in its folder and starts again.",
  deb: "The package is installed with your password (system dialog), then opsdeck starts again.",
  rpm: "The package is installed with your password (system dialog), then opsdeck starts again.",
  binary: "opsdeck replaces its program file; restart it afterwards.",
};

function UpdateDialog({
  open,
  onClose,
  status,
  error,
  onInstall,
}: {
  open: boolean;
  onClose: () => void;
  status: UpdateStatus | null;
  error: string;
  onInstall: () => void;
}) {
  const rel = status?.latest;
  if (!status || !rel) return null;
  const st = status.state;
  const busy = installing(status);
  const openRelease = () => window.open(rel.url, "_blank", "noopener");
  return (
    <Modal
      open={open}
      onClose={busy ? () => {} : onClose}
      title={status.available ? `opsdeck ${rel.version} is available` : `opsdeck ${rel.version}`}
      icon={<Sparkles />}
      width={600}
      footer={
        <>
          <Button variant="ghost" icon={<ExternalLink className="size-3.5" />} onClick={openRelease}>
            Release page
          </Button>
          <div className="flex-1" />
          {!busy && (
            <Button variant="ghost" onClick={onClose}>
              {st === "done" ? "Close" : "Later"}
            </Button>
          )}
          {status.available && st !== "done" && (status.method ? (
            <Button variant="primary" icon={<Download className="size-3.5" />} onClick={onInstall} loading={busy} disabled={busy}>
              {st === "failed" ? "Try again" : status.method === "binary" ? "Install" : "Install and restart"}
            </Button>
          ) : (
            <Button variant="primary" icon={<Download className="size-3.5" />} onClick={openRelease}>
              Download
            </Button>
          ))}
        </>
      }
    >
      <div className="space-y-3">
        <div className="text-xs text-muted">
          You have {status.current || "an unknown version"}
          {rel.published && <> · released {new Date(rel.published).toLocaleDateString()}</>}
        </div>
        {busy && (
          <div className="space-y-1.5">
            <div className="text-[13px]">
              {st === "downloading" ? `Downloading… ${Math.round(status.progress * 100)}%` : st === "installing" ? "Installing…" : status.message || "Restarting…"}
            </div>
            <div className="h-1.5 w-full rounded-full bg-panel-3 overflow-hidden">
              <div
                className={cx("h-full rounded-full bg-accent transition-all", st !== "downloading" && "animate-pulse")}
                style={{ width: `${st === "downloading" ? Math.round(status.progress * 100) : 100}%` }}
              />
            </div>
          </div>
        )}
        {st === "done" && <div className="rounded-md border border-accent/40 bg-accent/10 px-3 py-2 text-[13px]">{status.message}</div>}
        {(st === "failed" || error) && (
          <div className="rounded-md border border-err/40 bg-err/10 px-3 py-2 text-xs text-err whitespace-pre-wrap break-words">{error || status.message}</div>
        )}
        {status.available && !busy && st !== "done" && (
          <div className="text-xs text-muted">{status.method ? METHOD_HINT[status.method] : `Install it by hand: ${status.manual}.`}</div>
        )}
        {rel.notes ? (
          <div className="rounded-md border border-line bg-panel-2 px-3 py-2 max-h-[40vh] overflow-y-auto text-[13px]">
            <Markdown>{rel.notes}</Markdown>
          </div>
        ) : null}
      </div>
    </Modal>
  );
}

// UpdateBox is the sidebar's notice of a new version.
export function UpdateBox() {
  const { status, showDialog, dismissed, dismiss } = useUpdate();
  if (!status?.available || !status.latest || (dismissed && !installing(status))) return null;
  if (status.state === "done") {
    // the CLI replaced its binary; the running one is still the old version
    return (
      <div className="mx-2 mb-2 rounded-lg border border-accent/40 bg-accent/10 p-2.5 text-[12.5px]">
        <div className="flex items-center gap-1.5 font-medium">
          <Sparkles className="size-3.5 text-accent" />
          <span className="flex-1">Restart to finish</span>
          <span className="font-mono text-[11px] text-muted">{status.latest.version}</span>
        </div>
        <div className="mt-1 text-xs text-muted">Version {status.latest.version} is installed; restart opsdeck to use it.</div>
      </div>
    );
  }
  return (
    <div className="mx-2 mb-2 rounded-lg border border-accent/40 bg-accent/10 p-2.5 text-[12.5px]">
      <div className="flex items-center gap-1.5 font-medium">
        <Sparkles className="size-3.5 text-accent" />
        <span className="flex-1">Update available</span>
        <span className="font-mono text-[11px] text-muted">{status.latest.version}</span>
      </div>
      <div className="mt-2 flex items-center gap-1.5">
        <Button size="sm" variant="primary" icon={<RefreshCw className="size-3" />} onClick={showDialog}>
          {installing(status) ? "Updating…" : "Update"}
        </Button>
        <Button size="sm" variant="ghost" onClick={dismiss}>
          Later
        </Button>
      </div>
    </div>
  );
}
