import { useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { NavLink, useMatch, useNavigate } from "react-router-dom";
import {
  ChevronDown,
  ChevronsUpDown,
  FolderInput,
  Lock,
  Moon,
  Plug,
  Plus,
  Search,
  Settings,
  Sun,
} from "lucide-react";
import { api, errMsg } from "../api/client";
import { useApp } from "../context/app";
import { useTerminals } from "../context/terminals";
import { useTabs } from "../context/tabs";
import { NAV, NAV_GROUPS, SINGLETON_SECTIONS } from "../lib/nav";
import type { NavGroup } from "../lib/nav";
import { Button, FormField, IconButton, Input, Kbd, Tabs, Textarea, cx } from "../components/ui";
import { MenuItem, Modal, Popover, useToast } from "../components/overlay";
import { WORKSPACE_COLORS, slugify } from "../lib/format";
import { Logo } from "./Boot";
import { CommandPalette } from "./CommandPalette";


export function Shell({ children }: { children: ReactNode }) {
  const match = useMatch("/w/:ws/*");
  const { theme, setTheme, lock, workspaces } = useApp();
  const ws = match?.params.ws ?? lastWorkspace(workspaces.map((w) => w.name));
  const { tabs } = useTerminals();
  const wsTabs = useTabs();
  const [paletteOpen, setPaletteOpen] = useState(false);

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((o) => !o);
      }
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, []);

  return (
    <div className="flex h-full">
      <aside className="w-[216px] shrink-0 border-r border-line bg-panel flex flex-col">
        <div className="h-12 flex items-center gap-2 px-3 border-b border-line">
          <Logo size={22} />
          <span className="font-semibold tracking-tight">opsdeck</span>
          <div className="flex-1" />
          <IconButton title="Search (Ctrl+K)" onClick={() => setPaletteOpen(true)}>
            <Search className="size-4" />
          </IconButton>
        </div>
        <div className="p-2">
          <WorkspaceSwitcher current={ws} />
        </div>
        <nav className="flex-1 overflow-y-auto px-2 pb-2">
          {ws ? <SidebarNav ws={ws} terminals={tabs.length} /> : <div className="px-2.5 py-3 text-xs text-faint">Select or create a workspace to get started.</div>}
        </nav>
        <div className="border-t border-line p-2 space-y-px">
          {ws && (
            <NavLink
              to={`/w/${encodeURIComponent(ws)}/settings`}
              {...tabClick(wsTabs, ws, "settings")}
              className={({ isActive }) =>
                cx(
                  "flex items-center gap-2.5 h-8 px-2.5 rounded-md text-[13px] [&_svg]:size-4",
                  isActive ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2",
                )
              }
            >
              <Settings />
              Workspace settings
            </NavLink>
          )}
          <NavLink
            to="/connectors"
            className={({ isActive }) =>
              cx(
                "flex items-center gap-2.5 h-8 px-2.5 rounded-md text-[13px] [&_svg]:size-4",
                isActive ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2",
              )
            }
          >
            <Plug />
            Connectors
          </NavLink>
          <NavLink
            to="/settings"
            className={({ isActive }) =>
              cx(
                "flex items-center gap-2.5 h-8 px-2.5 rounded-md text-[13px] [&_svg]:size-4",
                isActive ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2",
              )
            }
          >
            <Settings />
            App settings
          </NavLink>
          <div className="flex items-center gap-1 pt-1 px-1">
            <IconButton title={theme === "dark" ? "Light theme" : "Dark theme"} onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>
              {theme === "dark" ? <Sun className="size-4" /> : <Moon className="size-4" />}
            </IconButton>
            <div className="flex-1" />
            <Button size="sm" variant="ghost" icon={<Lock className="size-3.5" />} onClick={() => void lock()} title="Lock vault">
              Lock
            </Button>
          </div>
        </div>
      </aside>
      <main className="flex-1 min-w-0 flex flex-col bg-bg">{children}</main>
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} ws={ws} />
    </div>
  );
}

// tabClick opens a workspace section from the sidebar: a click brings up the
// tab showing it or opens one, Ctrl/⌘-click always opens another tab (with
// Shift: in the background), a middle click opens one in the background.
function tabClick(tabs: ReturnType<typeof useTabs>, ws: string, to: string) {
  return {
    onClick: (e: React.MouseEvent) => {
      e.preventDefault();
      if (e.ctrlKey || e.metaKey) tabs.open(ws, to, !e.shiftKey);
      else tabs.show(ws, to);
    },
    onAuxClick: (e: React.MouseEvent) => {
      if (e.button === 1) {
        e.preventDefault();
        tabs.open(ws, to, false);
      }
    },
  };
}

const COLLAPSED_KEY = "opsdeck.nav.collapsed";

function loadCollapsed(): Set<NavGroup> {
  try {
    return new Set(JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? "[]") as NavGroup[]);
  } catch {
    return new Set();
  }
}

// SidebarNav lists a workspace's sections in groups that fold; a folded
// group still shows the section that is open.
function SidebarNav({ ws, terminals }: { ws: string; terminals: number }) {
  const wsTabs = useTabs();
  const [collapsed, setCollapsed] = useState(loadCollapsed);
  const match = useMatch("/w/:ws/:section/*");
  const current = match?.params.section ?? "";

  const toggle = (g: NavGroup) =>
    setCollapsed((c) => {
      const n = new Set(c);
      if (n.has(g)) n.delete(g);
      else n.add(g);
      try {
        localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...n]));
      } catch {
        /* ignore */
      }
      return n;
    });

  return (
    <>
      {NAV_GROUPS.map((g, gi) => {
        const folded = collapsed.has(g.id);
        const items = NAV.filter((n) => n.group === g.id && (!folded || n.to === current));
        return (
          <div key={g.id} className={cx(gi > 0 && "mt-1.5")}>
            {gi > 0 && (
              <button
                onClick={() => toggle(g.id)}
                className="group/h flex w-full items-center gap-1 h-6 px-2.5 text-[10.5px] font-medium uppercase tracking-wider text-faint hover:text-muted"
                aria-expanded={!folded}
              >
                <span className="flex-1 text-left">{g.label}</span>
                <ChevronDown className={cx("size-3 opacity-0 group-hover/h:opacity-100 transition-transform", folded && "-rotate-90 opacity-100")} />
              </button>
            )}
            <div className="space-y-px">
              {items.map((n) => (
                <NavLink
                  key={n.to}
                  to={`/w/${encodeURIComponent(ws)}/${n.to}`}
                  {...tabClick(wsTabs, ws, n.to)}
                  title={SINGLETON_SECTIONS.has(n.to) ? n.label : `${n.label} — opens in its own tab; Ctrl/⌘-click opens another`}
                  className={({ isActive }) =>
                    cx(
                      "group flex items-center gap-2.5 h-7 px-2.5 rounded-md text-[13px] transition-colors [&_svg]:size-4",
                      isActive ? "bg-panel-3 text-fg [&_svg]:text-accent" : "text-muted hover:text-fg hover:bg-panel-2",
                    )
                  }
                >
                  {n.icon}
                  <span className="flex-1">{n.label}</span>
                  {n.to === "terminal" && terminals > 0 && <span className="text-[11px] text-faint tabular-nums">{terminals}</span>}
                  {!SINGLETON_SECTIONS.has(n.to) && (
                    <span
                      role="button"
                      title="Open another tab"
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        wsTabs.open(ws, n.to);
                      }}
                      className="opacity-0 group-hover:opacity-100 rounded p-0.5 text-faint hover:text-fg hover:bg-panel-3 [&_svg]:!size-3.5 [&_svg]:!text-current"
                    >
                      <Plus />
                    </span>
                  )}
                </NavLink>
              ))}
            </div>
          </div>
        );
      })}
    </>
  );
}

function WorkspaceSwitcher({ current }: { current: string | null }) {
  const { workspaces } = useApp();
  const [open, setOpen] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const navigate = useNavigate();
  const cur = workspaces.find((w) => w.name === current);
  return (
    <div className="relative">
      <button
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center gap-2 rounded-md border border-line bg-panel-2 px-2 h-10 text-left hover:border-line-strong transition-colors"
      >
        {cur ? (
          <>
            <span className="size-5 rounded-md shrink-0 grid place-items-center text-[10px] font-bold text-white" style={{ background: cur.color || "#10b981" }}>
              {(cur.title || cur.name).slice(0, 1).toUpperCase()}
            </span>
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[13px] font-medium leading-tight">{cur.title || cur.name}</span>
              <span className="block truncate text-[10.5px] text-faint font-mono leading-tight">{cur.name}</span>
            </span>
          </>
        ) : (
          <span className="flex-1 text-muted text-[13px]">{workspaces.length ? "Choose workspace" : "No workspaces"}</span>
        )}
        <ChevronsUpDown className="size-3.5 text-faint" />
      </button>
      <Popover open={open} onClose={() => setOpen(false)} className="left-0 right-0 mt-1 max-h-[60vh] overflow-y-auto">
        {workspaces.map((w) => (
          <button
            key={w.name}
            onClick={() => {
              setOpen(false);
              navigate(`/w/${encodeURIComponent(w.name)}/overview`);
            }}
            className={cx("flex w-full items-center gap-2 px-3 h-9 text-left hover:bg-panel-3", w.name === current && "bg-panel-2")}
          >
            <span className="size-2.5 rounded-full shrink-0" style={{ background: w.color || "#10b981" }} />
            <span className="flex-1 min-w-0">
              <span className="block truncate text-[13px]">{w.title || w.name}</span>
            </span>
            <span className="text-[11px] text-faint tabular-nums">{w.hostCount} hosts</span>
          </button>
        ))}
        {workspaces.length > 0 && <div className="my-1 border-t border-line" />}
        <MenuItem
          icon={<Plus />}
          onClick={() => {
            setOpen(false);
            setCreateOpen(true);
          }}
        >
          New / import workspace
        </MenuItem>
      </Popover>
      <CreateWorkspaceModal open={createOpen} onClose={() => setCreateOpen(false)} />
    </div>
  );
}

export function CreateWorkspaceModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { reloadWorkspaces } = useApp();
  const toast = useToast();
  const navigate = useNavigate();
  const [tab, setTab] = useState<"new" | "import">("new");
  const [title, setTitle] = useState("");
  const [name, setName] = useState("");
  const [nameTouched, setNameTouched] = useState(false);
  const [description, setDescription] = useState("");
  const [color, setColor] = useState(WORKSPACE_COLORS[0]);
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (open) {
      setTitle("");
      setName("");
      setNameTouched(false);
      setDescription("");
      setPath("");
      setErr("");
      setColor(WORKSPACE_COLORS[Math.floor(Math.random() * WORKSPACE_COLORS.length)]);
    }
  }, [open]);

  const effName = nameTouched ? name : slugify(title);
  const valid = useMemo(() => (tab === "new" ? /^[a-z0-9][a-z0-9-]{0,47}$/.test(effName) : path.trim().length > 0), [tab, effName, path]);

  async function submit() {
    setBusy(true);
    setErr("");
    try {
      const w =
        tab === "new"
          ? await api.createWorkspace({ name: effName, title: title || effName, description, color })
          : await api.importWorkspace(path.trim());
      await reloadWorkspaces();
      toast.success(tab === "new" ? "Workspace created" : "Workspace imported", w.title || w.name);
      onClose();
      navigate(`/w/${encodeURIComponent(w.name)}/overview`);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Workspace"
      width={500}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={busy} disabled={!valid}>
            {tab === "new" ? "Create workspace" : "Import"}
          </Button>
        </>
      }
    >
      <Tabs
        className="-mt-2 mb-4"
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "new", label: "New workspace" },
          { id: "import", label: "Import existing folder" },
        ]}
      />
      {tab === "new" ? (
        <div className="space-y-3">
          <FormField label="Title" required>
            <Input autoFocus value={title} onChange={(e) => setTitle(e.target.value)} placeholder="ACME Corp" />
          </FormField>
          <FormField label="Name (identifier)" help="Lowercase letters, digits and dashes. Used in vault refs: vault://<name>/…. Cannot be changed later.">
            <Input
              mono
              value={effName}
              onChange={(e) => {
                setNameTouched(true);
                setName(e.target.value);
              }}
              placeholder="acme"
            />
          </FormField>
          <FormField label="Description">
            <Textarea value={description} onChange={(e) => setDescription(e.target.value)} className="min-h-14" placeholder="E-commerce platform, 3 environments" />
          </FormField>
          <div>
            <div className="text-xs font-medium text-muted mb-1.5">Color</div>
            <div className="flex gap-1.5">
              {WORKSPACE_COLORS.map((c) => (
                <button
                  key={c}
                  onClick={() => setColor(c)}
                  className={cx("size-6 rounded-md transition-transform", color === c && "ring-2 ring-offset-2 ring-offset-panel ring-fg scale-105")}
                  style={{ background: c }}
                  aria-label={c}
                />
              ))}
            </div>
          </div>
        </div>
      ) : (
        <div className="space-y-3">
          <div className="flex gap-2.5 text-muted">
            <FolderInput className="size-5 shrink-0" />
            <p>Register a folder that already contains a <code className="font-mono text-fg">workspace.yaml</code> (for example a git repository shared with your team). Files stay where they are.</p>
          </div>
          <FormField label="Folder path" required>
            <Input mono autoFocus value={path} onChange={(e) => setPath(e.target.value)} placeholder="/home/me/infra/acme-ops" />
          </FormField>
        </div>
      )}
      {err && <div className="mt-3 text-err text-xs">{err}</div>}
    </Modal>
  );
}

function lastWorkspace(names: string[]): string | null {
  try {
    const l = localStorage.getItem("opsdeck.lastWs");
    if (l && names.includes(l)) return l;
  } catch {
    /* ignore */
  }
  return names[0] ?? null;
}

export function KbdHint() {
  return (
    <span className="inline-flex items-center gap-0.5">
      <Kbd>Ctrl</Kbd>
      <Kbd>K</Kbd>
    </span>
  );
}
