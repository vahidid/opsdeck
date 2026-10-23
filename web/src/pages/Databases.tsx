import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { AlertTriangle, ChevronDown, ChevronRight, Copy, Database, Eye, Info, Play, RefreshCw, ShieldAlert, Table2 } from "lucide-react";
import { ApiError, api, errMsg } from "../api/client";
import type { DbAnswer, DbNotRead, DbResult, DbTable, Resource } from "../api/types";
import { useWs } from "../context/app";
import { useTabTitle } from "../context/tabs";
import { Badge, Button, EmptyState, ErrorBox, IconButton, Input, Kbd, Loading, PageHeader, SearchInput, Select, cx, riskTone } from "../components/ui";
import { Drawer, Modal, useToast } from "../components/overlay";
import { useAsync, useLocalStorage } from "../hooks";
import CodeEditor from "./ansible/editor/CodeEditor";

const ENGINES = new Set(["postgres", "postgresql", "mysql", "mariadb"]);
const engineOf = (r?: Resource) => String(r?.spec.engine ?? "").toLowerCase();
const isPg = (r?: Resource) => engineOf(r).startsWith("postgres");

// Databases of the workspace (PostgreSQL, MySQL, MariaDB): tables and views,
// their structure, and a SQL editor. Reads run in a read-only transaction;
// a script that is not a read is shown for confirmation first and runs as
// one transaction.
export function DatabasesPage() {
  const { name: ws, byKind } = useWs();
  const all = byKind("Database");
  const [params, setParams] = useSearchParams();
  const [saved, setSaved] = useLocalStorage<string>(`opsdeck.db.${ws}`, "");
  const db = params.get("db") || (all.some((d) => d.name === saved) ? saved : all[0]?.name) || "";
  const res = all.find((d) => d.name === db);
  const database = params.get("database") || "";
  const set = useCallback(
    (patch: Record<string, string | null>) =>
      setParams(
        (p) => {
          const n = new URLSearchParams(p);
          for (const [k, v] of Object.entries(patch)) {
            if (v) n.set(k, v);
            else n.delete(k);
          }
          return n;
        },
        { replace: true },
      ),
    [setParams],
  );
  useEffect(() => {
    if (db) setSaved(db);
  }, [db, setSaved]);
  useTabTitle(db ? `${database || db} · DB` : "Databases");

  const supported = !!res && ENGINES.has(engineOf(res));
  const info = useAsync(() => (supported ? api.dbInfo(ws, db, database || undefined) : Promise.resolve(undefined)), [ws, db, database, supported]);
  const dbs = useAsync(() => (supported ? api.dbDatabases(ws, db) : Promise.resolve([] as string[])), [ws, db, supported]);
  const tables = useAsync(() => (supported ? api.dbTables(ws, db, database || undefined) : Promise.resolve([] as DbTable[])), [ws, db, database, supported]);

  if (!all.length) {
    return (
      <div className="flex flex-col h-full">
        <PageHeader icon={<Database />} title="Databases" />
        <EmptyState icon={<Database />} title="No databases in this workspace">
          Add a <span className="font-mono">Database</span> resource (PostgreSQL, MySQL or MariaDB) under Resources, with its login stored in the vault. opsdeck reaches it through the workspace&apos;s VPNs
          and jump hosts, or through an SSH tunnel to the host it runs on.
        </EmptyState>
      </div>
    );
  }

  const current = info.data?.info.database || database;
  const version = shortVersion(info.data?.info.version);
  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<Database />}
        title="Databases"
        subtitle={
          info.error ? (
            <span className="text-err">{info.error}</span>
          ) : info.data ? (
            <span className="inline-flex flex-wrap items-center gap-1.5">
              {version}
              <span className="text-faint">·</span>
              <span className="font-mono">
                {info.data.info.user}@{info.data.info.address}
              </span>
              {info.data.path.length > 0 && <span className="text-faint">via {info.data.path.join(" → ")}</span>}
              {info.data.environment && <Badge tone={info.data.protected ? "err" : "neutral"}>{info.data.environment + (info.data.protected ? " · protected" : "")}</Badge>}
            </span>
          ) : (
            "Tables, structure and SQL — reads run in a read-only transaction"
          )
        }
        actions={
          <>
            {supported && (
              <Select value={current} onChange={(e) => set({ database: e.target.value })} className="w-44" title="Database on the server">
                {current && !(dbs.data ?? []).includes(current) && <option value={current}>{current}</option>}
                {(dbs.data ?? []).map((d) => (
                  <option key={d} value={d}>
                    {d}
                  </option>
                ))}
              </Select>
            )}
            <Select value={db} onChange={(e) => set({ db: e.target.value, database: null })} className="w-48" title="Database resource">
              {all.map((d) => (
                <option key={d.name} value={d.name}>
                  {d.name} ({String(d.spec.engine || "?")})
                </option>
              ))}
            </Select>
          </>
        }
      />
      {!supported ? (
        <EmptyState icon={<Database />} title={`${String(res?.spec.engine || "This engine")} is not supported yet`}>
          The Databases page opens PostgreSQL, MySQL and MariaDB databases. The agent can still work with this one through <span className="font-mono">exec</span> on its host.
        </EmptyState>
      ) : (
        <Workbench key={`${db}/${database}`} ws={ws} db={db} res={res!} database={database || undefined} tables={tables.data} tablesLoading={tables.loading} tablesError={tables.error} reloadTables={tables.reload} connError={info.error} />
      )}
    </div>
  );
}

function shortVersion(v?: string) {
  if (!v) return "";
  if (/mariadb/i.test(v)) return `MariaDB ${v.split("-")[0]}`;
  if (/^postgresql/i.test(v)) return v.split(" on ")[0].split(",")[0];
  return `MySQL ${v.split("-")[0]}`;
}

type HistoryItem = { sql: string; at: number };

function Workbench({
  ws,
  db,
  res,
  database,
  tables,
  tablesLoading,
  tablesError,
  reloadTables,
  connError,
}: {
  ws: string;
  db: string;
  res: Resource;
  database?: string;
  tables?: DbTable[];
  tablesLoading: boolean;
  tablesError: string | null;
  reloadTables: () => void;
  connError: string | null;
}) {
  const pg = isPg(res);
  const [sql, setSql] = useLocalStorage<string>(`opsdeck.db.sql.${ws}.${db}`, "select now();");
  const [history, setHistory] = useLocalStorage<HistoryItem[]>(`opsdeck.db.history.${ws}.${db}`, []);
  const [maxRows, setMaxRows] = useLocalStorage<number>("opsdeck.db.maxRows", 500);
  const [answer, setAnswer] = useState<DbAnswer | null>(null);
  const [failure, setFailure] = useState("");
  const [running, setRunning] = useState(false);
  const [confirm, setConfirm] = useState<(DbNotRead & { sql: string }) | null>(null);
  const [structure, setStructure] = useState<DbTable | null>(null);
  const [risk, setRisk] = useState<DbNotRead | null>(null);
  const toast = useToast();
  const sqlRef = useRef(sql);
  sqlRef.current = sql;

  // what the script is, as the user types (debounced)
  useEffect(() => {
    if (!sql.trim()) {
      setRisk(null);
      return;
    }
    const t = setTimeout(() => {
      api
        .dbClassify(ws, db, sql)
        .then(setRisk)
        .catch(() => setRisk(null));
    }, 350);
    return () => clearTimeout(t);
  }, [ws, db, sql]);

  const run = async (text: string, write = false) => {
    if (!text.trim() || running) return;
    setRunning(true);
    setFailure("");
    try {
      const a = await api.dbQuery(ws, db, { sql: text, database, write, maxRows });
      setAnswer(a);
      setHistory([{ sql: text, at: Date.now() }, ...history.filter((h) => h.sql !== text)].slice(0, 30));
      if (write && a.committed) {
        toast.success("Committed", `${a.results.length} statement${a.results.length === 1 ? "" : "s"}`);
        reloadTables();
      }
    } catch (e) {
      if (e instanceof ApiError && e.code === "not_read") {
        setConfirm({ ...(e.data as unknown as DbNotRead), sql: text });
      } else {
        setFailure(errMsg(e));
      }
    } finally {
      setRunning(false);
    }
  };

  const qualified = (t: DbTable) => {
    const q = (s: string) => (pg ? (/^[a-z_][a-z0-9_$]*$/.test(s) ? s : `"${s.replace(/"/g, '""')}"`) : /^[A-Za-z_][A-Za-z0-9_$]*$/.test(s) ? s : `\`${s.replace(/`/g, "``")}\``);
    return pg && t.schema !== "public" ? `${q(t.schema)}.${q(t.name)}` : q(t.name);
  };
  const openTable = (t: DbTable) => {
    const text = `select * from ${qualified(t)} limit 100;`;
    setSql(text);
    void run(text);
  };
  const words = useMemo(() => (tables ?? []).flatMap((t) => (pg && t.schema !== "public" ? [t.name, `${t.schema}.${t.name}`] : [t.name])), [tables, pg]);

  return (
    <div className="flex flex-1 min-h-0">
      <TableList tables={tables} loading={tablesLoading} error={tablesError} onReload={reloadTables} onOpen={openTable} onStructure={setStructure} grouped={pg} />
      <main className="flex-1 min-w-0 flex flex-col">
        <div className="h-[38%] min-h-44 flex flex-col border-b border-line">
          <div className="flex items-center gap-2 px-3 h-10 border-b border-line bg-panel shrink-0">
            <Button size="sm" variant="primary" icon={<Play className="size-3.5" />} onClick={() => void run(sqlRef.current)} loading={running} disabled={!sql.trim()}>
              Run
            </Button>
            <Kbd>Ctrl ↵</Kbd>
            {risk && (
              <Badge tone={riskTone(risk.risk)} title={risk.reasons.join("\n")}>
                {risk.risk === "read" ? "read · read-only transaction" : `${risk.risk} · asks before running`}
              </Badge>
            )}
            <div className="flex-1" />
            <HistoryPicker items={history} onPick={setSql} />
            <Select value={String(maxRows)} onChange={(e) => setMaxRows(Number(e.target.value))} className="h-7 w-32 text-xs" title="Rows shown per result">
              {[100, 500, 1000, 5000].map((n) => (
                <option key={n} value={n}>
                  {n} rows
                </option>
              ))}
            </Select>
          </div>
          <div className="flex-1 min-h-0">
            <CodeEditor value={sql} onChange={setSql} language={pg ? "pgsql" : "mysql"} onRun={() => void run(sqlRef.current)} words={words} autoFocus />
          </div>
        </div>
        <div className="flex-1 min-h-0 flex flex-col">
          {connError && !answer && (
            <div className="p-4">
              <ErrorBox error={connError} />
            </div>
          )}
          {failure && (
            <div className="p-4">
              <ErrorBox error={failure} />
            </div>
          )}
          {answer ? (
            <Results answer={answer} />
          ) : (
            !failure &&
            !connError && (
              <EmptyState icon={<Table2 />} title="Run a query" className="flex-1">
                Click a table to see its first rows, or write SQL above and press <Kbd>Ctrl ↵</Kbd>. Statements that change data are shown to you before they run.
              </EmptyState>
            )
          )}
        </div>
      </main>
      {confirm && (
        <ConfirmChange
          db={db}
          database={answer?.database || database || String(res.spec.database || "")}
          c={confirm}
          onCancel={() => setConfirm(null)}
          onRun={() => {
            const text = confirm.sql;
            setConfirm(null);
            void run(text, true);
          }}
        />
      )}
      {structure && <StructureDrawer ws={ws} db={db} database={database} t={structure} onClose={() => setStructure(null)} />}
    </div>
  );
}

function TableList({
  tables,
  loading,
  error,
  onReload,
  onOpen,
  onStructure,
  grouped,
}: {
  tables?: DbTable[];
  loading: boolean;
  error: string | null;
  onReload: () => void;
  onOpen: (t: DbTable) => void;
  onStructure: (t: DbTable) => void;
  grouped: boolean;
}) {
  const [q, setQ] = useState("");
  const [closed, setClosed] = useState<Record<string, boolean>>({});
  const groups = useMemo(() => {
    const m = new Map<string, DbTable[]>();
    for (const t of tables ?? []) {
      if (q && !`${t.schema}.${t.name}`.toLowerCase().includes(q.toLowerCase())) continue;
      const k = grouped ? t.schema : "";
      if (!m.has(k)) m.set(k, []);
      m.get(k)!.push(t);
    }
    return [...m.entries()];
  }, [tables, q, grouped]);
  return (
    <aside className="w-64 shrink-0 border-r border-line flex flex-col min-h-0">
      <div className="p-2 flex items-center gap-1 border-b border-line">
        <SearchInput value={q} onChange={setQ} placeholder="Tables…" className="flex-1" />
        <IconButton title="Reload tables" onClick={onReload}>
          <RefreshCw className={cx("size-3.5", loading && "animate-spin")} />
        </IconButton>
      </div>
      <div className="flex-1 overflow-y-auto px-2 pb-3">
        {error && (
          <div className="pt-2">
            <ErrorBox error={error} />
          </div>
        )}
        {loading && !tables && <Loading />}
        {tables && tables.length === 0 && <div className="text-[12px] text-faint p-2">No tables in this database.</div>}
        {groups.map(([schema, ts]) => {
          const open = q ? true : !closed[schema];
          return (
            <div key={schema} className="mt-2">
              {grouped && (
                <button className="w-full flex items-center gap-1 px-1 text-[10.5px] uppercase tracking-wide text-faint hover:text-muted" onClick={() => setClosed({ ...closed, [schema]: open })}>
                  {open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
                  {schema}
                  <span className="ml-auto normal-case tracking-normal">{ts.length}</span>
                </button>
              )}
              {open && (
                <div className="mt-0.5 space-y-px">
                  {ts.map((t) => (
                    <div key={t.schema + "." + t.name} className="group flex items-center rounded-md hover:bg-panel-2">
                      <button
                        className="flex-1 min-w-0 flex items-center gap-2 px-2 h-7 text-[12.5px] text-left text-muted group-hover:text-fg"
                        onClick={() => onOpen(t)}
                        title={`${t.type} · click for the first rows`}
                      >
                        {t.type.includes("view") ? <Eye className="size-3.5 shrink-0 text-info" /> : <Table2 className="size-3.5 shrink-0" />}
                        <span className="truncate font-mono">{t.name}</span>
                        <span className="ml-auto text-[10.5px] text-faint tabular-nums">{t.rows >= 0 ? compact(t.rows) : ""}</span>
                      </button>
                      <IconButton title="Columns and indexes" onClick={() => onStructure(t)} className="opacity-0 group-hover:opacity-100">
                        <Info className="size-3.5" />
                      </IconButton>
                    </div>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </aside>
  );
}

function compact(n: number) {
  if (n < 1000) return String(n);
  if (n < 1e6) return `${(n / 1e3).toFixed(n < 1e4 ? 1 : 0)}k`;
  if (n < 1e9) return `${(n / 1e6).toFixed(n < 1e7 ? 1 : 0)}M`;
  return `${(n / 1e9).toFixed(1)}B`;
}

function Results({ answer }: { answer: DbAnswer }) {
  const [i, setI] = useState(0);
  useEffect(() => setI(Math.max(0, answer.results.length - 1)), [answer]);
  const r = answer.results[Math.min(i, answer.results.length - 1)];
  return (
    <div className="flex-1 min-h-0 flex flex-col">
      {answer.results.length > 1 && (
        <div className="flex items-center gap-1 px-2 h-9 border-b border-line overflow-x-auto shrink-0">
          {answer.results.map((x, j) => (
            <button
              key={j}
              onClick={() => setI(j)}
              className={cx("h-7 px-2.5 rounded-md text-[12px] whitespace-nowrap max-w-64 truncate font-mono", j === i ? "bg-accent/15 text-fg" : "text-muted hover:bg-panel-2", x.error && "text-err")}
              title={x.statement}
            >
              {j + 1}. {x.statement.replace(/\s+/g, " ").slice(0, 40)}
            </button>
          ))}
        </div>
      )}
      {r && <ResultView r={r} />}
      <div className="flex items-center gap-3 px-3 h-8 border-t border-line text-[11.5px] text-faint shrink-0">
        {answer.error ? (
          <span className="text-err flex items-center gap-1">
            <AlertTriangle className="size-3.5" />
            {answer.committed ? "Failed" : answer.results.length && answer.risk !== "read" ? "Failed — nothing was committed" : "Failed"}
          </span>
        ) : answer.committed ? (
          <span className="text-ok">Committed</span>
        ) : (
          <span>Read-only transaction</span>
        )}
        <span>{answer.database}</span>
        <span className="ml-auto tabular-nums">{answer.durationMs} ms</span>
      </div>
    </div>
  );
}

function ResultView({ r }: { r: DbResult }) {
  const toast = useToast();
  if (r.error) {
    return (
      <div className="p-4">
        <ErrorBox error={r.error} />
      </div>
    );
  }
  if (!r.columns) {
    return (
      <div className="flex-1 grid place-items-center text-muted text-[13px]">
        {r.rowsAffected >= 0 ? `${r.rowsAffected} row${r.rowsAffected === 1 ? "" : "s"} affected` : "Done"} · {r.durationMs} ms
      </div>
    );
  }
  const rows = r.rows ?? [];
  const copy = async () => {
    const esc = (v: unknown) => (v === null || v === undefined ? "" : String(v).replace(/[\t\n]/g, " "));
    const text = [r.columns!.map((c) => c.name).join("\t"), ...rows.map((row) => row.map(esc).join("\t"))].join("\n");
    try {
      await navigator.clipboard.writeText(text);
      toast.success("Copied", `${rows.length} rows as tab-separated text`);
    } catch {
      toast.error("Cannot copy");
    }
  };
  return (
    <div className="flex-1 min-h-0 flex flex-col">
      <div className="flex items-center gap-2 px-3 h-8 border-b border-line text-[11.5px] text-muted shrink-0">
        <span className="tabular-nums">
          {rows.length} row{rows.length === 1 ? "" : "s"}
          {r.truncated && <span className="text-warn"> · more rows not shown (raise the limit or narrow the query)</span>}
        </span>
        <span className="text-faint tabular-nums">{r.durationMs} ms</span>
        <div className="flex-1" />
        <Button size="xs" variant="ghost" icon={<Copy className="size-3" />} onClick={() => void copy()}>
          Copy
        </Button>
      </div>
      <div className="flex-1 min-h-0 overflow-auto">
        <table className="text-[12px] font-mono border-separate border-spacing-0">
          <thead className="sticky top-0 z-10 bg-panel">
            <tr>
              <th className="sticky left-0 bg-panel px-2 h-7 text-right text-faint font-normal border-b border-r border-line">#</th>
              {r.columns.map((c, j) => (
                <th key={j} className="px-2.5 h-7 text-left font-medium whitespace-nowrap border-b border-r border-line" title={c.type}>
                  {c.name}
                  <span className="ml-1.5 text-[10px] text-faint font-normal">{c.type}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, k) => (
              <tr key={k} className="hover:bg-panel-2/60">
                <td className="sticky left-0 bg-bg px-2 text-right text-faint border-b border-r border-line/60 tabular-nums">{k + 1}</td>
                {row.map((v, j) => (
                  <td key={j} className="px-2.5 py-1 border-b border-r border-line/60 whitespace-nowrap max-w-[28rem] truncate align-top" title={v === null ? "NULL" : String(v)}>
                    {v === null ? <span className="text-faint italic">NULL</span> : typeof v === "boolean" ? <span className="text-info">{String(v)}</span> : typeof v === "number" ? <span className="text-warn">{v}</span> : String(v)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function HistoryPicker({ items, onPick }: { items: HistoryItem[]; onPick: (sql: string) => void }) {
  if (!items.length) return null;
  return (
    <Select
      value=""
      onChange={(e) => {
        if (e.target.value !== "") onPick(items[Number(e.target.value)].sql);
      }}
      className="h-7 w-40 text-xs"
      title="Recent queries"
    >
      <option value="">History…</option>
      {items.map((h, i) => (
        <option key={i} value={i}>
          {h.sql.replace(/\s+/g, " ").slice(0, 60)}
        </option>
      ))}
    </Select>
  );
}

function ConfirmChange({ db, database, c, onCancel, onRun }: { db: string; database: string; c: DbNotRead & { sql: string }; onCancel: () => void; onRun: () => void }) {
  // a destructive script on a protected environment: type the database's name
  const typed = c.risk === "destructive" && c.protected;
  const [name, setName] = useState("");
  return (
    <Modal
      open
      onClose={onCancel}
      width={620}
      icon={<ShieldAlert />}
      title={`Run this ${c.risk === "destructive" ? "destructive change" : "change"}?`}
      footer={
        <>
          <Button onClick={onCancel}>Cancel</Button>
          <Button variant={c.risk === "destructive" ? "danger" : "primary"} onClick={onRun} disabled={typed && name !== db} icon={<Play className="size-3.5" />}>
            Run {c.statements > 1 ? `${c.statements} statements` : "it"}
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-[13px]">
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge tone={riskTone(c.risk)}>{c.risk}</Badge>
          <span className="font-mono">{db}</span>
          {database && <span className="text-muted">database {database}</span>}
          {c.environment && <Badge tone={c.protected ? "err" : "neutral"}>{c.environment + (c.protected ? " · protected" : "")}</Badge>}
        </div>
        {c.reasons.length > 0 && (
          <ul className="list-disc pl-5 text-muted space-y-0.5">
            {c.reasons.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
        )}
        <pre className="max-h-64 overflow-auto rounded-md border border-line bg-panel-2 p-2.5 text-[12px] font-mono whitespace-pre-wrap">{c.sql}</pre>
        <div className="text-muted text-[12px] flex items-start gap-1.5">
          <Info className="size-3.5 mt-0.5 shrink-0" />
          It runs as one transaction: if a statement fails, nothing is committed. It is recorded in the activity journal.
        </div>
        {typed && (
          <label className="block">
            <span className="text-[12px] text-muted">
              Type <span className="font-mono text-fg">{db}</span> to confirm
            </span>
            <Input mono value={name} onChange={(e) => setName(e.target.value)} className="mt-1" autoFocus />
          </label>
        )}
      </div>
    </Modal>
  );
}

function StructureDrawer({ ws, db, database, t, onClose }: { ws: string; db: string; database?: string; t: DbTable; onClose: () => void }) {
  const ti = useAsync(() => api.dbTable(ws, db, t.schema, t.name, database), [ws, db, database, t.schema, t.name]);
  return (
    <Drawer open onClose={onClose} width={620} title={<span className="font-mono">{t.name}</span>} subtitle={`${t.type} · ${t.schema}${t.rows >= 0 ? ` · ~${compact(t.rows)} rows` : ""}`}>
      {ti.loading && <Loading />}
      {ti.error && <ErrorBox error={ti.error} />}
      {ti.data && (
        <div className="space-y-5">
          <table className="w-full text-[12.5px]">
            <thead>
              <tr className="text-left text-faint text-[11px] uppercase tracking-wide">
                <th className="py-1.5 font-medium">Column</th>
                <th className="py-1.5 font-medium">Type</th>
                <th className="py-1.5 font-medium">Null</th>
                <th className="py-1.5 font-medium">Default</th>
              </tr>
            </thead>
            <tbody>
              {ti.data.columns.map((c) => (
                <tr key={c.name} className="border-t border-line">
                  <td className="py-1.5 font-mono">
                    {c.name}
                    {c.primary && (
                      <Badge tone="accent" className="ml-1.5">
                        PK
                      </Badge>
                    )}
                  </td>
                  <td className="py-1.5 font-mono text-muted">{c.type}</td>
                  <td className="py-1.5 text-muted">{c.nullable ? "yes" : "no"}</td>
                  <td className="py-1.5 font-mono text-faint truncate max-w-48" title={c.default ?? ""}>
                    {c.default ?? ""}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div>
            <div className="text-[11px] uppercase tracking-wide text-faint mb-1.5">Indexes</div>
            {ti.data.indexes.length === 0 && <div className="text-faint text-[12px]">No indexes.</div>}
            <div className="space-y-1.5">
              {ti.data.indexes.map((ix) => (
                <div key={ix.name} className="rounded-md border border-line bg-panel-2 px-2.5 py-1.5">
                  <div className="flex items-center gap-1.5 text-[12.5px]">
                    <span className="font-mono">{ix.name}</span>
                    {ix.primary ? <Badge tone="accent">primary</Badge> : ix.unique && <Badge>unique</Badge>}
                  </div>
                  <div className="font-mono text-[11.5px] text-muted mt-0.5 break-all">{ix.definition}</div>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </Drawer>
  );
}
