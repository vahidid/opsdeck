import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { SINGLETON_SECTIONS, sectionInfo, sectionOf } from "../lib/nav";

// Workspace tabs: every section (an agent session, a doc, the VPN page…) can
// be open in its own tab. Each workspace has its own set, kept in
// localStorage; the URL always shows the active tab.

export type WsTab = { id: string; path: string; title?: string };
type WsTabs = { tabs: WsTab[]; active: string };

type TabsApi = {
  get: (ws: string) => WsTabs;
  open: (ws: string, path: string, activate?: boolean) => void;
  close: (ws: string, id: string) => void;
  activate: (ws: string, id: string) => void;
  move: (ws: string, from: number, to: number) => void;
  setTitle: (ws: string, id: string, title: string) => void;
  sync: (ws: string, path: string) => void;
};

const Ctx = createContext<TabsApi | null>(null);

const key = (ws: string) => `opsdeck.tabs.${ws}`;
const newId = () => Math.random().toString(36).slice(2, 10);
const wsUrl = (ws: string, path: string) => `/w/${encodeURIComponent(ws)}/${path}`;

function load(ws: string): WsTabs | null {
  try {
    const v = JSON.parse(localStorage.getItem(key(ws)) ?? "null") as WsTabs | null;
    if (v && Array.isArray(v.tabs)) {
      const tabs = v.tabs.filter((t) => t && typeof t.path === "string" && sectionInfo(t.path)).slice(0, 30);
      if (tabs.length) return { tabs, active: tabs.some((t) => t.id === v.active) ? v.active : tabs[0].id };
    }
  } catch {
    /* ignore */
  }
  return null;
}

export function TabsProvider({ children }: { children: ReactNode }) {
  const navigate = useNavigate();
  const [state, setState] = useState<Record<string, WsTabs>>({});
  const ref = useRef(state);
  ref.current = state;

  const update = useCallback((ws: string, fn: (t: WsTabs) => WsTabs) => {
    setState((s) => {
      const cur = s[ws] ?? load(ws) ?? { tabs: [], active: "" };
      const next = fn(cur);
      try {
        localStorage.setItem(key(ws), JSON.stringify({ tabs: next.tabs.map(({ id, path }) => ({ id, path })), active: next.active }));
      } catch {
        /* ignore */
      }
      return { ...s, [ws]: next };
    });
  }, []);

  const get = useCallback((ws: string) => ref.current[ws] ?? load(ws) ?? { tabs: [], active: "" }, []);

  const api = useMemo<TabsApi>(
    () => ({
      get,
      open(ws, path, activate = true) {
        const cur = get(ws);
        const single = SINGLETON_SECTIONS.has(sectionOf(path)) ? cur.tabs.find((t) => sectionOf(t.path) === sectionOf(path)) : undefined;
        const id = single?.id ?? newId();
        update(ws, (t) => ({
          tabs: single ? t.tabs : [...t.tabs, { id, path }],
          active: activate ? id : t.active,
        }));
        if (activate) navigate(wsUrl(ws, single?.path ?? path));
      },
      close(ws, id) {
        const cur = get(ws);
        const i = cur.tabs.findIndex((t) => t.id === id);
        if (i < 0) return;
        let tabs = cur.tabs.filter((t) => t.id !== id);
        if (!tabs.length) tabs = [{ id: newId(), path: "overview" }];
        let active = cur.active;
        if (active === id) {
          active = tabs[Math.min(i, tabs.length - 1)].id;
          navigate(wsUrl(ws, tabs.find((t) => t.id === active)!.path));
        }
        update(ws, () => ({ tabs, active }));
      },
      activate(ws, id) {
        const t = get(ws).tabs.find((x) => x.id === id);
        if (!t) return;
        update(ws, (s) => ({ ...s, active: id }));
        navigate(wsUrl(ws, t.path));
      },
      move(ws, from, to) {
        update(ws, (s) => {
          const tabs = [...s.tabs];
          const [x] = tabs.splice(from, 1);
          tabs.splice(to, 0, x);
          return { ...s, tabs };
        });
      },
      setTitle(ws, id, title) {
        if (get(ws).tabs.find((t) => t.id === id)?.title === title) return;
        update(ws, (s) => ({ ...s, tabs: s.tabs.map((t) => (t.id === id ? { ...t, title } : t)) }));
      },
      // The URL changed: the active tab follows it, unless it opened a
      // single-instance section that already has a tab.
      sync(ws, path) {
        const cur = get(ws);
        if (!cur.tabs.length) {
          const id = newId();
          update(ws, () => ({ tabs: [{ id, path }], active: id }));
          return;
        }
        const active = cur.tabs.find((t) => t.id === cur.active) ?? cur.tabs[0];
        const exact = cur.tabs.find((t) => t.path === path && t.id !== active.id);
        if (active.path === path) {
          if (cur.active !== active.id) update(ws, (s) => ({ ...s, active: active.id }));
          return;
        }
        const single = SINGLETON_SECTIONS.has(sectionOf(path)) ? cur.tabs.find((t) => sectionOf(t.path) === sectionOf(path)) : undefined;
        if (single) {
          update(ws, (s) => ({ tabs: s.tabs.map((t) => (t.id === single.id ? { ...t, path } : t)), active: single.id }));
          return;
        }
        if (exact) {
          update(ws, (s) => ({ ...s, active: exact.id }));
          return;
        }
        update(ws, (s) => ({ ...s, tabs: s.tabs.map((t) => (t.id === active.id ? { ...t, path, title: undefined } : t)) }));
      },
    }),
    // state: consumers re-render when tabs change
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [get, update, navigate, state],
  );

  return <Ctx.Provider value={api}>{children}</Ctx.Provider>;
}

export function useTabs(): TabsApi {
  const c = useContext(Ctx);
  if (!c) throw new Error("useTabs outside TabsProvider");
  return c;
}

// Per-tab context: which tab a page is rendered in, and whether it is visible.
export const TabCtx = createContext<{ ws: string; id: string; active: boolean } | null>(null);

export function useTabActive(): boolean {
  return useContext(TabCtx)?.active ?? true;
}

// useTabTitle names the tab a page is shown in (e.g. an agent session title).
export function useTabTitle(title: string | undefined | null) {
  const tab = useContext(TabCtx);
  const tabs = useContext(Ctx);
  useEffect(() => {
    if (tab && tabs && title) tabs.setTitle(tab.ws, tab.id, title);
  }, [tab, tabs, title]);
}

// useOpenInTab returns click handlers for in-app navigation: a plain click
// navigates the current tab; Ctrl/⌘-click or a middle click opens a new tab.
export function useOpenInTab() {
  const tab = useContext(TabCtx);
  const tabs = useContext(Ctx);
  const navigate = useNavigate();
  return useCallback(
    (path: string) => ({
      onClick: (e: React.MouseEvent) => {
        if ((e.ctrlKey || e.metaKey) && tab && tabs) {
          e.preventDefault();
          tabs.open(tab.ws, path, !e.shiftKey);
          return;
        }
        navigate(tab ? wsUrl(tab.ws, path) : path);
      },
      onAuxClick: (e: React.MouseEvent) => {
        if (e.button === 1 && tab && tabs) {
          e.preventDefault();
          tabs.open(tab.ws, path, false);
        }
      },
    }),
    [tab, tabs, navigate],
  );
}

export function tabLabel(t: WsTab): string {
  if (t.title) return t.title;
  const info = sectionInfo(t.path);
  const label = info?.label ?? t.path;
  const [, rest] = t.path.split(/\/(.*)/);
  if (sectionOf(t.path) === "agent" && rest) return `${label} · ${decodeURIComponent(rest).slice(0, 8)}`;
  const q = t.path.split("?")[1];
  if (q) {
    const p = new URLSearchParams(q);
    const v = p.get("path") ?? p.get("host") ?? p.get("kind") ?? p.get("item");
    if (v) return `${label} · ${v.split("/").pop()}`;
  }
  return label;
}
