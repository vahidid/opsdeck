import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { RotateCw } from "lucide-react";
import { termTheme } from "../context/terminals";
import { Button, Input, Select, StatusDot } from "./ui";

type Status = "connecting" | "connected" | "closed" | "error";

// ExecTerminal is an interactive terminal over a PTY WebSocket (docker exec
// into a container). url(cols, rows, opts) builds the socket URL.
export function ExecTerminal({
  url,
  className,
  options = true,
}: {
  url: (cols: number, rows: number, o: { user: string; shell: string }) => string;
  className?: string;
  options?: boolean;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<Status>("connecting");
  const [message, setMessage] = useState("");
  const [user, setUser] = useState("");
  const [shell, setShell] = useState("auto");
  const [gen, setGen] = useState(0);
  const urlRef = useRef(url);
  urlRef.current = url;

  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const term = new Terminal({
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: 12.5,
      cursorBlink: true,
      scrollback: 5000,
      theme: termTheme(),
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    try {
      fit.fit();
    } catch {
      /* not visible yet */
    }
    setStatus("connecting");
    setMessage("");
    const ws = new WebSocket(urlRef.current(term.cols || 100, term.rows || 30, { user, shell }));
    const dim = (s: string) => term.write(`\x1b[2m${s}\x1b[0m\r\n`);
    ws.onopen = () => {
      setStatus("connected");
      ws.send(JSON.stringify({ t: "r", c: term.cols, r: term.rows }));
      term.focus();
    };
    ws.onmessage = (ev) => {
      let m: { t: string; d?: string; code?: number; msg?: string };
      try {
        m = JSON.parse(String(ev.data));
      } catch {
        return;
      }
      if (m.t === "d" && m.d !== undefined) term.write(m.d);
      else if (m.t === "s") dim(`▸ ${m.msg ?? ""}`);
      else if (m.t === "e") {
        term.write(`\r\n\x1b[31m✖ ${m.msg ?? "error"}\x1b[0m\r\n`);
        setStatus("error");
        setMessage(m.msg ?? "error");
      } else if (m.t === "x") {
        dim(`\r\n[exited with code ${m.code ?? 0}]`);
        setStatus("closed");
        setMessage(`exited (${m.code ?? 0})`);
      }
    };
    ws.onclose = () => setStatus((s) => (s === "connected" || s === "connecting" ? "closed" : s));
    ws.onerror = () => setStatus("error");
    const input = term.onData((d) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify({ t: "d", d })));
    const resize = term.onResize(({ cols, rows }) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify({ t: "r", c: cols, r: rows })));
    const ro = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        /* hidden */
      }
    });
    ro.observe(el);
    return () => {
      ro.disconnect();
      input.dispose();
      resize.dispose();
      ws.close();
      term.dispose();
    };
    // user/shell apply on reconnect
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [gen]);

  const tone = status === "connected" ? "ok" : status === "connecting" ? "warn" : status === "error" ? "err" : "neutral";
  return (
    <div className={`flex flex-col min-h-0 rounded-lg border border-line bg-panel overflow-hidden ${className ?? ""}`}>
      <div className="flex items-center gap-2 px-2 h-10 border-b border-line shrink-0">
        <StatusDot tone={tone} pulse={status === "connected"} />
        <span className="text-xs text-muted">{message || status}</span>
        <div className="flex-1" />
        {options && (
          <>
            <Input mono className="w-28 !h-7" placeholder="user (default)" value={user} onChange={(e) => setUser(e.target.value)} title="Run as this user (docker exec -u)" />
            <Select className="w-24 !h-7 text-xs" value={shell} onChange={(e) => setShell(e.target.value)} title="Shell">
              <option value="auto">auto</option>
              <option value="bash">bash</option>
              <option value="sh">sh</option>
              <option value="ash">ash</option>
            </Select>
          </>
        )}
        <Button size="sm" variant="ghost" icon={<RotateCw className="size-3.5" />} onClick={() => setGen((g) => g + 1)}>
          {status === "connected" ? "Reconnect" : "Connect"}
        </Button>
      </div>
      <div ref={hostRef} className="flex-1 min-h-0 p-1.5" dir="ltr" />
    </div>
  );
}
