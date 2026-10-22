import { useEffect, useRef, useState } from "react";
import { ChevronDown, ChevronRight, ExternalLink, KeyRound, Plug, Plus, RefreshCw, Terminal, Trash2 } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { ConnInput, ConnOptions, GitConnection, OAuthStart } from "../api/types";
import { Badge, Button, CopyButton, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, PageHeader, Spinner, Textarea, cx } from "../components/ui";
import { Modal, useConfirm, useToast } from "../components/overlay";
import { ProviderMark } from "../components/brand";
import { useAsync } from "../hooks";
import { timeAgo } from "../lib/format";

type Service = { id: string; label: string; provider: "github" | "gitlab"; url: string; selfHosted: boolean; hint: string };

const SERVICES: Service[] = [
  { id: "github", label: "GitHub", provider: "github", url: "https://github.com", selfHosted: false, hint: "github.com" },
  { id: "gitlab", label: "GitLab.com", provider: "gitlab", url: "https://gitlab.com", selfHosted: false, hint: "gitlab.com" },
  { id: "gitlab-self", label: "GitLab self-hosted", provider: "gitlab", url: "", selfHosted: true, hint: "your company's GitLab" },
  { id: "ghes", label: "GitHub Enterprise Server", provider: "github", url: "", selfHosted: true, hint: "self-hosted GitHub" },
];

const METHOD_LABEL: Record<string, string> = { token: "access token", oauth: "signed in with the browser", gh: "GitHub CLI login", glab: "GitLab CLI login" };

export function ConnectorsPage() {
  const conns = useAsync(() => api.gitConnections(), []);
  const [adding, setAdding] = useState<GitConnection | true | null>(null);
  return (
    <div className="flex-1 overflow-y-auto">
      <PageHeader
        icon={<Plug />}
        title="Connectors"
        subtitle="GitHub and GitLab accounts connected on this computer. Any workspace can then add their repositories in one click."
        actions={
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
            Connect an account
          </Button>
        }
      />
      <div className="px-5 pb-8 space-y-3 max-w-4xl">
        {conns.error && <ErrorBox error={conns.error} onRetry={() => void conns.reload()} />}
        {conns.loading && !conns.data && <Loading />}
        {conns.data?.length === 0 && (
          <EmptyState icon={<Plug />} title="No accounts connected">
            Connect GitHub or GitLab once; then adding a repository to a workspace is picking it from a list.
            <div className="mt-4">
              <ServiceTiles onPick={() => setAdding(true)} />
            </div>
          </EmptyState>
        )}
        {(conns.data ?? []).map((c) => (
          <ConnectorCard key={c.id} c={c} onChanged={() => void conns.reload()} onReconnect={() => setAdding(c)} />
        ))}
      </div>
      {adding && (
        <ConnectModal
          reconnect={adding === true ? undefined : adding}
          onClose={() => setAdding(null)}
          onConnected={() => {
            setAdding(null);
            void conns.reload();
          }}
        />
      )}
    </div>
  );
}

function ServiceTiles({ onPick, selected }: { onPick: (s: Service) => void; selected?: string }) {
  return (
    <div className="grid grid-cols-2 gap-2 text-left">
      {SERVICES.map((s) => (
        <button
          key={s.id}
          type="button"
          onClick={() => onPick(s)}
          className={cx(
            "flex items-center gap-3 rounded-md border px-3 py-2.5",
            selected === s.id ? "border-accent bg-accent/8" : "border-line bg-panel-2 hover:border-line-strong",
          )}
        >
          <ProviderMark provider={s.provider} className="size-5 shrink-0" />
          <span className="min-w-0">
            <span className="block text-[13px] font-medium">{s.label}</span>
            <span className="block text-[11px] text-faint">{s.hint}</span>
          </span>
        </button>
      ))}
    </div>
  );
}

function ConnectorCard({ c, onChanged, onReconnect }: { c: GitConnection; onChanged: () => void; onReconnect: () => void }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [testing, setTesting] = useState(false);
  const [state, setState] = useState<{ ok: boolean; error?: string } | null>(null);
  async function test() {
    setTesting(true);
    try {
      const r = await api.gitConnTest(c.id);
      setState(r);
    } catch (e) {
      setState({ ok: false, error: errMsg(e) });
    } finally {
      setTesting(false);
    }
  }
  async function remove() {
    const ok = await confirm({
      title: `Disconnect ${c.host}?`,
      message: "The token is deleted from the vault. Repositories that use this account stop pulling and pushing until you connect it again (their working copies stay).",
      confirmLabel: "Disconnect",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.gitDisconnect(c.id);
      onChanged();
    } catch (e) {
      toast.error("Disconnect failed", errMsg(e));
    }
  }
  return (
    <div className="rounded-lg border border-line bg-panel px-4 py-3">
      <div className="flex items-center gap-3">
        <ProviderMark provider={c.provider} className="size-6 text-fg shrink-0" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-[14px] font-semibold">{c.name || c.login}</span>
            <span className="text-muted text-xs font-mono">@{c.login}</span>
            <Badge tone="neutral">{c.host}</Badge>
            {state && <Badge tone={state.ok ? "ok" : "err"}>{state.ok ? "working" : "failing"}</Badge>}
          </div>
          <div className="text-[11.5px] text-faint">
            {METHOD_LABEL[c.method] ?? c.method} · connected {timeAgo(c.created)} · token in <span className="font-mono">{c.credential}</span> · id{" "}
            <span className="font-mono">{c.id}</span>
          </div>
        </div>
        <Button size="sm" variant="ghost" loading={testing} icon={<RefreshCw className="size-3.5" />} onClick={() => void test()}>
          Test
        </Button>
        <Button size="sm" variant="secondary" onClick={onReconnect}>
          Reconnect
        </Button>
        <IconButton title="Disconnect" onClick={() => void remove()}>
          <Trash2 className="size-4" />
        </IconButton>
      </div>
      {state && !state.ok && <div className="mt-2 text-xs text-err whitespace-pre-wrap">{state.error}</div>}
    </div>
  );
}

// ConnectModal connects (or reconnects) an account: choose the service,
// then the easiest available way in — an existing gh/glab login, browser
// sign-in, or a token from a pre-filled page.
export function ConnectModal({ reconnect, onClose, onConnected }: { reconnect?: GitConnection; onClose: () => void; onConnected: (c: GitConnection) => void }) {
  const toast = useToast();
  const initial = reconnect ? SERVICES.find((s) => s.provider === reconnect.provider && (s.url === reconnect.url || s.selfHosted)) : undefined;
  const [svc, setSvc] = useState<Service | undefined>(initial);
  const [url, setUrl] = useState(reconnect?.url ?? "");
  const [caCert, setCaCert] = useState(reconnect?.caCert ?? "");
  const [apiUrl, setApiUrl] = useState(reconnect?.apiUrl ?? "");
  const [clientId, setClientId] = useState(reconnect?.oauthClientId ?? "");
  const [advanced, setAdvanced] = useState(!!(reconnect?.caCert || reconnect?.apiUrl));
  const [opts, setOpts] = useState<ConnOptions | null>(null);
  const [optsErr, setOptsErr] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [flow, setFlow] = useState<OAuthStart | null>(null);
  const flowRef = useRef<string | null>(null);

  const input = (): ConnInput => ({ provider: svc!.provider, url: svc!.selfHosted ? url : svc!.url, caCert, apiUrl, clientId: clientId || opts?.clientId });
  const ready = !!svc && (!svc.selfHosted || /\w+\.\w+|localhost|\d+\.\d+/.test(url));

  // Look up the ways in for this server.
  useEffect(() => {
    setOpts(null);
    setOptsErr("");
    if (!ready) return;
    const t = setTimeout(() => {
      api
        .gitConnOptions(input())
        .then((o) => {
          setOpts(o);
          if (!clientId && o.clientId) setClientId(o.clientId);
        })
        .catch((e) => setOptsErr(errMsg(e)));
    }, 400);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [svc, url, ready]);

  // Stop polling when the dialog closes.
  useEffect(
    () => () => {
      if (flowRef.current) void api.gitOAuthCancel(flowRef.current).catch(() => {});
    },
    [],
  );

  async function connect(method: "token" | "cli") {
    setBusy(method);
    try {
      const c = await api.gitConnect({ ...input(), method, token: method === "token" ? token.trim() : undefined });
      toast.success(`Connected ${c.host}`, `as ${c.login}`);
      onConnected(c);
    } catch (e) {
      toast.error("Could not connect", errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  async function signIn() {
    setBusy("oauth");
    try {
      const f = await api.gitOAuthStart(input());
      setFlow(f);
      flowRef.current = f.flow;
      window.open(f.verificationUriComplete || f.verificationUri, "_blank", "noopener");
      for (;;) {
        await new Promise((r) => setTimeout(r, 2000));
        if (flowRef.current !== f.flow) return;
        const st = await api.gitOAuthStatus(f.flow);
        if (st.status === "done" && st.connection) {
          flowRef.current = null;
          toast.success(`Connected ${st.connection.host}`, `as ${st.connection.login}`);
          onConnected(st.connection);
          return;
        }
        if (st.status === "error") throw new Error(st.error);
      }
    } catch (e) {
      toast.error("Sign-in failed", errMsg(e));
      setFlow(null);
      flowRef.current = null;
    } finally {
      setBusy(null);
    }
  }

  const providerName = svc?.provider === "github" ? "GitHub" : "GitLab";
  const cliTool = opts?.cli.tool ?? (svc?.provider === "github" ? "gh" : "glab");
  return (
    <Modal open onClose={onClose} title={reconnect ? `Reconnect ${reconnect.host}` : "Connect a Git account"} icon={<Plug />} width={600}>
      <div className="space-y-4">
        {!reconnect && <ServiceTiles selected={svc?.id} onPick={(s) => setSvc(s)} />}
        {svc?.selfHosted && (
          <FormField label="Server address" help={svc.provider === "gitlab" ? "e.g. gitlab.example.com" : "e.g. github.example.com"}>
            <Input mono autoFocus value={url} onChange={(e) => setUrl(e.target.value)} placeholder={svc.provider === "gitlab" ? "https://gitlab.example.com" : "https://github.example.com"} />
          </FormField>
        )}
        {svc && (
          <div>
            <button type="button" onClick={() => setAdvanced((v) => !v)} className="flex items-center gap-1 text-xs text-muted hover:text-fg">
              {advanced ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />} Advanced
            </button>
            {advanced && (
              <div className="mt-2 space-y-3">
                {svc.selfHosted && (
                  <FormField label="CA certificate (PEM)" help="Only when the server uses a certificate from a private CA.">
                    <Textarea mono rows={3} value={caCert} onChange={(e) => setCaCert(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" />
                  </FormField>
                )}
                {svc.selfHosted && (
                  <FormField label="API URL" help="Only when the API is not at the usual place.">
                    <Input mono value={apiUrl} onChange={(e) => setApiUrl(e.target.value)} placeholder={svc.provider === "gitlab" ? "https://gitlab.example.com/api/v4" : "https://github.example.com/api/v3"} />
                  </FormField>
                )}
                <FormField
                  label="OAuth application ID (for browser sign-in)"
                  help={
                    <>
                      {svc.provider === "gitlab"
                        ? "Create an application (not confidential, scopes api, read_user, write_repository; any redirect URI) and paste its Application ID."
                        : "Create an OAuth app with “Enable Device Flow” and paste its Client ID."}{" "}
                      {opts?.oauthAppPage && (
                        <a href={opts.oauthAppPage} target="_blank" rel="noreferrer" className="text-accent hover:underline">
                          Open the page
                        </a>
                      )}
                    </>
                  }
                >
                  <Input mono value={clientId} onChange={(e) => setClientId(e.target.value)} placeholder="optional" />
                </FormField>
              </div>
            )}
          </div>
        )}

        {svc && ready && !opts && !optsErr && (
          <div className="flex items-center gap-2 text-xs text-muted">
            <Spinner className="size-3.5" /> Checking the server…
          </div>
        )}
        {optsErr && <div className="text-xs text-err">{optsErr}</div>}

        {opts && (
          <div className="space-y-3 border-t border-line pt-4">
            {opts.cli.ready && (
              <Way icon={<Terminal className="size-4" />} title={`Use your ${cliTool} login`} sub={`You are already logged in to ${opts.host} with the ${providerName} CLI; opsdeck copies its token into the vault.`}>
                <Button variant="primary" loading={busy === "cli"} onClick={() => void connect("cli")}>
                  Connect
                </Button>
              </Way>
            )}
            {(clientId || opts.clientId) && (
              <Way icon={<ProviderMark provider={svc!.provider} className="size-4" />} title={`Sign in with ${providerName}`} sub="Opens the sign-in page; approve opsdeck there and come back.">
                {flow ? (
                  <div className="text-right">
                    <div className="flex items-center gap-1 justify-end">
                      <span className="font-mono text-[15px] tracking-wider">{flow.userCode}</span>
                      <CopyButton text={flow.userCode} />
                    </div>
                    <a href={flow.verificationUriComplete || flow.verificationUri} target="_blank" rel="noreferrer" className="text-[11px] text-accent hover:underline">
                      Enter it at {flow.verificationUri.replace(/^https?:\/\//, "")}
                    </a>
                    <div className="text-[11px] text-faint flex items-center gap-1 justify-end mt-0.5">
                      <Spinner className="size-3" /> waiting for approval…
                    </div>
                  </div>
                ) : (
                  <Button variant={opts.cli.ready ? "secondary" : "primary"} loading={busy === "oauth"} onClick={() => void signIn()}>
                    Sign in
                  </Button>
                )}
              </Way>
            )}
            <Way
              icon={<KeyRound className="size-4" />}
              title="Use an access token"
              sub={
                <>
                  <a href={opts.tokenPage} target="_blank" rel="noreferrer" className="text-accent hover:underline inline-flex items-center gap-1">
                    Create a token <ExternalLink className="size-3" />
                  </a>{" "}
                  (the page opens with the needed scopes selected), then paste it here. It is stored in the vault; the agent can use it but never sees it.
                </>
              }
            >
              <span />
            </Way>
            <div className="flex gap-2 ps-7">
              <Input
                mono
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder={svc?.provider === "github" ? "ghp_… or github_pat_…" : "glpat-…"}
                onKeyDown={(e) => e.key === "Enter" && token.trim() && void connect("token")}
              />
              <Button variant={opts.cli.ready || clientId ? "secondary" : "primary"} disabled={!token.trim()} loading={busy === "token"} onClick={() => void connect("token")}>
                Connect
              </Button>
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}

function Way({ icon, title, sub, children }: { icon: React.ReactNode; title: string; sub: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-3">
      <span className="text-muted shrink-0">{icon}</span>
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium">{title}</div>
        <div className="text-[11.5px] text-muted">{sub}</div>
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}
