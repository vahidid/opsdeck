import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { api } from "../api/client";

export type TermStatus = "connecting" | "connected" | "closed" | "error";

export type TermTab = {
  id: string;
  ws: string;
  host: string;
  status: TermStatus;
  message: string;
  term: Terminal;
  fit: FitAddon;
  el: HTMLDivElement;
  socket: WebSocket | null;
  opened: boolean;
  created: number;
};

type Ctx = {
  tabs: TermTab[];
  activeId: string | null;
  setActive: (id: string) => void;
  open: (ws: string, host: string) => string;
  close: (id: string) => void;
  reconnect: (id: string) => void;
  attach: (id: string, parent: HTMLElement | null) => void;
  fitActive: () => void;
};
const TCtx = createContext<Ctx | null>(null);

function termTheme() {
  const css = getComputedStyle(document.documentElement);
  const v = (n: string, f: string) => css.getPropertyValue(n).trim() || f;
  const light = document.documentElement.classList.contains("light");
  return {
    background: v("--c-panel", "#101317"),
    foreground: v("--c-text", "#e5e8ec"),
    cursor: v("--c-accent", "#10b981"),
    cursorAccent: v("--c-panel", "#101317"),
    selectionBackground: light ? "#bcd3f5" : "#2a3a50",
    black: light ? "#1e2127" : "#20252c",
    brightBlack: "#5d6672",
    red: "#f87171",
    brightRed: "#fca5a5",
    green: light ? "#059669" : "#34d399",
    brightGreen: "#6ee7b7",
    yellow: light ? "#b45309" : "#fbbf24",
    brightYellow: "#fcd34d",
    blue: "#60a5fa",
    brightBlue: "#93c5fd",
    magenta: "#c084fc",
    brightMagenta: "#d8b4fe",
    cyan: "#22d3ee",
    brightCyan: "#67e8f9",
    white: light ? "#4b5563" : "#d1d5db",
    brightWhite: light ? "#111827" : "#f9fafb",
  };
}

export function TerminalProvider({ children }: { children: ReactNode }) {
  const [tabs, setTabs] = useState<TermTab[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [ver, bump] = useState(0);
  const tabsRef = useRef<TermTab[]>([]);
  tabsRef.current = tabs;
  const rerender = useCallback(() => bump((x) => x + 1), []);

  const connect = useCallback(
    (tab: TermTab) => {
      try {
        tab.socket?.close();
      } catch {
        /* ignore */
      }
      tab.status = "connecting";
      tab.message = "";
      rerender();
      const ws = new WebSocket(api.terminalUrl(tab.ws, tab.host, tab.term.cols || 120, tab.term.rows || 32));
      tab.socket = ws;
      const dim = (s: string) => tab.term.write(`\x1b[2m${s}\x1b[0m\r\n`);
      ws.onopen = () => {
        tab.status = "connected";
        rerender();
        ws.send(JSON.stringify({ t: "r", c: tab.term.cols, r: tab.term.rows }));
      };
      ws.onmessage = (ev) => {
        let m: { t: string; d?: string; code?: number; msg?: string };
        try {
          m = JSON.parse(typeof ev.data === "string" ? ev.data : "");
        } catch {
          if (typeof ev.data === "string") tab.term.write(ev.data);
          return;
        }
        if (m.t === "d" && m.d !== undefined) {
          tab.term.write(m.d);
          if (tab.message) {
            // first output: the "connecting via …" notice is over
            tab.message = "";
            rerender();
          }
        } else if (m.t === "s") {
          dim(`▸ ${m.msg ?? ""}`);
          tab.message = m.msg ?? "";
          rerender();
        } else if (m.t === "e") {
          tab.term.write(`\r\n\x1b[31m✖ ${m.msg ?? "error"}\x1b[0m\r\n`);
          tab.status = "error";
          tab.message = m.msg ?? "error";
          rerender();
        } else if (m.t === "x") {
          dim(`\r\n[session exited with code ${m.code ?? 0}]`);
          tab.status = "closed";
          tab.message = `exited (${m.code ?? 0})`;
          rerender();
        }
      };
      ws.onerror = () => {
        if (tab.socket !== ws) return;
        tab.status = "error";
        tab.message = tab.message || "connection error";
        rerender();
      };
      ws.onclose = () => {
        if (tab.socket !== ws) return;
        if (tab.status === "connected" || tab.status === "connecting") {
          tab.status = "closed";
          dim("\r\n[connection closed]");
        }
        rerender();
      };
    },
    [rerender],
  );

  const open = useCallback(
    (ws: string, host: string) => {
      const id = `t${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
      const term = new Terminal({
        fontFamily: '"JetBrains Mono", "SF Mono", ui-monospace, Menlo, Consolas, monospace',
        fontSize: 12.5,
        lineHeight: 1.2,
        cursorBlink: true,
        scrollback: 10000,
        allowProposedApi: true,
        theme: termTheme(),
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      const el = document.createElement("div");
      el.style.width = "100%";
      el.style.height = "100%";
      const tab: TermTab = {
        id,
        ws,
        host,
        status: "connecting",
        message: "",
        term,
        fit,
        el,
        socket: null,
        opened: false,
        created: Date.now(),
      };
      term.onData((d) => {
        if (tab.socket && tab.socket.readyState === WebSocket.OPEN) tab.socket.send(JSON.stringify({ t: "d", d }));
      });
      term.onResize(({ cols, rows }) => {
        if (tab.socket && tab.socket.readyState === WebSocket.OPEN) tab.socket.send(JSON.stringify({ t: "r", c: cols, r: rows }));
      });
      setTabs((l) => [...l, tab]);
      setActiveId(id);
      // connect after the terminal is attached and sized (see attach)
      return id;
    },
    [],
  );

  const attach = useCallback(
    (id: string, parent: HTMLElement | null) => {
      const tab = tabsRef.current.find((t) => t.id === id);
      if (!tab || !parent) return;
      if (tab.el.parentElement !== parent) {
        parent.innerHTML = "";
        parent.appendChild(tab.el);
      }
      if (!tab.opened) {
        tab.term.open(tab.el);
        tab.opened = true;
        try {
          tab.fit.fit();
        } catch {
          /* ignore */
        }
        connect(tab);
      } else {
        requestAnimationFrame(() => {
          try {
            tab.fit.fit();
          } catch {
            /* ignore */
          }
        });
      }
      tab.term.focus();
    },
    [connect],
  );

  const close = useCallback((id: string) => {
    const tab = tabsRef.current.find((t) => t.id === id);
    if (tab) {
      try {
        tab.socket?.close();
      } catch {
        /* ignore */
      }
      tab.socket = null;
      tab.term.dispose();
    }
    setTabs((l) => {
      const next = l.filter((t) => t.id !== id);
      setActiveId((cur) => (cur === id ? (next[next.length - 1]?.id ?? null) : cur));
      return next;
    });
  }, []);

  const reconnect = useCallback(
    (id: string) => {
      const tab = tabsRef.current.find((t) => t.id === id);
      if (!tab) return;
      tab.term.write("\r\n\x1b[2m[reconnecting…]\x1b[0m\r\n");
      connect(tab);
    },
    [connect],
  );

  const fitActive = useCallback(() => {
    const tab = tabsRef.current.find((t) => t.id === activeId);
    if (tab?.opened) {
      try {
        tab.fit.fit();
      } catch {
        /* ignore */
      }
    }
  }, [activeId]);

  useEffect(() => {
    const h = () => fitActive();
    window.addEventListener("resize", h);
    return () => window.removeEventListener("resize", h);
  }, [fitActive]);

  // keep xterm colors in sync with theme changes
  useEffect(() => {
    const obs = new MutationObserver(() => {
      tabsRef.current.forEach((t) => (t.term.options.theme = termTheme()));
    });
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "style"] });
    return () => obs.disconnect();
  }, []);

  const value = useMemo<Ctx>(
    // eslint-disable-next-line react-hooks/exhaustive-deps
    () => ({ tabs: [...tabs], activeId, setActive: setActiveId, open, close, reconnect, attach, fitActive }),
    [tabs, activeId, open, close, reconnect, attach, fitActive, ver],
  );
  return <TCtx.Provider value={value}>{children}</TCtx.Provider>;
}

export function useTerminals(): Ctx {
  const c = useContext(TCtx);
  if (!c) throw new Error("TerminalProvider missing");
  return c;
}
