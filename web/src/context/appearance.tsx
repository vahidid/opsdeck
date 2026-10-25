import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";

// Appearance holds the viewer's personal look and menu: it lives in this
// browser only (localStorage), never in the workspace files.

export type ThemePref = "dark" | "light" | "system";
export type Accent = "workspace" | "emerald" | "blue" | "amber" | "rose" | "cyan";
export type Density = "comfortable" | "compact";
export type Scale = "s" | "m" | "l";
export type Corners = "sharp" | "soft" | "round";
export type MenuMode = "panel" | "rail";

export type Appearance = {
  theme: ThemePref;
  accent: Accent;
  density: Density;
  scale: Scale;
  corners: Corners;
  menu: MenuMode;
  pinned: string[]; // section ids (lib/nav)
  hidden: string[]; // section ids left out of the menu (still in ⌘K)
};

export const ACCENTS: { id: Accent; label: string; swatch: string }[] = [
  { id: "workspace", label: "Workspace colour", swatch: "" },
  { id: "emerald", label: "Emerald", swatch: "#10b981" },
  { id: "blue", label: "Blue", swatch: "#3b82f6" },
  { id: "cyan", label: "Cyan", swatch: "#22d3ee" },
  { id: "amber", label: "Amber", swatch: "#f59e0b" },
  { id: "rose", label: "Rose", swatch: "#fb7185" },
];

export const DEFAULT_APPEARANCE: Appearance = {
  theme: "dark",
  accent: "workspace",
  density: "comfortable",
  scale: "m",
  corners: "soft",
  menu: "panel",
  pinned: ["resources", "docker", "logs"],
  hidden: [],
};

const KEY = "opsdeck.appearance";
const ZOOM: Record<Scale, number> = { s: 0.92, m: 1, l: 1.1 }; // matches index.css

function load(): Appearance {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) return { ...DEFAULT_APPEARANCE, ...(JSON.parse(raw) as Partial<Appearance>) };
    // the theme used to be stored on its own
    const old = localStorage.getItem("opsdeck.theme");
    if (old) return { ...DEFAULT_APPEARANCE, theme: JSON.parse(old) === "light" ? "light" : "dark" };
  } catch {
    /* ignore */
  }
  return DEFAULT_APPEARANCE;
}

type Ctx = {
  prefs: Appearance;
  set: (p: Partial<Appearance>) => void;
  reset: () => void;
  theme: "dark" | "light"; // the resolved theme
  customizeOpen: boolean;
  setCustomizeOpen: (v: boolean) => void;
};
const C = createContext<Ctx | null>(null);

function systemDark() {
  return typeof window !== "undefined" && window.matchMedia?.("(prefers-color-scheme: dark)").matches;
}

export function AppearanceProvider({ children }: { children: ReactNode }) {
  const [prefs, setPrefs] = useState<Appearance>(load);
  const [sysDark, setSysDark] = useState(systemDark);
  const [customizeOpen, setCustomizeOpen] = useState(false);

  useEffect(() => {
    const mq = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!mq) return;
    const h = () => setSysDark(mq.matches);
    mq.addEventListener("change", h);
    return () => mq.removeEventListener("change", h);
  }, []);

  const theme = prefs.theme === "system" ? (sysDark ? "dark" : "light") : prefs.theme;

  useLayoutEffect(() => {
    const el = document.documentElement;
    el.classList.toggle("light", theme === "light");
    el.classList.toggle("dark", theme === "dark");
    el.dataset.accent = prefs.accent;
    el.dataset.density = prefs.density;
    el.dataset.scale = prefs.scale;
    el.dataset.corners = prefs.corners;
  }, [theme, prefs.accent, prefs.density, prefs.scale, prefs.corners]);

  // engines with the older zoom (WebKit) scale percentage heights too: if the
  // zoomed app no longer fits the window, size it back down
  useLayoutEffect(() => {
    const root = document.getElementById("root");
    if (!root) return;
    root.style.height = "";
    const z = ZOOM[prefs.scale];
    if (z === 1) return;
    const h = root.getBoundingClientRect().height;
    if (Math.abs(h - window.innerHeight) > 2) root.style.height = `calc(100% / ${z})`;
  }, [prefs.scale]);

  const set = useCallback((p: Partial<Appearance>) => {
    setPrefs((cur) => {
      const next = { ...cur, ...p };
      try {
        localStorage.setItem(KEY, JSON.stringify(next));
      } catch {
        /* ignore */
      }
      return next;
    });
  }, []);
  const reset = useCallback(() => set(DEFAULT_APPEARANCE), [set]);

  const value = useMemo(() => ({ prefs, set, reset, theme, customizeOpen, setCustomizeOpen }), [prefs, set, reset, theme, customizeOpen]);
  return <C.Provider value={value}>{children}</C.Provider>;
}

export function useAppearance(): Ctx {
  const c = useContext(C);
  if (!c) throw new Error("AppearanceProvider missing");
  return c;
}

// readableOn picks dark or white text for a background colour (#rrggbb).
export function readableOn(hex: string): string {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) return "#ffffff";
  const n = parseInt(m[1], 16);
  const lin = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  const L = 0.2126 * lin((n >> 16) & 255) + 0.7152 * lin((n >> 8) & 255) + 0.0722 * lin(n & 255);
  // contrast against white vs. against near-black
  return (1.05 / (L + 0.05)) >= ((L + 0.05) / 0.06) ? "#ffffff" : "#0b0d10";
}
