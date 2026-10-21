import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { api } from "../api/client";
import type { ItemType, KindSchema, Resource, Workspace, WorkspaceSummary } from "../api/types";
import { useLocalStorage } from "../hooks";

type Theme = "dark" | "light";

type AppCtx = {
  theme: Theme;
  setTheme: (t: Theme) => void;
  workspaces: WorkspaceSummary[];
  workspacesLoaded: boolean;
  reloadWorkspaces: () => Promise<void>;
  kinds: KindSchema[];
  kindMap: Record<string, KindSchema>;
  itemTypes: ItemType[];
  lock: () => Promise<void>;
};
const Ctx = createContext<AppCtx | null>(null);

export function AppProvider({ children, onLock }: { children: ReactNode; onLock: () => void }) {
  const [theme, setTheme] = useLocalStorage<Theme>("opsdeck.theme", "dark");
  const [workspaces, setWorkspaces] = useState<WorkspaceSummary[]>([]);
  const [workspacesLoaded, setLoaded] = useState(false);
  const [kinds, setKinds] = useState<KindSchema[]>([]);
  const [itemTypes, setItemTypes] = useState<ItemType[]>([]);

  useEffect(() => {
    document.documentElement.classList.toggle("light", theme === "light");
    document.documentElement.classList.toggle("dark", theme === "dark");
  }, [theme]);

  const reloadWorkspaces = useCallback(async () => {
    try {
      setWorkspaces(await api.workspaces());
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    void reloadWorkspaces();
    api.kinds().then(setKinds).catch(() => {});
    api.itemTypes().then(setItemTypes).catch(() => {});
  }, [reloadWorkspaces]);

  const kindMap = useMemo(() => Object.fromEntries(kinds.map((k) => [k.kind, k])), [kinds]);

  const lock = useCallback(async () => {
    try {
      await api.vaultLock();
    } finally {
      onLock();
    }
  }, [onLock]);

  const value = useMemo(
    () => ({ theme, setTheme, workspaces, workspacesLoaded, reloadWorkspaces, kinds, kindMap, itemTypes, lock }),
    [theme, setTheme, workspaces, workspacesLoaded, reloadWorkspaces, kinds, kindMap, itemTypes, lock],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useApp(): AppCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("AppProvider missing");
  return c;
}

// ---------------- Workspace ----------------

type WsCtx = {
  name: string;
  workspace: Workspace | null;
  resources: Resource[];
  loading: boolean;
  error: string | null;
  reload: () => Promise<void>;
  reloadResources: () => Promise<void>;
  hosts: Resource[];
  byKind: (kind: string) => Resource[];
};
const WCtx = createContext<WsCtx | null>(null);

export function WorkspaceProvider({ name, children }: { name: string; children: ReactNode }) {
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [resources, setResources] = useState<Resource[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const reloadResources = useCallback(async () => {
    try {
      setResources(await api.resources(name));
    } catch {
      /* surfaced elsewhere */
    }
  }, [name]);

  const reload = useCallback(async () => {
    setLoading(true);
    try {
      const [w, r] = await Promise.all([api.workspace(name), api.resources(name)]);
      setWorkspace(w);
      setResources(r);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [name]);

  useEffect(() => {
    setWorkspace(null);
    setResources([]);
    void reload();
  }, [reload]);

  useEffect(() => {
    const color = workspace?.color || "#10b981";
    document.documentElement.style.setProperty("--c-accent", color);
    return () => {
      document.documentElement.style.removeProperty("--c-accent");
    };
  }, [workspace?.color]);

  const value = useMemo<WsCtx>(
    () => ({
      name,
      workspace,
      resources,
      loading,
      error,
      reload,
      reloadResources,
      hosts: resources.filter((r) => r.kind === "Host"),
      byKind: (k: string) => resources.filter((r) => r.kind === k),
    }),
    [name, workspace, resources, loading, error, reload, reloadResources],
  );
  return <WCtx.Provider value={value}>{children}</WCtx.Provider>;
}

export function useWs(): WsCtx {
  const c = useContext(WCtx);
  if (!c) throw new Error("WorkspaceProvider missing");
  return c;
}

export function hostAddress(r: Resource): string {
  const s = r.spec || {};
  const addr = String(s.address ?? "");
  const port = s.port ? `:${String(s.port)}` : "";
  const user = s.user ? `${String(s.user)}@` : "";
  return addr ? `${user}${addr}${port}` : "";
}

export function hostRoles(r: Resource): string[] {
  const roles = r.spec?.roles;
  return Array.isArray(roles) ? roles.map(String) : [];
}
