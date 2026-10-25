import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AskAgentButton } from "../components/AskAgent";
import type { PointerEvent as ReactPointerEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { AlertTriangle, ChevronDown, ChevronRight, Database, Eye, Info, KeyRound, Layers, Play, RefreshCw, ShieldAlert, Table2 } from "lucide-react";
import { ApiError, api, errMsg } from "../api/client";
import type { DbAnswer, DbEngine, DbNotRead, DbResult, DbTable, Resource } from "../api/types";
import { useWs } from "../context/app";
import { useTabTitle } from "../context/tabs";
import { Badge, Button, EmptyState, ErrorBox, IconButton, Input, Kbd, Loading, PageHeader, SearchInput, Select, cx, riskTone } from "../components/ui";
import { Drawer, Modal, useToast } from "../components/overlay";
import { useAsync, useLocalStorage } from "../hooks";
import CodeEditor from "./ansible/editor/CodeEditor";
import type { Language } from "./ansible/editor/CodeEditor";
import { ResultGrid } from "./databases/ResultGrid";

const ENGINES: Record<string, DbEngine> = { postgres: "postgres", postgresql: "postgres", mysql: "mysql", mariadb: "mysql", mongodb: "mongodb", mongo: "mongodb", redis: "redis", valkey: "redis" };
const engineOf = (r?: Resource): DbEngine | undefined => ENGINES[String(r?.spec.engine ?? "").toLowerCase()];

// what each engine calls things, and how its scripts look
const ENGINE: Record<DbEngine, { language: Language; tables: string; table: string; start: string; hint: string }> = {
  postgres: { language: "pgsql", tables: "Tables", table: "table", start: "select now();", hint: "write SQL above" },
  mysql: { language: "mysql", tables: "Tables", table: "table", start: "select now();", hint: "write SQL above" },
  mongodb: { language: "mongo", tables: "Collections", table: "collection", start: "show collections", hint: "write a query like db.orders.find({status: \"open\"}).limit(20) above" },
  redis: { language: "redis", tables: "Keys", table: "key", start: "INFO server", hint: "write commands above, one per line (GET k, HGETALL k, SCAN 0 MATCH user:*)" },
};

// Databases of the workspace (PostgreSQL, MySQL, MariaDB, MongoDB, Redis):
// tables, collections or keys, their structure, and a query editor. Reads
// run so they cannot change data; a script that is not a read is shown for
// confirmation first.
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

  const engine = engineOf(res);
  const supported = !!engine;
  // Redis keys are listed by a SCAN pattern on the server
  const [match, setMatch] = useState("");
  useEffect(() => setMatch(""), [db]);
  const info = useAsync(() => (supported ? api.dbInfo(ws, db, database || undefined) : Promise.resolve(undefined)), [ws, db, database, supported]);
  const dbs = useAsync(() => (supported ? api.dbDatabases(ws, db) : Promise.resolve([] as string[])), [ws, db, supported]);
  const tables = useAsync(
    () => (supported ? api.dbTables(ws, db, database || undefined, engine === "redis" ? match || undefined : undefined) : Promise.resolve([] as DbTable[])),
    [ws, db, database, supported, engine === "redis" ? match : ""],
  );

  if (!all.length) {
    return (
      <div className="flex flex-col h-full">
        <PageHeader icon={<Database />} title="Databases" />
        <EmptyState icon={<Database />} title="No databases in this workspace">
          Add a <span className="font-mono">Database</span> resource (PostgreSQL, MySQL, MariaDB, MongoDB or Redis) under Resources, with its login stored in the vault. opsdeck reaches it through the
          workspace&apos;s VPNs and jump hosts, or through an SSH tunnel to the host it runs on.
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
            "Tables, collections and keys — reads run without changing data"
          )
        }
        actions={
          <>
            <AskAgentButton prompt={"Check the health of this workspace's databases: connections, slow queries and disk usage."} />
            {supported && (
              <Select value={current} onChange={(e) => set({ database: e.target.value })} className="w-44" title="Database on the server">
                {current && !(dbs.data ?? []).includes(current) && <option value={current}>{engine === "redis" ? `db ${current}` : current}</option>}
                {(dbs.data ?? []).map((d) => (
                  <option key={d} value={d}>
                    {engine === "redis" ? `db ${d}` : d}
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
          The Databases page opens PostgreSQL, MySQL, MariaDB, MongoDB and Redis databases. The agent can still work with this one through <span className="font-mono">exec</span> on its host.
        </EmptyState>
      ) : (
        <Workbench
          key={`${db}/${database}`}
          ws={ws}
          db={db}
          res={res!}
          engine={engine}
          atomic={info.data?.atomic ?? (engine === "postgres" || engine === "mysql")}
          database={database || undefined}
          tables={tables.data}
          tablesLoading={tables.loading}
          tablesError={tables.error}
          reloadTables={tables.reload}
          match={match}
          onMatch={setMatch}
          connError={info.error}
        />
      )}
    </div>
  );
}

function shortVersion(v?: string) {
  if (!v) return "";
  if (/^(mongodb|redis|valkey) /i.test(v)) return v;
  if (/mariadb/i.test(v)) return `MariaDB ${v.split("-")[0]}`;
  if (/^postgresql/i.test(v)) return v.split(" on ")[0].split(",")[0];
  return `MySQL ${v.split("-")[0]}`;
}

type HistoryItem = { sql: string; at: number };

function Workbench({
  ws,
  db,
  res,
  engine,
  atomic,
  database,
  tables,
  tablesLoading,
  tablesError,
  reloadTables,
  match,
  onMatch,
  connError,
}: {
  ws: string;
  db: string;
  res: Resource;
  engine: DbEngine;
  atomic: boolean;
  database?: string;
  tables?: DbTable[];
  tablesLoading: boolean;
  tablesError: string | null;
  reloadTables: () => void;
  match: string;
  onMatch: (m: string) => void;
  connError: string | null;
}) {
  const E = ENGINE[engine];
  const [sql, setSql] = useLocalStorage<string>(`opsdeck.db.sql.${ws}.${db}`, E.start);
  const [history, setHistory] = useLocalStorage<HistoryItem[]>(`opsdeck.db.history.${ws}.${db}`, []);
  const [maxRows, setMaxRows] = useLocalStorage<number>("opsdeck.db.maxRows", 500);
  const [split, setSplit] = useLocalStorage<number>("opsdeck.db.split", 38);
  const [answer, setAnswer] = useState<DbAnswer | null>(null);
  const [failure, setFailure] = useState("");
  const [running, setRunning] = useState(false);
  const [confirm, setConfirm] = useState<(DbNotRead & { sql: string }) | null>(null);
  const [structure, setStructure] = useState<DbTable | null>(null);
  const [risk, setRisk] = useState<DbNotRead | null>(null);
  const toast = useToast();
  const sqlRef = useRef(sql);
  sqlRef.current = sql;
  const mainRef = useRef<HTMLElement>(null);

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
      if (write && !a.error) {
        toast.success(atomic ? "Committed" : "Done", `${a.results.length} statement${a.results.length === 1 ? "" : "s"}`);
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

  const openTable = (t: DbTable) => {
    setSql(t.query);
    void run(t.query);
  };
  const words = useMemo(
    () =>
      (tables ?? []).flatMap((t) => {
        if (engine === "postgres" && t.schema !== "public") return [t.name, `${t.schema}.${t.name}`];
        if (engine === "mongodb") return [t.name, `db.${t.name}`];
        return [t.name];
      }),
    [tables, engine],
  );

  // the line between the editor and the results moves
  const drag = (e: ReactPointerEvent) => {
    e.preventDefault();
    const box = mainRef.current?.getBoundingClientRect();
    if (!box) return;
    const onMove = (ev: PointerEvent) => setSplit(Math.min(80, Math.max(12, ((ev.clientY - box.top) / box.height) * 100)));
    const onUp = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };

  return (
    <div className="flex flex-1 min-h-0">
      <TableList
        engine={engine}
        tables={tables}
        loading={tablesLoading}
        error={tablesError}
        onReload={reloadTables}
        onOpen={openTable}
        onStructure={setStructure}
        match={match}
        onMatch={onMatch}
      />
      <main ref={mainRef} className="flex-1 min-w-0 flex flex-col">
        <div className="min-h-32 flex flex-col" style={{ height: `${split}%` }}>
          <div className="flex items-center gap-2 px-3 h-10 border-b border-line bg-panel shrink-0">
            <Button size="sm" variant="primary" icon={<Play className="size-3.5" />} onClick={() => void run(sqlRef.current)} loading={running} disabled={!sql.trim()}>
              Run
            </Button>
            <Kbd>Ctrl ↵</Kbd>
            {risk && (
              <Badge tone={riskTone(risk.risk)} title={risk.reasons.join("\n")}>
                {risk.risk === "read" ? (atomic ? "read · read-only transaction" : "read") : `${risk.risk} · asks before running`}
              </Badge>
            )}
            <div className="flex-1" />
            <HistoryPicker items={history} onPick={setSql} />
            <Select value={String(maxRows)} onChange={(e) => setMaxRows(Number(e.target.value))} className="h-7 w-32 text-xs" title="Rows shown per result">
              {[100, 500, 1000, 5000, 10000].map((n) => (
                <option key={n} value={n}>
                  {n} rows
                </option>
              ))}
            </Select>
          </div>
          <div className="flex-1 min-h-0">
            <CodeEditor value={sql} onChange={setSql} language={E.language} onRun={() => void run(sqlRef.current)} words={words} autoFocus />
          </div>
        </div>
        <div className="h-1.5 -my-[3px] z-10 cursor-row-resize border-b border-line hover:bg-accent/30 shrink-0" onPointerDown={drag} title="Drag to resize" />
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
            <Results answer={answer} atomic={atomic} engine={engine} />
          ) : (
            !failure &&
            !connError && (
              <EmptyState icon={<Table2 />} title="Run a query" className="flex-1">
                Click a {E.table} to see its content, or {E.hint} and press <Kbd>Ctrl ↵</Kbd>. Scripts that change data are shown to you before they run.
              </EmptyState>
            )
          )}
        </div>
      </main>
      {confirm && (
        <ConfirmChange
          db={db}
          database={answer?.database || database || String(res.spec.database || "")}
          atomic={confirm.atomic ?? atomic}
          c={confirm}
          onCancel={() => setConfirm(null)}
          onRun={() => {
            const text = confirm.sql;
            setConfirm(null);
            void run(text, true);
          }}
        />
      )}
      {structure && <StructureDrawer ws={ws} db={db} database={database} t={structure} onClose={() => setStructure(null)} onShow={() => (openTable(structure), setStructure(null))} />}
    </div>
  );
}

function TableList({
  engine,
  tables,
  loading,
  error,
  onReload,
  onOpen,
  onStructure,
  match,
  onMatch,
}: {
  engine: DbEngine;
  tables?: DbTable[];
  loading: boolean;
  error: string | null;
  onReload: () => void;
  onOpen: (t: DbTable) => void;
  onStructure: (t: DbTable) => void;
  match: string;
  onMatch: (m: string) => void;
}) {
  const E = ENGINE[engine];
  const redis = engine === "redis";
  const grouped = engine === "postgres" || redis;
  const [q, setQ] = useState(match);
  const [closed, setClosed] = useState<Record<string, boolean>>({});
  // Redis: the search is a SCAN pattern, sent once typing stops
  useEffect(() => {
    if (!redis) return;
    const t = setTimeout(() => onMatch(!q ? "" : /[*?[]/.test(q) ? q : `*${q}*`), 400);
    return () => clearTimeout(t);
  }, [q, redis, onMatch]);
  const groups = useMemo(() => {
    const m = new Map<string, DbTable[]>();
    for (const t of tables ?? []) {
      if (!redis && q && !`${t.schema}.${t.name}`.toLowerCase().includes(q.toLowerCase())) continue;
      const k = grouped ? t.schema : "";
      if (!m.has(k)) m.set(k, []);
      m.get(k)!.push(t);
    }
    return [...m.entries()];
  }, [tables, q, grouped, redis]);
  const many = groups.length > 1;
  return (
    <aside className="w-64 shrink-0 border-r border-line flex flex-col min-h-0">
      <div className="p-2 flex items-center gap-1 border-b border-line">
        <SearchInput value={q} onChange={setQ} placeholder={redis ? "Keys (pattern: user:*)…" : `${E.tables}…`} className="flex-1" />
        <IconButton title={`Reload ${E.tables.toLowerCase()}`} onClick={onReload}>
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
        {tables && tables.length === 0 && <div className="text-[12px] text-faint p-2">{redis && match ? `No key matches ${match}.` : `No ${E.tables.toLowerCase()} in this database.`}</div>}
        {redis && tables && tables.length >= 1000 && <div className="text-[11px] text-faint px-2 pt-2">The first 1,000 keys. Narrow the pattern to see others.</div>}
        {groups.map(([schema, ts]) => {
          const open = (q && !redis) || !many ? true : !closed[schema];
          return (
            <div key={schema} className="mt-2">
              {grouped && many && (
                <button className="w-full flex items-center gap-1 px-1 text-[10.5px] uppercase tracking-wide text-faint hover:text-muted" onClick={() => setClosed({ ...closed, [schema]: open })}>
                  {open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
                  <span className={cx(redis && "normal-case font-mono")}>{schema || (redis ? "(no prefix)" : "")}</span>
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
                        title={`${t.type} · ${t.query}`}
                      >
                        <TableIcon t={t} engine={engine} />
                        <span className="truncate font-mono">{redis && many && t.schema && t.name.startsWith(t.schema + ":") ? <><span className="text-faint">…:</span>{t.name.slice(t.schema.length + 1)}</> : t.name}</span>
                        <span className="ml-auto text-[10.5px] text-faint tabular-nums whitespace-nowrap">{t.rows >= 0 ? compact(t.rows) : t.bytes > 0 && redis ? bytes(t.bytes) : ""}</span>
                      </button>
                      <IconButton title={engine === "redis" ? "Type, expiry and size" : engine === "mongodb" ? "Fields and indexes" : "Columns and indexes"} onClick={() => onStructure(t)} className="opacity-0 group-hover:opacity-100">
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

function TableIcon({ t, engine }: { t: DbTable; engine: DbEngine }) {
  if (t.type.includes("view")) return <Eye className="size-3.5 shrink-0 text-info" />;
  if (engine === "mongodb") return <Layers className="size-3.5 shrink-0" />;
  if (engine === "redis") return <KeyRound className="size-3.5 shrink-0" />;
  return <Table2 className="size-3.5 shrink-0" />;
}

function compact(n: number) {
  if (n < 1000) return String(n);
  if (n < 1e6) return `${(n / 1e3).toFixed(n < 1e4 ? 1 : 0)}k`;
  if (n < 1e9) return `${(n / 1e6).toFixed(n < 1e7 ? 1 : 0)}M`;
  return `${(n / 1e9).toFixed(1)}B`;
}

function bytes(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}

function Results({ answer, atomic, engine }: { answer: DbAnswer; atomic: boolean; engine: DbEngine }) {
  const [i, setI] = useState(0);
  useEffect(() => setI(Math.max(0, answer.results.length - 1)), [answer]);
  const r = answer.results[Math.min(i, answer.results.length - 1)];
  const wrote = answer.risk !== "read";
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
            {!wrote || answer.results.length === 0 ? "Failed" : atomic ? "Failed — nothing was committed" : answer.results.length > 1 ? "Failed — the statements before it ran" : "Failed"}
          </span>
        ) : wrote ? (
          <span className="text-ok">{atomic ? "Committed" : "Done"}</span>
        ) : (
          <span>{atomic ? "Read-only transaction" : engine === "redis" ? "Read commands only" : "Read operations only"}</span>
        )}
        <span>{engine === "redis" ? `db ${answer.database}` : answer.database}</span>
        <span className="ml-auto tabular-nums">{answer.durationMs} ms</span>
      </div>
    </div>
  );
}

function ResultView({ r }: { r: DbResult }) {
  if (r.error) {
    return (
      <div className="p-4">
        <ErrorBox error={r.error} />
      </div>
    );
  }
  if (!r.columns) {
    return (
      <div className="flex-1 grid place-items-center text-muted text-[13px] text-center px-4">
        <div>
          {r.rowsAffected >= 0 ? `${r.rowsAffected} row${r.rowsAffected === 1 ? "" : "s"} affected` : "Done"} · {r.durationMs} ms
          {r.note && <div className="mt-1 text-faint text-[12px] font-mono">{r.note}</div>}
        </div>
      </div>
    );
  }
  return <ResultGrid r={r} />;
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

function ConfirmChange({
  db,
  database,
  atomic,
  c,
  onCancel,
  onRun,
}: {
  db: string;
  database: string;
  atomic: boolean;
  c: DbNotRead & { sql: string };
  onCancel: () => void;
  onRun: () => void;
}) {
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
          {atomic
            ? "It runs as one transaction: if a statement fails, nothing is committed."
            : "It runs statement by statement and stops at the first error: what ran before it stays (this engine has no transaction over a script)."}{" "}
          It is recorded in the activity journal.
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

function StructureDrawer({ ws, db, database, t, onClose, onShow }: { ws: string; db: string; database?: string; t: DbTable; onClose: () => void; onShow: () => void }) {
  const ti = useAsync(() => api.dbTable(ws, db, t.schema, t.name, database), [ws, db, database, t.schema, t.name]);
  const kind = ti.data?.kind ?? "table";
  const sub = [t.type, kind === "key" ? "" : t.schema, t.rows >= 0 ? `~${compact(t.rows)} ${kind === "collection" ? "documents" : kind === "key" ? "elements" : "rows"}` : ""].filter(Boolean).join(" · ");
  return (
    <Drawer
      open
      onClose={onClose}
      width={620}
      title={<span className="font-mono">{t.name}</span>}
      subtitle={sub}
      footer={
        <Button icon={<Play className="size-3.5" />} onClick={onShow} title={t.query}>
          Show content
        </Button>
      }
    >
      {ti.loading && <Loading />}
      {ti.error && <ErrorBox error={ti.error} />}
      {ti.data && (
        <div className="space-y-5">
          {ti.data.properties && ti.data.properties.length > 0 && (
            <table className="w-full text-[12.5px]">
              <tbody>
                {ti.data.properties.map((p) => (
                  <tr key={p.name} className="border-t border-line first:border-t-0">
                    <td className="py-1.5 pr-4 text-muted whitespace-nowrap align-top">{p.name}</td>
                    <td className="py-1.5 font-mono break-all">{p.value}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {ti.data.columns.length > 0 && (
            <table className="w-full text-[12.5px]">
              <thead>
                <tr className="text-left text-faint text-[11px] uppercase tracking-wide">
                  <th className="py-1.5 font-medium">{kind === "collection" ? "Field" : "Column"}</th>
                  <th className="py-1.5 font-medium">{kind === "collection" ? "Types" : "Type"}</th>
                  <th className="py-1.5 font-medium">Null</th>
                  <th className="py-1.5 font-medium">{kind === "collection" ? "Present in" : "Default"}</th>
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
                      {c.default ?? (kind === "collection" ? "all" : "")}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {ti.data.note && <div className="text-[12px] text-faint">{ti.data.note}</div>}
          {kind !== "key" && (
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
          )}
        </div>
      )}
    </Drawer>
  );
}
