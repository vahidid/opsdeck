import { useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { NavLink, useMatch, useNavigate } from "react-router-dom";
import {
  Bot,
  ChevronsUpDown,
  FileKey,
  FolderInput,
  Lock,
  PanelLeftClose,
  PencilLine,
  Pin,
  PinOff,
  Plug,
  Plus,
  Search,
  Settings,
  SlidersHorizontal,
  X,
} from "lucide-react";
import { api, errMsg } from "../api/client";
import { useApp } from "../context/app";
import { readableOn, useAppearance } from "../context/appearance";
import { useTerminals } from "../context/terminals";
import { useTabs } from "../context/tabs";
import { UpdateBox } from "../context/update";
import { NAV, NAV_GROUPS, SETTINGS_NAV, SINGLETON_SECTIONS, groupOf } from "../lib/nav";
import type { NavGroup } from "../lib/nav";
import { useMediaQuery } from "../hooks";
import { Button, FormField, Input, Kbd, Tabs, Textarea, cx } from "../components/ui";
import { TabStrip } from "../components/TabStrip";
import { CustomizeDrawer } from "../components/Customize";
import { MenuItem, Modal, Popover, useToast } from "../components/overlay";
import { WORKSPACE_COLORS, slugify } from "../lib/format";
import { Logo } from "./Boot";
import { CommandPalette } from "./CommandPalette";


export function Shell({ children }: { children: ReactNode }) {
  const match = useMatch("/w/:ws/*");
  const { workspaces } = useApp();
  const { prefs } = useAppearance();
  const ws = match?.params.ws ?? lastWorkspace(workspaces.map((w) => w.name));
  const wsTabs = useTabs();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const wide = useMediaQuery("(min-width: 900px)");
  const docked = wide && prefs.menu === "panel";
  const section = useMatch("/w/:ws/:section/*")?.params.section ?? "";
  const [group, setGroup] = useState<NavGroup>(() => groupOf(section));
  const [flyout, setFlyout] = useState(false);

  // the panel follows the open section; a flyout closes once you go somewhere
  useEffect(() => {
    setGroup(groupOf(section));
    setFlyout(false);
  }, [section]);

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return;
      const k = e.key.toLowerCase();
      if (k === "k") {
        e.preventDefault();
        setPaletteOpen((o) => !o);
      } else if (k === "j" && ws) {
        e.preventDefault();
        wsTabs.show(ws, "agent");
      }
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [ws, wsTabs]);

  const pickGroup = (g: NavGroup) => {
    if (docked) {
      setGroup(g);
      return;
    }
    setFlyout((open) => !(open && g === group));
    setGroup(g);
  };

  return (
    <div className="flex h-full overflow-hidden">
      <Rail ws={ws} group={group} openGroup={docked || flyout ? group : null} section={section} onGroup={pickGroup} onSearch={() => setPaletteOpen(true)} />
      {docked && (
        <aside className="w-[232px] shrink-0 border-r border-line bg-panel flex flex-col min-h-0">
          <NavPanel ws={ws} group={group} section={section} />
        </aside>
      )}
      {!docked && flyout && (
        <>
          <div className="fixed inset-0 z-30" onMouseDown={() => setFlyout(false)} />
          <aside className="fixed left-[60px] top-0 bottom-0 z-40 w-[248px] border-r border-line-strong bg-panel flex flex-col min-h-0 shadow-2xl shadow-black/40">
            <NavPanel ws={ws} group={group} section={section} onClose={() => setFlyout(false)} />
          </aside>
        </>
      )}
      <main className="flex-1 min-w-0 min-h-0 flex flex-col bg-bg">
        <TopBar ws={ws} onSearch={() => setPaletteOpen(true)} />
        {children}
      </main>
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} ws={ws} />
      <CustomizeDrawer />
    </div>
  );
}

// Rail is the narrow column of icons: the five groups of a workspace, the
// agent (in its own colour), search, personalisation and the lock.
function Rail({
  ws,
  group,
  openGroup,
  section,
  onGroup,
  onSearch,
}: {
  ws: string | null;
  group: NavGroup;
  openGroup: NavGroup | null;
  section: string;
  onGroup: (g: NavGroup) => void;
  onSearch: () => void;
}) {
  const { lock } = useApp();
  const { setCustomizeOpen } = useAppearance();
  const wsTabs = useTabs();
  const navigate = useNavigate();
  const current = groupOf(section);
  const btn = "relative grid size-10 place-items-center rounded-[11px] transition-colors [&_svg]:size-[18px]";
  return (
    <nav aria-label="Main" className="w-[60px] shrink-0 border-r border-line bg-bg flex flex-col items-center gap-1 py-3 overflow-y-auto [scrollbar-width:none]">
      <button
        className="mb-2 rounded-[10px] focus-visible:outline-2 focus-visible:outline-accent"
        title="Overview"
        aria-label="Overview"
        onClick={() => (ws ? wsTabs.show(ws, "overview") : navigate("/"))}
      >
        <Logo size={34} />
      </button>
      {NAV_GROUPS.map((g) => {
        const open = openGroup === g.id;
        const here = current === g.id && !!section;
        return (
          <button
            key={g.id}
            disabled={!ws}
            onClick={() => onGroup(g.id)}
            title={g.label}
            aria-label={g.label}
            aria-pressed={open}
            className={cx(
              btn,
              "disabled:opacity-40",
              open ? "bg-accent/15 text-accent" : here ? "text-fg bg-panel-2" : "text-faint hover:text-fg hover:bg-panel-2",
              group === g.id && !open && !here && "text-muted",
            )}
          >
            {g.icon}
            {here && !open && <span className="absolute -left-[10px] top-2.5 bottom-2.5 w-[3px] rounded-r bg-accent" />}
          </button>
        );
      })}
      <div className="my-2 h-px w-6 bg-line" />
      <button
        disabled={!ws}
        onClick={() => ws && wsTabs.show(ws, "agent")}
        title="Agent (Ctrl/⌘+J)"
        aria-label="Agent"
        className={cx(
          btn,
          "disabled:opacity-40",
          section === "agent" ? "bg-agent text-agent-fg shadow-md shadow-agent/30" : "bg-agent/12 text-agent-ink hover:bg-agent/20",
        )}
      >
        <Bot />
      </button>
      <div className="flex-1" />
      <button onClick={onSearch} title="Search (Ctrl/⌘+K)" aria-label="Search" className={cx(btn, "text-faint hover:text-fg hover:bg-panel-2")}>
        <Search />
      </button>
      <button onClick={() => setCustomizeOpen(true)} title="Customize" aria-label="Customize" className={cx(btn, "text-faint hover:text-fg hover:bg-panel-2")}>
        <SlidersHorizontal />
      </button>
      <NavLink to="/settings" title="App settings" aria-label="App settings" className={({ isActive }) => cx(btn, isActive ? "bg-panel-2 text-fg" : "text-faint hover:text-fg hover:bg-panel-2")}>
        <Settings />
      </NavLink>
      <button onClick={() => void lock()} title="Lock vault" aria-label="Lock vault" className={cx(btn, "text-faint hover:text-fg hover:bg-panel-2")}>
        <Lock />
      </button>
    </nav>
  );
}

// NavPanel lists the sections of one group, below the workspace switcher and
// the pinned sections.
function NavPanel({ ws, group, section, onClose }: { ws: string | null; group: NavGroup; section: string; onClose?: () => void }) {
  const { prefs, set, setCustomizeOpen } = useAppearance();
  const { tabs } = useTerminals();
  const g = NAV_GROUPS.find((x) => x.id === group)!;
  const hidden = new Set(prefs.hidden);
  const items: NavItem[] = [...NAV.filter((n) => n.group === group), ...(group === "security" ? [{ ...SETTINGS_NAV, group: "security" as NavGroup }] : [])].filter(
    (n) => !hidden.has(n.to) || n.to === section,
  );
  const pinned = prefs.pinned.map((id) => NAV.find((n) => n.to === id)).filter((n): n is NavItem => !!n);
  const hiddenHere = NAV.filter((n) => n.group === group && hidden.has(n.to) && n.to !== section).length;
  return (
    <>
      <div className="p-3 pb-2">
        <WorkspaceSwitcher current={ws} />
      </div>
      <div className="flex-1 min-h-0 overflow-y-auto px-2.5 pb-3">
        {!ws ? (
          <div className="px-2.5 py-3 text-xs text-faint">Select or create a workspace to get started.</div>
        ) : (
          <>
            {pinned.length > 0 && (
              <div className="mb-4">
                <div className="px-2.5 h-7 flex items-center text-[10.5px] font-semibold uppercase tracking-wider text-faint">Pinned</div>
                {pinned.map((n) => (
                  <SectionLink key={n.to} ws={ws} n={n} terminals={tabs.length} />
                ))}
              </div>
            )}
            <div className="px-2.5 h-7 flex items-center gap-1 text-[10.5px] font-semibold uppercase tracking-wider text-faint">
              <span className="flex-1">{g.label}</span>
              {onClose ? (
                <button onClick={onClose} title="Close" className="rounded p-1 hover:bg-panel-3 hover:text-fg">
                  <X className="size-3.5" />
                </button>
              ) : (
                <button onClick={() => set({ menu: "rail" })} title="Collapse the menu to icons" className="rounded p-1 hover:bg-panel-3 hover:text-fg">
                  <PanelLeftClose className="size-3.5" />
                </button>
              )}
            </div>
            {items.map((n) => (
              <SectionLink key={n.to} ws={ws} n={n} terminals={tabs.length} />
            ))}
            {hiddenHere > 0 && (
              <button onClick={() => setCustomizeOpen(true)} className="mt-1 px-2.5 h-7 text-[11.5px] text-faint hover:text-fg">
                {hiddenHere} hidden · show
              </button>
            )}
          </>
        )}
      </div>
      <UpdateBox />
      <div className="border-t border-line p-2.5 space-y-px">
        <button
          onClick={() => setCustomizeOpen(true)}
          className="flex w-full items-center gap-2.5 h-8 px-2.5 rounded-md text-[13px] text-muted hover:text-fg hover:bg-panel-2 [&_svg]:size-4"
        >
          <PencilLine />
          Edit menu &amp; appearance
        </button>
        <NavLink
          to="/connectors"
          className={({ isActive }) =>
            cx("flex items-center gap-2.5 h-8 px-2.5 rounded-md text-[13px] [&_svg]:size-4", isActive ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2")
          }
        >
          <Plug />
          Connectors
        </NavLink>
      </div>
    </>
  );
}

type NavItem = (typeof NAV)[number];

function SectionLink({ ws, n, terminals }: { ws: string; n: NavItem; terminals: number }) {
  const wsTabs = useTabs();
  const { prefs, set } = useAppearance();
  const isPinned = prefs.pinned.includes(n.to);
  return (
    <NavLink
      to={`/w/${encodeURIComponent(ws)}/${n.to}`}
      {...tabClick(wsTabs, ws, n.to)}
      title={SINGLETON_SECTIONS.has(n.to) ? n.label : `${n.label} — Ctrl/⌘-click opens another tab`}
      className={({ isActive }) =>
        cx(
          "group flex items-center gap-2.5 h-8 px-2.5 rounded-md text-[13px] transition-colors [&_svg]:size-4",
          isActive ? "bg-panel-3 text-fg font-medium [&_svg]:text-accent" : "text-muted hover:text-fg hover:bg-panel-2 [&_svg]:text-faint",
        )
      }
    >
      {n.to === "agent" ? <span className="text-agent-ink [&_svg]:!text-agent-ink">{n.icon}</span> : n.icon}
      <span className="flex-1 truncate">{n.label}</span>
      {n.to === "terminal" && terminals > 0 && <span className="text-[11px] text-faint tabular-nums">{terminals}</span>}
      {n.to !== "settings" && (
        <span
          role="button"
          title={isPinned ? "Unpin" : "Pin to the top"}
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            set({ pinned: isPinned ? prefs.pinned.filter((x) => x !== n.to) : [...prefs.pinned, n.to] });
          }}
          className="rounded p-0.5 text-faint opacity-0 group-hover:opacity-100 hover:text-fg hover:bg-panel-3 [&_svg]:!size-3.5 [&_svg]:!text-current"
        >
          {isPinned ? <PinOff /> : <Pin />}
        </span>
      )}
    </NavLink>
  );
}

// TopBar holds the open tabs, the search box and the agent button.
function TopBar({ ws, onSearch }: { ws: string | null; onSearch: () => void }) {
  const wsTabs = useTabs();
  const section = useMatch("/w/:ws/:section/*")?.params.section ?? "";
  const inWs = !!useMatch("/w/:ws/*");
  return (
    <header className="h-12 shrink-0 flex items-center gap-2 border-b border-line bg-panel/60 pl-2 pr-3">
      <div className="flex-1 min-w-0 h-full flex items-center">{inWs && ws ? <TabStrip ws={ws} /> : null}</div>
      <button
        onClick={onSearch}
        className="flex items-center gap-2 h-8 rounded-lg border border-line bg-panel px-2.5 text-[12.5px] text-faint hover:text-muted hover:border-line-strong transition-colors sm:w-64 lg:w-72"
        title="Search (Ctrl/⌘+K)"
      >
        <Search className="size-4 shrink-0" />
        <span className="hidden sm:block flex-1 text-left truncate">Search or jump to…</span>
        <span className="hidden sm:inline-flex">
          <KbdHint />
        </span>
      </button>
      {ws && (
        <Button
          variant={section === "agent" ? "agentSoft" : "agent"}
          icon={<Bot className="size-4" />}
          onClick={() => wsTabs.show(ws, "agent")}
          title="Ask the agent (Ctrl/⌘+J)"
          className="shrink-0"
        >
          <span className="hidden md:inline">Agent</span>
        </Button>
      )}
    </header>
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
        className="w-full flex items-center gap-2.5 rounded-lg border border-line bg-bg px-2 h-11 text-left hover:border-line-strong transition-colors"
      >
        {cur ? (
          <>
            <span className="size-7 rounded-md shrink-0 grid place-items-center text-[12px] font-bold" style={{ background: cur.color || "#10b981", color: readableOn(cur.color || "#10b981") }}>
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
  const [tab, setTab] = useState<"new" | "import" | "file">("new");
  const [title, setTitle] = useState("");
  const [name, setName] = useState("");
  const [nameTouched, setNameTouched] = useState(false);
  const [description, setDescription] = useState("");
  const [color, setColor] = useState(WORKSPACE_COLORS[0]);
  const [path, setPath] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (open) {
      setTitle("");
      setName("");
      setNameTouched(false);
      setDescription("");
      setPath("");
      setFile(null);
      setPassword("");
      setErr("");
      setColor(WORKSPACE_COLORS[Math.floor(Math.random() * WORKSPACE_COLORS.length)]);
    }
  }, [open]);

  const effName = nameTouched ? name : slugify(title);
  const valid = useMemo(
    () => (tab === "new" ? /^[a-z0-9][a-z0-9-]{0,47}$/.test(effName) : tab === "import" ? path.trim().length > 0 : !!file && password.length > 0),
    [tab, effName, path, file, password],
  );

  async function importFile() {
    let bundle: unknown;
    try {
      bundle = JSON.parse(await file!.text());
    } catch {
      throw new Error("This is not an opsdeck workspace export.");
    }
    const r = await api.importWorkspaceFile(bundle, password);
    const kept = r.skipped ? `, ${r.skipped} kept from this vault` : "";
    toast.success("Workspace imported", `${r.workspace.title || r.workspace.name}: ${r.items} vault item${r.items === 1 ? "" : "s"}${kept}`);
    return r.workspace;
  }

  async function submit() {
    setBusy(true);
    setErr("");
    try {
      const w =
        tab === "new"
          ? await api.createWorkspace({ name: effName, title: title || effName, description, color })
          : tab === "import"
            ? await api.importWorkspace(path.trim())
            : await importFile();
      await reloadWorkspaces();
      if (tab !== "file") toast.success(tab === "new" ? "Workspace created" : "Workspace imported", w.title || w.name);
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
          { id: "file", label: "Import export file" },
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
      ) : tab === "file" ? (
        <div className="space-y-3">
          <div className="flex gap-2.5 text-muted">
            <FileKey className="size-5 shrink-0" />
            <p>
              Open a file made with <i>Export workspace</i> in workspace settings. Its hosts, resources, docs and vault items are added here; the secrets are stored in your vault under
              your own master password.
            </p>
          </div>
          <FormField label="Export file" required>
            <Input type="file" accept=".opsdeck,.json" className="pt-1" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
          </FormField>
          <FormField label="File password" required help="The password chosen when the file was exported.">
            <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} onKeyDown={(e) => e.key === "Enter" && valid && void submit()} />
          </FormField>
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
