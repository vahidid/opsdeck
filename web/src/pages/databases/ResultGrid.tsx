import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent } from "react";
import { ArrowDown, ArrowUp, Braces, ChevronDown, ChevronUp, Copy, Download, Maximize2, Rows3, Table2, WrapText, X } from "lucide-react";
import type { DbResult } from "../../api/types";
import { Button, IconButton, SearchInput, Segmented, cx, writeClipboard } from "../../components/ui";
import { MenuItem, Popover, useToast } from "../../components/overlay";
import { useLocalStorage } from "../../hooks";
import CodeEditor from "../ansible/editor/CodeEditor";

const ROW_H = 28;
const INDEX_W = 52;
// a value the server cut (the page gets up to 65,536 characters of one)
const CUT_AT = 65536;

/** The full text of a value: JSON for documents and arrays. */
export function cellText(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

/** A value as the viewer shows it: documents, arrays and JSON text indented. */
function prettyText(v: unknown): { text: string; json: boolean } {
  if (v !== null && typeof v === "object") return { text: JSON.stringify(v, null, 2), json: true };
  if (typeof v === "string") {
    const t = v.trim();
    if ((t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"))) {
      try {
        return { text: JSON.stringify(JSON.parse(t), null, 2), json: true };
      } catch {
        /* not JSON */
      }
    }
  }
  return { text: cellText(v), json: false };
}

function compare(a: unknown, b: unknown): number {
  if (a === b) return 0;
  if (a === null || a === undefined) return 1;
  if (b === null || b === undefined) return -1;
  if (typeof a === "number" && typeof b === "number") return a - b;
  if (typeof a === "boolean" && typeof b === "boolean") return Number(a) - Number(b);
  const x = cellText(a);
  const y = cellText(b);
  // numbers sent as text (numeric, bigint, decimal) sort as numbers
  const nx = Number(x);
  const ny = Number(y);
  if (x !== "" && y !== "" && !Number.isNaN(nx) && !Number.isNaN(ny)) return nx - ny;
  return x.localeCompare(y, undefined, { numeric: true });
}

function initialWidths(cols: { name: string; type: string }[], rows: unknown[][]): number[] {
  return cols.map((c, j) => {
    // the header shows the name and the type
    let n = c.name.length + (c.type ? Math.min(c.type.length, 12) * 0.75 + 2 : 0) + 3;
    for (let i = 0; i < Math.min(rows.length, 60); i++) {
      const t = cellText(rows[i][j]);
      n = Math.max(n, Math.min(t.length, 60));
    }
    return Math.round(Math.min(420, Math.max(72, n * 7.4 + 28)));
  });
}

type Sort = { col: number; desc: boolean } | null;
type Sel = { row: number; col: number } | null;

function csvField(v: unknown) {
  const s = cellText(v);
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

/**
 * ResultGrid shows one result set: virtualized rows, resizable and sortable
 * columns, a filter, wrapped or single-line cells, a JSON view, and a viewer
 * for the whole value of a cell or a row. Keys: arrows move, Enter opens the
 * viewer, Ctrl/⌘-C copies the cell.
 */
export function ResultGrid({ r }: { r: DbResult }) {
  const toast = useToast();
  const cols = useMemo(() => r.columns ?? [], [r.columns]);
  const rows = useMemo(() => r.rows ?? [], [r.rows]);
  const [wrap, setWrap] = useLocalStorage<boolean>("opsdeck.db.wrap", false);
  const [mode, setMode] = useLocalStorage<"table" | "json">("opsdeck.db.view", "table");
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<Sort>(null);
  const [widths, setWidths] = useState<number[]>(() => initialWidths(cols, rows));
  const [sel, setSel] = useState<Sel>(null);
  const [viewer, setViewer] = useState<"value" | "row" | null>(null);
  const [menu, setMenu] = useState<"" | "copy" | "download">("");

  // a new result: new columns
  useEffect(() => {
    setWidths(initialWidths(cols, rows));
    setSort(null);
    setSel(null);
  }, [cols, rows]);

  const view = useMemo(() => {
    let idx = rows.map((_, i) => i);
    const q = filter.trim().toLowerCase();
    if (q) idx = idx.filter((i) => rows[i].some((v) => cellText(v).toLowerCase().includes(q)));
    if (sort) {
      idx = [...idx].sort((a, b) => {
        const c = compare(rows[a][sort.col], rows[b][sort.col]);
        return sort.desc ? -c : c;
      });
    }
    return idx;
  }, [rows, filter, sort]);

  // virtual scrolling (single-line rows have one height)
  const scroller = useRef<HTMLDivElement>(null);
  const [scroll, setScroll] = useState({ top: 0, height: 600 });
  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setScroll((s) => ({ ...s, height: el.clientHeight })));
    ro.observe(el);
    return () => ro.disconnect();
  }, [mode]);
  const WRAP_MAX = 1000;
  const shown = wrap ? view.slice(0, WRAP_MAX) : view;
  const start = wrap ? 0 : Math.max(0, Math.floor(scroll.top / ROW_H) - 10);
  const end = wrap ? shown.length : Math.min(view.length, Math.ceil((scroll.top + scroll.height) / ROW_H) + 10);

  const value = sel ? rows[view[sel.row]]?.[sel.col] : undefined;

  // the selected cell is in view, not under the sticky # column
  useEffect(() => {
    const el = scroller.current;
    if (!el || !sel) return;
    const left = widths.slice(0, sel.col).reduce((a, b) => a + b, 0);
    const w = widths[sel.col] ?? 0;
    if (left < el.scrollLeft) el.scrollLeft = left;
    else if (INDEX_W + left + w > el.scrollLeft + el.clientWidth) el.scrollLeft = Math.min(left, INDEX_W + left + w - el.clientWidth);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sel?.col, sel?.row, viewer]);

  const move = (dr: number, dc: number) => {
    if (!view.length || !cols.length) return;
    const cur = sel ?? { row: 0, col: 0 };
    const next = { row: Math.min(view.length - 1, Math.max(0, cur.row + dr)), col: Math.min(cols.length - 1, Math.max(0, cur.col + dc)) };
    setSel(next);
    const el = scroller.current;
    if (el && !wrap) {
      const top = next.row * ROW_H;
      const header = ROW_H;
      if (top < el.scrollTop) el.scrollTop = top;
      else if (top + ROW_H + header > el.scrollTop + el.clientHeight) el.scrollTop = top + ROW_H + header - el.clientHeight;
    }
  };

  const copy = async (text: string, what: string) => {
    try {
      await writeClipboard(text);
      toast.success("Copied", what);
    } catch {
      toast.error("Cannot copy");
    }
  };

  const onKey = (e: ReactKeyboardEvent) => {
    const k = e.key;
    if (k === "ArrowDown") move(1, 0);
    else if (k === "ArrowUp") move(-1, 0);
    else if (k === "ArrowLeft") move(0, -1);
    else if (k === "ArrowRight") move(0, 1);
    else if (k === "PageDown") move(Math.floor(scroll.height / ROW_H), 0);
    else if (k === "PageUp") move(-Math.floor(scroll.height / ROW_H), 0);
    else if (k === "Enter" && sel) setViewer("value");
    else if (k === "Escape" && viewer) setViewer(null);
    else if ((e.metaKey || e.ctrlKey) && k.toLowerCase() === "c" && sel && !window.getSelection()?.toString()) void copy(cellText(value), cols[sel.col].name);
    else return;
    e.preventDefault();
  };

  const resize = (j: number) => (e: ReactPointerEvent) => {
    e.preventDefault();
    e.stopPropagation();
    const x0 = e.clientX;
    const w0 = widths[j];
    const onMove = (ev: PointerEvent) => setWidths((w) => w.map((x, i) => (i === j ? Math.max(48, w0 + ev.clientX - x0) : x)));
    const onUp = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };
  const fit = (j: number) => {
    let n = cols[j].name.length + 2;
    for (const i of view.slice(0, 500)) n = Math.max(n, Math.min(cellText(rows[i][j]).length, 160));
    setWidths((w) => w.map((x, i) => (i === j ? Math.round(Math.min(1200, Math.max(60, n * 7.4 + 28))) : x)));
  };

  const objects = () => view.map((i) => Object.fromEntries(cols.map((c, j) => [c.name, rows[i][j] ?? null])));
  const formats = {
    tsv: () => [cols.map((c) => c.name).join("\t"), ...view.map((i) => rows[i].map((v) => cellText(v).replace(/[\t\n\r]/g, " ")).join("\t"))].join("\n"),
    csv: () => [cols.map((c) => csvField(c.name)).join(","), ...view.map((i) => rows[i].map(csvField).join(","))].join("\r\n"),
    json: () => JSON.stringify(objects(), null, 2),
  };
  const download = (f: "csv" | "json") => {
    const blob = new Blob([formats[f]()], { type: f === "csv" ? "text/csv" : "application/json" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `result-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.${f}`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    setMenu("");
  };

  const total = widths.reduce((a, b) => a + b, INDEX_W);
  const counted = view.length === rows.length ? `${rows.length} row${rows.length === 1 ? "" : "s"}` : `${view.length} of ${rows.length} rows`;

  return (
    <div className="flex-1 min-h-0 flex flex-col">
      <div className="flex items-center gap-2 px-3 h-9 border-b border-line text-[11.5px] text-muted shrink-0">
        <span className="tabular-nums whitespace-nowrap">
          {counted}
          {r.truncated && (
            <span className="text-warn" title="Raise the row limit or narrow the query">
              {" "}
              · more not shown
            </span>
          )}
        </span>
        <span className="text-faint tabular-nums">{r.durationMs} ms</span>
        {r.note && <span className="text-faint truncate">{r.note}</span>}
        <div className="flex-1" />
        <SearchInput value={filter} onChange={setFilter} placeholder="Filter rows…" className="w-44 h-7" />
        <Segmented
          size="xs"
          value={mode}
          onChange={setMode}
          options={[
            { id: "table", label: <Table2 className="size-3" />, title: "Table" },
            { id: "json", label: <Braces className="size-3" />, title: "JSON" },
          ]}
        />
        {mode === "table" && (
          <IconButton title={wrap ? "Single-line cells" : "Wrap cells"} onClick={() => setWrap(!wrap)} className={cx(wrap && "bg-accent/15 text-fg")}>
            <WrapText className="size-3.5" />
          </IconButton>
        )}
        <div className="relative">
          <Button size="xs" variant="ghost" icon={<Copy className="size-3" />} onClick={() => setMenu(menu === "copy" ? "" : "copy")}>
            Copy
          </Button>
          <Popover open={menu === "copy"} onClose={() => setMenu("")} className="right-0 top-8 w-48">
            <MenuItem onClick={() => (setMenu(""), void copy(formats.tsv(), `${view.length} rows, tab-separated`))} hint="Excel">
              Tab-separated
            </MenuItem>
            <MenuItem onClick={() => (setMenu(""), void copy(formats.csv(), `${view.length} rows as CSV`))}>CSV</MenuItem>
            <MenuItem onClick={() => (setMenu(""), void copy(formats.json(), `${view.length} rows as JSON`))}>JSON</MenuItem>
          </Popover>
        </div>
        <div className="relative">
          <IconButton title="Download" onClick={() => setMenu(menu === "download" ? "" : "download")}>
            <Download className="size-3.5" />
          </IconButton>
          <Popover open={menu === "download"} onClose={() => setMenu("")} className="right-0 top-8 w-44">
            <MenuItem onClick={() => download("csv")}>CSV file</MenuItem>
            <MenuItem onClick={() => download("json")}>JSON file</MenuItem>
          </Popover>
        </div>
      </div>
      <div className="flex-1 min-h-0 flex">
        {mode === "json" ? (
          <div className="flex-1 min-w-0">
            <CodeEditor value={JSON.stringify(objects().slice(0, 2000), null, 2)} language="json" readOnly wrap />
          </div>
        ) : (
          <div
            ref={scroller}
            tabIndex={0}
            onKeyDown={onKey}
            onScroll={(e) => setScroll({ top: e.currentTarget.scrollTop, height: e.currentTarget.clientHeight })}
            className="flex-1 min-w-0 overflow-auto outline-none"
          >
            <table className="text-[12px] font-mono border-separate border-spacing-0" style={{ tableLayout: "fixed", width: total }}>
              <colgroup>
                <col style={{ width: INDEX_W }} />
                {widths.map((w, j) => (
                  <col key={j} style={{ width: w }} />
                ))}
              </colgroup>
              <thead className="sticky top-0 z-20 bg-panel">
                <tr style={{ height: ROW_H }}>
                  <th className="sticky left-0 z-10 bg-panel px-2 text-right text-faint font-normal border-b border-r border-line">#</th>
                  {cols.map((c, j) => (
                    <th
                      key={j}
                      className="relative px-2.5 text-left font-medium border-b border-r border-line cursor-pointer select-none hover:bg-panel-2"
                      title={`${c.name}${c.type ? ` · ${c.type}` : ""} — click to sort`}
                      onClick={() => setSort(sort?.col === j ? (sort.desc ? null : { col: j, desc: true }) : { col: j, desc: false })}
                    >
                      <div className="flex items-center gap-1.5 overflow-hidden">
                        <span className="truncate">{c.name}</span>
                        {c.type && <span className="text-[10px] text-faint font-normal truncate shrink-[2]">{c.type}</span>}
                        {sort?.col === j && (sort.desc ? <ArrowDown className="size-3 shrink-0 text-accent" /> : <ArrowUp className="size-3 shrink-0 text-accent" />)}
                      </div>
                      <span
                        className="absolute right-0 top-0 h-full w-1.5 cursor-col-resize hover:bg-accent/40"
                        onPointerDown={resize(j)}
                        onClick={(e) => e.stopPropagation()}
                        onDoubleClick={(e) => (e.stopPropagation(), fit(j))}
                        title="Drag to resize, double-click to fit"
                      />
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {start > 0 && <tr style={{ height: start * ROW_H }} />}
                {shown.slice(start, end).map((ri, k) => {
                  const vr = start + k;
                  const row = rows[ri];
                  const selected = sel?.row === vr;
                  return (
                    <tr key={ri} style={wrap ? undefined : { height: ROW_H }} className={cx(selected ? "bg-accent/[0.07]" : "hover:bg-panel-2/60")}>
                      <td
                        className={cx("sticky left-0 z-[1] px-2 text-right text-faint border-b border-r border-line/60 tabular-nums align-top cursor-pointer", selected ? "bg-panel-2" : "bg-bg")}
                        style={{ lineHeight: `${ROW_H - 1}px` }}
                        onClick={() => (setSel({ row: vr, col: sel?.col ?? 0 }), setViewer("row"))}
                        title="Show the row"
                      >
                        {ri + 1}
                      </td>
                      {cols.map((_, j) => (
                        <td
                          key={j}
                          onClick={() => setSel({ row: vr, col: j })}
                          onDoubleClick={() => (setSel({ row: vr, col: j }), setViewer("value"))}
                          className={cx(
                            "px-2.5 border-b border-r border-line/60 align-top",
                            wrap ? "py-1" : "overflow-hidden whitespace-nowrap text-ellipsis",
                            selected && sel?.col === j && "outline outline-1 -outline-offset-1 outline-accent bg-accent/10",
                          )}
                          style={wrap ? undefined : { lineHeight: `${ROW_H - 1}px` }}
                        >
                          <Value v={row[j]} wrap={wrap} />
                        </td>
                      ))}
                    </tr>
                  );
                })}
                {!wrap && end < view.length && <tr style={{ height: (view.length - end) * ROW_H }} />}
              </tbody>
            </table>
            {wrap && view.length > WRAP_MAX && (
              <div className="px-3 py-2 text-[12px] text-faint">
                The first {WRAP_MAX} rows are shown wrapped. Turn wrapping off to scroll through all {view.length}.
              </div>
            )}
            {view.length === 0 && <div className="px-3 py-6 text-[12.5px] text-faint">{rows.length ? "No row matches the filter." : "No rows."}</div>}
          </div>
        )}
        {viewer && sel && mode === "table" && view[sel.row] !== undefined && (
          <Viewer
            tab={viewer}
            onTab={setViewer}
            cols={cols}
            row={rows[view[sel.row]]}
            rowNumber={view[sel.row] + 1}
            col={sel.col}
            onCol={(col) => (setSel({ ...sel, col }), setViewer("value"))}
            onMove={(d) => move(d, 0)}
            onClose={() => (setViewer(null), scroller.current?.focus())}
            copy={copy}
          />
        )}
      </div>
    </div>
  );
}

/** One cell: NULL, numbers, booleans, documents as compact JSON, text with ⏎ for line breaks. */
function Value({ v, wrap }: { v: unknown; wrap: boolean }) {
  if (v === null || v === undefined) return <span className="text-faint italic">NULL</span>;
  if (typeof v === "boolean") return <span className="text-info">{String(v)}</span>;
  if (typeof v === "number") return <span className="text-warn">{v}</span>;
  if (typeof v === "object") {
    const s = JSON.stringify(v);
    const label = Array.isArray(v) ? `[${v.length}]` : "{…}";
    return (
      <span className={cx("text-muted", wrap && "whitespace-pre-wrap break-all line-clamp-[12]")}>
        <span className="mr-1 rounded bg-panel-3 px-1 text-[10px] text-faint">{label}</span>
        {wrap ? s.slice(0, 4000) : s.slice(0, 400)}
      </span>
    );
  }
  const s = String(v);
  if (wrap) return <span className="whitespace-pre-wrap break-words line-clamp-[12]">{s.slice(0, 4000)}</span>;
  const head = s.slice(0, 400);
  if (!head.includes("\n")) return <>{head}</>;
  const parts = head.split(/\r?\n/);
  return (
    <>
      {parts.map((p, i) => (
        <span key={i}>
          {i > 0 && <span className="text-faint mx-0.5">⏎</span>}
          {p}
        </span>
      ))}
    </>
  );
}

/** Viewer shows the whole value of a cell, or every column of a row. */
function Viewer({
  tab,
  onTab,
  cols,
  row,
  rowNumber,
  col,
  onCol,
  onMove,
  onClose,
  copy,
}: {
  tab: "value" | "row";
  onTab: (t: "value" | "row") => void;
  cols: { name: string; type: string }[];
  row: unknown[];
  rowNumber: number;
  col: number;
  onCol: (j: number) => void;
  onMove: (d: number) => void;
  onClose: () => void;
  copy: (text: string, what: string) => Promise<void>;
}) {
  const [width, setWidth] = useLocalStorage<number>("opsdeck.db.viewerWidth", 440);
  const [format, setFormat] = useState(true);
  const [wrapText, setWrapText] = useState(true);
  const v = row[col];
  const pretty = prettyText(v);
  const raw = cellText(v);
  const text = format && pretty.json ? pretty.text : raw;
  const cut = typeof v === "string" && v.length >= CUT_AT && v.endsWith("…");
  const drag = (e: ReactPointerEvent) => {
    e.preventDefault();
    const x0 = e.clientX;
    const w0 = width;
    const onMove = (ev: PointerEvent) => setWidth(Math.min(1100, Math.max(300, w0 - (ev.clientX - x0))));
    const onUp = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };
  return (
    <aside
      className="relative shrink-0 border-l border-line bg-panel flex flex-col min-h-0"
      style={{ width }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onClose();
        }
      }}
    >
      <span className="absolute -left-0.5 top-0 h-full w-1.5 cursor-col-resize hover:bg-accent/40 z-10" onPointerDown={drag} />
      <div className="flex items-center gap-1 px-2 h-9 border-b border-line shrink-0">
        <Segmented
          size="xs"
          value={tab}
          onChange={onTab}
          options={[
            { id: "value", label: <><Maximize2 className="size-3" /> Value</> },
            { id: "row", label: <><Rows3 className="size-3" /> Row</> },
          ]}
        />
        <span className="ml-1 text-[11.5px] text-faint tabular-nums">row {rowNumber}</span>
        <div className="flex-1" />
        <IconButton title="Previous row (↑)" onClick={() => onMove(-1)}>
          <ChevronUp className="size-3.5" />
        </IconButton>
        <IconButton title="Next row (↓)" onClick={() => onMove(1)}>
          <ChevronDown className="size-3.5" />
        </IconButton>
        <IconButton title="Close (Esc)" onClick={onClose}>
          <X className="size-3.5" />
        </IconButton>
      </div>
      {tab === "value" ? (
        <>
          <div className="flex items-center gap-2 px-3 h-9 border-b border-line text-[11.5px] shrink-0">
            <span className="font-mono text-fg truncate">{cols[col]?.name}</span>
            {cols[col]?.type && <span className="text-faint">{cols[col].type}</span>}
            <span className="text-faint tabular-nums whitespace-nowrap">{v === null || v === undefined ? "NULL" : `${raw.length.toLocaleString()} chars`}</span>
            <div className="flex-1" />
            {pretty.json && (
              <IconButton title={format ? "Show as stored" : "Format JSON"} onClick={() => setFormat(!format)} className={cx(format && "bg-accent/15 text-fg")}>
                <Braces className="size-3.5" />
              </IconButton>
            )}
            <IconButton title={wrapText ? "Do not wrap" : "Wrap lines"} onClick={() => setWrapText(!wrapText)} className={cx(wrapText && "bg-accent/15 text-fg")}>
              <WrapText className="size-3.5" />
            </IconButton>
            <IconButton title="Copy the value" onClick={() => void copy(text, cols[col]?.name ?? "value")}>
              <Copy className="size-3.5" />
            </IconButton>
          </div>
          {cut && <div className="px-3 py-1.5 text-[11.5px] text-warn border-b border-line">The value is longer than {CUT_AT.toLocaleString()} characters: the rest is not shown. Select a part of it (substring, $substr) to read further.</div>}
          <div className="flex-1 min-h-0">
            {v === null || v === undefined ? (
              <div className="p-4 text-faint italic text-[12.5px]">NULL</div>
            ) : (
              <CodeEditor key={`${rowNumber}/${col}`} value={text} language={format && pretty.json ? "json" : "text"} readOnly wrap={wrapText} />
            )}
          </div>
        </>
      ) : (
        <>
          <div className="flex items-center gap-2 px-3 h-9 border-b border-line text-[11.5px] shrink-0">
            <span className="text-muted">{cols.length} columns</span>
            <div className="flex-1" />
            <Button
              size="xs"
              variant="ghost"
              icon={<Copy className="size-3" />}
              onClick={() => void copy(JSON.stringify(Object.fromEntries(cols.map((c, j) => [c.name, row[j] ?? null])), null, 2), `row ${rowNumber} as JSON`)}
            >
              JSON
            </Button>
          </div>
          <div className="flex-1 min-h-0 overflow-y-auto">
            {cols.map((c, j) => {
              const x = row[j];
              const t = prettyText(x);
              return (
                <button key={j} onClick={() => onCol(j)} className={cx("block w-full text-left px-3 py-2 border-b border-line/60 hover:bg-panel-2", j === col && "bg-accent/[0.07]")} title="Open the whole value">
                  <div className="flex items-center gap-1.5 text-[11.5px]">
                    <span className="font-mono text-fg">{c.name}</span>
                    {c.type && <span className="text-faint">{c.type}</span>}
                  </div>
                  <div className="mt-0.5 font-mono text-[12px] text-muted whitespace-pre-wrap break-words line-clamp-6">
                    {x === null || x === undefined ? <span className="text-faint italic">NULL</span> : t.text.slice(0, 1500)}
                  </div>
                </button>
              );
            })}
          </div>
        </>
      )}
    </aside>
  );
}
