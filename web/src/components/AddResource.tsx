import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";
import { Bot, Search, X } from "lucide-react";
import { useApp } from "../context/app";
import type { KindSchema } from "../api/types";
import { CATEGORIES, PRODUCTS, SUGGESTED } from "../lib/catalog";
import type { Category, Product } from "../lib/catalog";
import { BrandLogo } from "./brand";
import { KindIcon } from "./icons";
import { Button, IconButton, cx } from "./ui";

const CATEGORY_OF: Record<string, Category> = { compute: "servers", network: "vpn", service: "web", cluster: "k8s" };

// shortLabel drops the parenthesised list some kind labels carry.
export function shortLabel(label: string): string {
  return label.replace(/\s*\(.*\)\s*$/, "");
}

// catalogFor keeps the products whose kind this server knows and adds a plain
// tile for any kind the catalog does not cover yet.
export function catalogFor(kinds: KindSchema[]): Product[] {
  const known = new Set(kinds.map((k) => k.kind));
  const list = PRODUCTS.filter((p) => known.has(p.kind));
  for (const k of kinds) {
    if (list.some((p) => p.kind === k.kind)) continue;
    list.push({
      id: `kind:${k.kind}`,
      name: shortLabel(k.label),
      kind: k.kind,
      category: CATEGORY_OF[k.category] ?? "web",
      color: "#3F4A5A",
      mark: { icon: <KindIcon kind={k.kind} icon={k.icon} /> },
      caption: k.label,
    });
  }
  return list;
}

// AddResourcePicker is step one of adding a resource: pick the product you
// use; the form that follows is already set up for it.
export function AddResourcePicker({ open, onClose, onPick }: { open: boolean; onClose: () => void; onPick: (p: Product) => void }) {
  const { kinds } = useApp();
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [cat, setCat] = useState<Category | "all">("all");
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  const catalog = useMemo(() => catalogFor(kinds), [kinds]);

  useEffect(() => {
    if (open) {
      setQ("");
      setCat("all");
      setSel(0);
    }
  }, [open]);

  const t = q.trim().toLowerCase();
  const sections = useMemo(() => {
    if (t) {
      const hits = catalog.filter((p) => `${p.name} ${p.kind} ${p.caption} ${p.keywords ?? ""}`.toLowerCase().includes(t));
      return hits.length ? [{ title: `${hits.length} match${hits.length > 1 ? "es" : ""}`, items: hits }] : [];
    }
    if (cat !== "all") return [{ title: CATEGORIES.find((c) => c.id === cat)!.label, items: catalog.filter((p) => p.category === cat) }];
    const suggested = SUGGESTED.map((id) => catalog.find((p) => p.id === id)).filter((p): p is Product => !!p);
    return [
      ...(suggested.length ? [{ title: "Suggested", items: suggested }] : []),
      ...CATEGORIES.map((c) => ({ title: c.label, items: catalog.filter((p) => p.category === c.id) })).filter((s) => s.items.length),
    ];
  }, [catalog, cat, t]);
  const flat = sections.flatMap((s) => s.items);

  useEffect(() => setSel(0), [t, cat]);
  useEffect(() => {
    listRef.current?.querySelector(`[data-idx="${sel}"]`)?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [open, onClose]);

  if (!open) return null;
  let idx = -1;
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-3 sm:p-6 backdrop-blur-[2px]" onMouseDown={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Add resource"
        className="w-full max-w-[1040px] h-[min(720px,calc(100vh-24px))] rounded-xl border border-line-strong bg-panel shadow-2xl shadow-black/40 flex flex-col overflow-hidden"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header className="flex items-start gap-3 px-5 pt-5 pb-3 shrink-0">
          <div className="flex-1 min-w-0">
            <h2 className="text-[18px] font-semibold tracking-tight">Add resource</h2>
            <p className="text-muted mt-0.5">Pick the product you use. opsdeck fills in the technical type for you.</p>
          </div>
          <IconButton title="Close" onClick={onClose}>
            <X className="size-4" />
          </IconButton>
        </header>
        <div className="px-5 pb-4 border-b border-line shrink-0">
          <div className="relative">
            <Search className="size-4 absolute left-3 top-1/2 -translate-y-1/2 text-faint" />
            <input
              autoFocus
              value={q}
              onChange={(e) => setQ(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "ArrowDown" || e.key === "ArrowRight") {
                  e.preventDefault();
                  setSel((s) => Math.min(flat.length - 1, s + 1));
                } else if (e.key === "ArrowUp" || e.key === "ArrowLeft") {
                  e.preventDefault();
                  setSel((s) => Math.max(0, s - 1));
                } else if (e.key === "Enter" && flat[sel]) {
                  e.preventDefault();
                  onPick(flat[sel]);
                }
              }}
              aria-label="Search resource types"
              placeholder={`Search ${catalog.length} types… postgres, anyconnect, grafana, k8s`}
              className="w-full h-11 rounded-lg border border-line-strong bg-bg pl-10 pr-3 text-[14px] placeholder:text-faint focus:outline-none focus:border-accent focus:ring-2 focus:ring-accent/20"
            />
          </div>
          <div className="flex md:hidden gap-1.5 overflow-x-auto mt-3 [scrollbar-width:none]">
            {[{ id: "all" as const, label: "All" }, ...CATEGORIES].map((c) => (
              <button
                key={c.id}
                onClick={() => (setCat(c.id), setQ(""))}
                className={cx("shrink-0 h-7 px-3 rounded-full border text-[12px]", cat === c.id && !t ? "bg-fg text-bg border-fg" : "border-line text-muted")}
              >
                {c.label}
              </button>
            ))}
          </div>
        </div>
        <div className="flex-1 min-h-0 flex">
          <nav aria-label="Categories" className="hidden md:flex w-[200px] shrink-0 flex-col gap-px p-2.5 border-r border-line overflow-y-auto">
            {[{ id: "all" as const, label: "All" }, ...CATEGORIES].map((c) => {
              const n = c.id === "all" ? catalog.length : catalog.filter((p) => p.category === c.id).length;
              if (!n) return null;
              const on = cat === c.id && !t;
              return (
                <button
                  key={c.id}
                  onClick={() => (setCat(c.id), setQ(""))}
                  aria-pressed={on}
                  className={cx("flex items-center gap-2 h-8 px-2.5 rounded-md text-[13px] text-left", on ? "bg-panel-3 text-fg font-medium" : "text-muted hover:text-fg hover:bg-panel-2")}
                >
                  <span className="flex-1">{c.label}</span>
                  <span className="text-[11px] text-faint tabular-nums">{n}</span>
                </button>
              );
            })}
          </nav>
          <div ref={listRef} className="flex-1 min-w-0 overflow-y-auto p-4 sm:p-5 space-y-6">
            {sections.map((s) => (
              <section key={s.title}>
                <h3 className="text-[10.5px] font-semibold uppercase tracking-wider text-faint mb-2.5">{s.title}</h3>
                <div className="grid grid-cols-[repeat(auto-fill,minmax(210px,1fr))] gap-2.5">
                  {s.items.map((p) => {
                    idx++;
                    const i = idx;
                    return (
                      <button
                        key={`${s.title}:${p.id}`}
                        data-idx={i}
                        onClick={() => onPick(p)}
                        onMouseEnter={() => setSel(i)}
                        className={cx(
                          "flex items-center gap-3 p-3 rounded-lg border text-left transition-colors min-w-0",
                          i === sel ? "border-accent/60 bg-accent/[0.07]" : "border-line bg-panel hover:border-line-strong hover:bg-panel-2",
                        )}
                      >
                        <BrandLogo product={p} size={38} />
                        <span className="min-w-0">
                          <span className="block font-semibold truncate">{p.name}</span>
                          <span className="block text-[12px] text-faint truncate">{p.caption}</span>
                        </span>
                      </button>
                    );
                  })}
                </div>
              </section>
            ))}
            {sections.length === 0 && (
              <div className="py-16 text-center text-muted">
                <div className="font-medium text-fg">Nothing matches “{q}”</div>
                <div className="mt-1">
                  Use <b>Web panel / API</b> for any URL or <b>Linux host</b> for any SSH server, or let the agent set it up.
                </div>
              </div>
            )}
          </div>
        </div>
        <footer className="flex flex-wrap items-center gap-2 px-5 py-3 border-t border-line shrink-0">
          <span className="text-[12.5px] text-faint">Not sure what to pick?</span>
          <Button
            size="sm"
            variant="agentSoft"
            icon={<Bot className="size-3.5" />}
            onClick={() => {
              const ws = location.pathname.match(/^\/w\/([^/]+)/)?.[1];
              onClose();
              if (ws) navigate(`/w/${ws}/agent?new=1&draft=${encodeURIComponent(`Help me add ${q.trim() || "a resource"} to this workspace.`)}`);
            }}
          >
            Let the agent set it up
          </Button>
          <span className="flex-1" />
          <span className="hidden sm:flex items-center gap-1.5 text-[11.5px] text-faint">
            <kbd className="rounded border border-line-strong px-1 font-mono text-[10px]">↑↓</kbd> move
            <kbd className="rounded border border-line-strong px-1 font-mono text-[10px]">↵</kbd> choose
          </span>
        </footer>
      </div>
    </div>,
    document.body,
  );
}
