import { useEffect, useMemo, useRef, useState } from "react";
import { NAV, SETTINGS_NAV } from "../lib/nav";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";
import { ArrowRight, Bot, Moon, Plus, Search, SlidersHorizontal, SquareTerminal } from "lucide-react";
import { api } from "../api/client";
import type { Resource } from "../api/types";
import { useApp } from "../context/app";
import { useAppearance } from "../context/appearance";
import { useTerminals } from "../context/terminals";
import { useTabs } from "../context/tabs";
import { KindIcon } from "../components/icons";
import { Kbd, cx } from "../components/ui";

type Cmd = { id: string; group: string; label: string; hint?: string; icon: ReactNode; run: () => void };


export function CommandPalette({ open, onClose, ws }: { open: boolean; onClose: () => void; ws: string | null }) {
  const { workspaces, theme, setTheme } = useApp();
  const { setCustomizeOpen } = useAppearance();
  const terms = useTerminals();
  const navigate = useNavigate();
  const tabs = useTabs();
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const [resources, setResources] = useState<Resource[]>([]);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    setQ("");
    setSel(0);
    if (ws) api.resources(ws).then(setResources).catch(() => setResources([]));
  }, [open, ws]);

  const cmds = useMemo<Cmd[]>(() => {
    const out: Cmd[] = [];
    const W = ws ? `/w/${encodeURIComponent(ws)}` : "";
    if (ws) {
      for (const r of resources) {
        if (r.kind === "Host") {
          out.push({
            id: `term:${r.name}`,
            group: "Hosts",
            label: `SSH ${r.name}`,
            hint: String(r.spec?.address ?? ""),
            icon: <SquareTerminal className="size-4" />,
            run: () => {
              terms.open(ws, r.name);
              navigate(`${W}/terminal`);
            },
          });
        }
        out.push({
          id: `res:${r.kind}/${r.name}`,
          group: "Resources",
          label: r.name,
          hint: r.kind,
          icon: <KindIcon kind={r.kind} className="size-4" />,
          run: () =>
            navigate(r.kind === "GitRemote" ? `${W}/git/${encodeURIComponent(r.name)}` : `${W}/resources?edit=${encodeURIComponent(`${r.kind}/${r.name}`)}`),
        });
      }
      out.push({
        id: "new-session",
        group: "Actions",
        label: "Ask the agent…",
        hint: "Ctrl/⌘ J",
        icon: <Bot className="size-4 text-agent-ink" />,
        run: () => navigate(`${W}/agent?new=1`),
      });
      out.push({
        id: "add-resource",
        group: "Actions",
        label: "Add resource",
        icon: <Plus className="size-4" />,
        run: () => navigate(`${W}/resources?add=1`),
      });
      for (const s of [...NAV, SETTINGS_NAV]) {
        out.push({
          id: `go:${s.to}`,
          group: "Go to",
          label: s.label,
          icon: <ArrowRight className="size-4" />,
          run: () => tabs.show(ws, s.to),
        });
      }
    }
    for (const w of workspaces) {
      out.push({
        id: `ws:${w.name}`,
        group: "Workspaces",
        label: w.title || w.name,
        hint: w.name,
        icon: <span className="size-2.5 rounded-full inline-block" style={{ background: w.color }} />,
        run: () => navigate(`/w/${encodeURIComponent(w.name)}/overview`),
      });
    }
    out.push({
      id: "theme",
      group: "Actions",
      label: `Switch to ${theme === "dark" ? "light" : "dark"} theme`,
      icon: <Moon className="size-4" />,
      run: () => setTheme(theme === "dark" ? "light" : "dark"),
    });
    out.push({
      id: "customize",
      group: "Actions",
      label: "Customize menu and appearance",
      icon: <SlidersHorizontal className="size-4" />,
      run: () => setCustomizeOpen(true),
    });
    out.push({ id: "app-settings", group: "Go to", label: "App settings", icon: <ArrowRight className="size-4" />, run: () => navigate("/settings") });
    return out;
  }, [ws, resources, workspaces, theme, setTheme, navigate, terms, tabs, setCustomizeOpen]);

  const filtered = useMemo(() => {
    const t = q.trim().toLowerCase();
    if (!t) {
      // actions first when nothing is typed
      const rest = cmds.filter((c) => c.group !== "Resources");
      return [...rest.filter((c) => c.group === "Actions"), ...rest.filter((c) => c.group !== "Actions")].slice(0, 60);
    }
    const scored = cmds
      .map((c) => {
        const hay = `${c.label} ${c.hint ?? ""} ${c.group}`.toLowerCase();
        let score = -1;
        if (c.label.toLowerCase().startsWith(t)) score = 3;
        else if (hay.includes(t)) score = 2;
        else {
          let i = 0;
          for (const ch of hay) if (ch === t[i]) i++;
          if (i === t.length) score = 1;
        }
        return { c, score };
      })
      .filter((x) => x.score > 0)
      .sort((a, b) => b.score - a.score);
    return scored.map((x) => x.c).slice(0, 60);
  }, [cmds, q]);

  useEffect(() => setSel(0), [q]);
  useEffect(() => {
    listRef.current?.querySelector(`[data-idx="${sel}"]`)?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  if (!open) return null;
  const run = (c: Cmd | undefined) => {
    if (!c) return;
    onClose();
    c.run();
  };
  let lastGroup = "";
  return createPortal(
    <div className="fixed inset-0 z-[60] bg-black/55 backdrop-blur-[2px] flex items-start justify-center pt-[12vh] p-4" onMouseDown={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Search or jump to"
        className="w-full max-w-[620px] rounded-xl border border-line-strong bg-panel shadow-2xl shadow-black/50 overflow-hidden"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2.5 px-4 h-14 border-b border-line">
          <Search className="size-4 text-muted" />
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search sections, hosts, resources, actions…"
            aria-label="Search sections, hosts, resources and actions"
            className="flex-1 bg-transparent outline-none text-[15px] placeholder:text-faint"
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setSel((s) => Math.min(filtered.length - 1, s + 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setSel((s) => Math.max(0, s - 1));
              } else if (e.key === "Enter") {
                e.preventDefault();
                run(filtered[sel]);
              } else if (e.key === "Escape") onClose();
            }}
          />
        </div>
        <div ref={listRef} role="listbox" className="max-h-[52vh] overflow-y-auto p-1.5">
          {filtered.length === 0 && <div className="px-4 py-6 text-center text-muted">No matches</div>}
          {filtered.map((c, i) => {
            const header = c.group !== lastGroup ? c.group : null;
            lastGroup = c.group;
            return (
              <div key={c.id}>
                {header && <div className="px-2.5 pt-2.5 pb-1 text-[10.5px] font-semibold uppercase tracking-wider text-faint">{header}</div>}
                <button
                  data-idx={i}
                  role="option"
                  aria-selected={i === sel}
                  onMouseEnter={() => setSel(i)}
                  onClick={() => run(c)}
                  className={cx("w-full flex items-center gap-2.5 px-2.5 h-9 rounded-md text-left text-[13px]", i === sel ? "bg-accent/12 text-fg" : "")}
                >
                  <span className="text-muted">{c.icon}</span>
                  <span className="flex-1 truncate">{c.label}</span>
                  {c.hint && <span className="text-[11px] text-faint font-mono truncate max-w-[40%]">{c.hint}</span>}
                </button>
              </div>
            );
          })}
        </div>
        <div className="flex items-center gap-4 px-4 h-10 border-t border-line text-[11.5px] text-faint">
          <span className="flex items-center gap-1.5">
            <Kbd>↑</Kbd>
            <Kbd>↓</Kbd> move
          </span>
          <span className="flex items-center gap-1.5">
            <Kbd>↵</Kbd> open
          </span>
          <span className="flex items-center gap-1.5">
            <Kbd>esc</Kbd> close
          </span>
        </div>
      </div>
    </div>,
    document.body,
  );
}
