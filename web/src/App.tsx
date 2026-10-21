import { useState } from "react";
import { BrowserRouter, Navigate, Route, Routes, useParams } from "react-router-dom";
import { Layers, Plus } from "lucide-react";
import { AppProvider, WorkspaceProvider, useApp, useWs } from "./context/app";
import { TerminalProvider } from "./context/terminals";
import { ConfirmProvider, ToastProvider } from "./components/overlay";
import { Button, EmptyState, ErrorBox, Loading } from "./components/ui";
import { Boot } from "./pages/Boot";
import { CreateWorkspaceModal, Shell } from "./pages/Shell";
import { OverviewPage } from "./pages/Overview";
import { ResourcesPage } from "./pages/Resources";
import { VPNPage } from "./pages/VPN";
import { TerminalPage } from "./pages/Terminal";
import { DockerPage } from "./pages/Docker";
import { KubernetesPage } from "./pages/Kubernetes";
import { LogsPage } from "./pages/Logs";
import { DocsPage } from "./pages/Docs";
import { VaultPage } from "./pages/Vault";
import { AgentPage } from "./pages/Agent";
import { ActivityPage } from "./pages/Activity";
import { AppSettingsPage } from "./pages/Settings";
import { WorkspaceSettingsPage } from "./pages/WorkspaceSettings";

export function App() {
  return (
    <ToastProvider>
      <ConfirmProvider>
        <Boot>
          {(lock) => (
            <AppProvider onLock={lock}>
              <TerminalProvider>
                <BrowserRouter>
                  <Shell>
                    <Routes>
                      <Route path="/" element={<Home />} />
                      <Route path="/settings" element={<AppSettingsPage />} />
                      <Route path="/w/:ws/*" element={<WorkspaceRoutes />} />
                      <Route path="*" element={<Navigate to="/" replace />} />
                    </Routes>
                  </Shell>
                </BrowserRouter>
              </TerminalProvider>
            </AppProvider>
          )}
        </Boot>
      </ConfirmProvider>
    </ToastProvider>
  );
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
        <Routes>
          <Route path="overview" element={<OverviewPage />} />
          <Route path="agent" element={<AgentPage />} />
          <Route path="agent/:sid" element={<AgentPage />} />
          <Route path="resources" element={<ResourcesPage />} />
          <Route path="vpn" element={<VPNPage />} />
          <Route path="terminal" element={<TerminalPage />} />
          <Route path="docker" element={<DockerPage />} />
          <Route path="kubernetes" element={<KubernetesPage />} />
          <Route path="logs" element={<LogsPage />} />
          <Route path="docs" element={<DocsPage />} />
          <Route path="vault" element={<VaultPage />} />
          <Route path="activity" element={<ActivityPage />} />
          <Route path="settings" element={<WorkspaceSettingsPage />} />
          <Route path="*" element={<Navigate to="overview" replace />} />
        </Routes>
      </WorkspaceGate>
    </WorkspaceProvider>
  );
}

function WorkspaceGate({ children }: { children: React.ReactNode }) {
  const { workspace, loading, error, reload } = useWs();
  if (!workspace && loading) return <Loading />;
  if (!workspace && error) return <ErrorBox error={error} onRetry={() => void reload()} />;
  return <>{children}</>;
}
