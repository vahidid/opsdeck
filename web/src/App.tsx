import { Suspense, lazy, useEffect, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes, useLocation, useParams } from "react-router-dom";
import { Layers, Plus } from "lucide-react";
import { AppProvider, WorkspaceProvider, useApp, useWs } from "./context/app";
import { TerminalProvider } from "./context/terminals";
import { UpdateProvider } from "./context/update";
import { TabCtx, TabsProvider, useTabs } from "./context/tabs";
import { TabStrip } from "./components/TabStrip";
import { sectionInfo } from "./lib/nav";
import { ConfirmProvider, ToastProvider, useToast } from "./components/overlay";
import { installDesktop, isDesktop } from "./lib/desktop";
import { Button, EmptyState, ErrorBox, Loading, cx } from "./components/ui";
import { Boot } from "./pages/Boot";
import { CreateWorkspaceModal, Shell } from "./pages/Shell";
import { OverviewPage } from "./pages/Overview";
import { ResourcesPage } from "./pages/Resources";
import { VPNPage } from "./pages/VPN";
import { GitPage } from "./pages/Git";
import { SkillsPage } from "./pages/Skills";
import { ScriptsPage } from "./pages/Scripts";
import { TopologyPage } from "./pages/Topology";
import { TerminalPage } from "./pages/Terminal";
import { DockerPage } from "./pages/docker";
import { KubernetesPage } from "./pages/kubernetes";
// the Ansible editor (CodeMirror, YAML) is loaded when it is opened
const AnsiblePage = lazy(() => import("./pages/ansible").then((m) => ({ default: m.AnsiblePage })));
import { LogsPage } from "./pages/Logs";
import { DocsPage } from "./pages/Docs";
import { VaultPage } from "./pages/Vault";
import { AgentPage } from "./pages/Agent";
import { ActivityPage } from "./pages/Activity";
import { AppSettingsPage } from "./pages/Settings";
import { ConnectorsPage } from "./pages/Connectors";
import { WorkspaceSettingsPage } from "./pages/WorkspaceSettings";

export function App() {
  return (
    <ToastProvider>
      {isDesktop && <DesktopBridge />}
      <ConfirmProvider>
        <Boot>
          {(lock) => (
            <AppProvider onLock={lock}>
              <UpdateProvider>
              <TerminalProvider>
                <BrowserRouter>
                  <TabsProvider>
                  <Shell>
                    <Routes>
                      <Route path="/" element={<Home />} />
                      <Route path="/settings" element={<AppSettingsPage />} />
                      <Route path="/connectors" element={<ConnectorsPage />} />
                      <Route path="/w/:ws/*" element={<WorkspaceRoutes />} />
                      <Route path="*" element={<Navigate to="/" replace />} />
                    </Routes>
                  </Shell>
                  </TabsProvider>
                </BrowserRouter>
              </TerminalProvider>
              </UpdateProvider>
            </AppProvider>
          )}
        </Boot>
      </ConfirmProvider>
    </ToastProvider>
  );
}

// In the desktop app, web links open in the default browser and downloads
// are saved in the downloads folder (see lib/desktop).
function DesktopBridge() {
  const toast = useToast();
  useEffect(() => installDesktop((n) => toast[n.kind](n.title, n.detail)), [toast]);
  return null;
}

function Home() {
  const { workspaces, workspacesLoaded } = useApp();
  const [open, setOpen] = useState(false);
  if (!workspacesLoaded) return <Loading />;
  const last = (() => {
    try {
      return localStorage.getItem("opsdeck.lastWs");
    } catch {
      return null;
    }
  })();
  const target = workspaces.find((w) => w.name === last) ?? workspaces[0];
  if (target) return <Navigate to={`/w/${encodeURIComponent(target.name)}/overview`} replace />;
  return (
    <div className="flex-1 grid place-items-center">
      <EmptyState
        icon={<Layers />}
        title="Create your first workspace"
        action={
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setOpen(true)}>
            New workspace
          </Button>
        }
      >
        A workspace holds one project: its servers, networks and VPNs, clusters, panels, documentation and credentials — isolated
        from your other projects.
      </EmptyState>
      <CreateWorkspaceModal open={open} onClose={() => setOpen(false)} />
    </div>
  );
}

function WorkspaceRoutes() {
  const { ws = "" } = useParams();
  try {
    localStorage.setItem("opsdeck.lastWs", ws);
  } catch {
    /* ignore */
  }
  return (
    <WorkspaceProvider key={ws} name={ws}>
      <WorkspaceGate>
        <TabbedWorkspace ws={ws} />
      </WorkspaceGate>
    </WorkspaceProvider>
  );
}

// The routes of one tab. Background tabs have no catch-all redirect, so a
// stale tab can never navigate the window.
function workspaceRoutes(active: boolean) {
  return (
    <>
      <Route path="overview" element={<OverviewPage />} />
      <Route path="agent" element={<AgentPage />} />
      <Route path="agent/:sid" element={<AgentPage />} />
      <Route path="resources" element={<ResourcesPage />} />
      <Route path="topology" element={<TopologyPage />} />
      <Route path="vpn" element={<VPNPage />} />
      <Route path="git" element={<GitPage />} />
      <Route path="git/:repo" element={<GitPage />} />
      <Route path="terminal" element={<TerminalPage />} />
      <Route path="docker" element={<DockerPage />} />
      <Route path="kubernetes" element={<KubernetesPage />} />
      <Route
        path="ansible"
        element={
          <Suspense fallback={<Loading />}>
            <AnsiblePage />
          </Suspense>
        }
      />
      <Route path="logs" element={<LogsPage />} />
      <Route path="docs" element={<DocsPage />} />
      <Route path="skills" element={<SkillsPage />} />
      <Route path="scripts" element={<ScriptsPage />} />
      <Route path="vault" element={<VaultPage />} />
      <Route path="activity" element={<ActivityPage />} />
      <Route path="settings" element={<WorkspaceSettingsPage />} />
      {active && <Route path="*" element={<Navigate to="overview" replace />} />}
    </>
  );
}

// TabbedWorkspace keeps every open tab mounted (hidden when in the
// background) so agent streams, logs and forms keep their state; the active
// tab follows the URL.
function TabbedWorkspace({ ws }: { ws: string }) {
  const location = useLocation();
  const tabs = useTabs();
  const prefix = `/w/${encodeURIComponent(ws)}/`;
  const rel = location.pathname.startsWith(prefix) ? location.pathname.slice(prefix.length) + location.search : "";
  useEffect(() => {
    if (rel && sectionInfo(rel)) tabs.sync(ws, rel);
  }, [ws, rel, tabs]);
  const { tabs: list, active } = tabs.get(ws);

  // Ctrl/⌘-click or middle-click on an in-app link opens it in a new tab.
  const intercept = (e: React.MouseEvent) => {
    const newTab = e.ctrlKey || e.metaKey || e.button === 1;
    if (!newTab) return;
    const a = (e.target as HTMLElement).closest("a[href]") as HTMLAnchorElement | null;
    const href = a?.getAttribute("href") ?? "";
    if (!href.startsWith(prefix) || a?.target === "_blank") return;
    e.preventDefault();
    e.stopPropagation();
    tabs.open(ws, href.slice(prefix.length), !e.shiftKey && e.button !== 1);
  };

  return (
    <div className="flex-1 min-h-0 flex flex-col" onClickCapture={intercept} onAuxClickCapture={intercept}>
      {list.length > 0 && <TabStrip ws={ws} />}
      {list.map((t) => {
        const isActive = t.id === active;
        const [pathname, search = ""] = t.path.split("?");
        const loc = isActive ? location : { pathname: prefix + pathname, search: search ? "?" + search : "", hash: "", state: null, key: t.id };
        return (
          <div key={t.id} className={cx("flex-1 min-h-0 flex flex-col", !isActive && "hidden")}>
            <TabCtx.Provider value={{ ws, id: t.id, active: isActive }}>
              <Routes location={loc}>{workspaceRoutes(isActive)}</Routes>
            </TabCtx.Provider>
          </div>
        );
      })}
      {list.length === 0 && <Routes>{workspaceRoutes(true)}</Routes>}
    </div>
  );
}

function WorkspaceGate({ children }: { children: React.ReactNode }) {
  const { workspace, loading, error, reload } = useWs();
  if (!workspace && loading) return <Loading />;
  if (!workspace && error) return <ErrorBox error={error} onRetry={() => void reload()} />;
  return <>{children}</>;
}
