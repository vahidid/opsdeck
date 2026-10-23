# Databases

The **Databases** page opens the PostgreSQL, MySQL and MariaDB databases of a workspace:
the databases on the server, their tables and views, the columns and indexes of a table,
and a SQL editor with results as a table. The agent works with the same databases through
its `database` tool.

## The resource

```yaml
- kind: Database
  name: shop-db
  environment: prod                  # protected environments: every change is confirmed
  spec:
    engine: postgres                 # postgres | mysql | mariadb (others: not supported yet)
    address: 127.0.0.1:5432          # see "How opsdeck reaches it"
    host: db-1                       # the Host it runs on (optional)
    database: shop                   # default database (PostgreSQL: postgres)
    login: vault://acme/shop-db      # a login item: username and password
    tls: prefer                      # prefer (default) | require | verify-full | disable
```

The login stays in the vault. As for every credential, *Agent may use this item* in the
vault decides whether the agent may use it, and *Ask me before every use* makes every
agent call that uses it ask.

## How opsdeck reaches it

1. **`via`** set: through that network or jump host, like any other resource. `address` is
   the database as seen from there.
2. **No `via`, but `host`**: through an SSH tunnel to that host. `address` is the database
   as the host sees it, so `127.0.0.1:5432` works for a database that only listens
   locally. The host's own path (its VPN, its bastion) is used to get there.
3. Neither: directly from this computer.

The page's header shows the path, e.g. `via Host/bastion → Host/db-1`.

## Reads and changes

Every script is split into statements and classified (`internal/dbx/sqlrisk.go`):

- **Reads** — `SELECT`, `WITH … SELECT`, `VALUES`, `TABLE`, `SHOW`, `DESCRIBE`, `EXPLAIN`
  (without `ANALYZE` of a change), session `SET`/`USE`. They run in a **read-only
  transaction** that is rolled back, so the database itself refuses a write the
  classifier did not see (a function of yours that inserts, for example). The
  classifier also keeps out what a read-only transaction still allows: functions that end
  sessions or touch server files (`pg_terminate_backend`, `lo_export`, `pg_read_file`,
  `LOAD_FILE`, `dblink`, `query_to_xml`…), `SELECT … INTO OUTFILE`, `FOR UPDATE`,
  `SET GLOBAL`, `nextval`.
- **Changes** — everything else (`INSERT`, `UPDATE … WHERE`, `CREATE`, `ALTER`, `GRANT`,
  `CALL`, `DO`, `COPY`…). On the page they are shown for confirmation first; the agent
  asks for approval like any change on a server.
- **Destructive** — `DROP`, `TRUNCATE`, `DELETE`/`UPDATE` without `WHERE`, `ALTER … DROP`,
  `RESET MASTER`, `PURGE`, `SHUTDOWN`, and code blocks that drop data. On a protected
  environment the page asks you to type the database's name.

Comments, strings, quoted identifiers, `$$` bodies and MySQL's executable `/*! … */`
comments are tokenized, so a `DROP` in a string is not a statement and one in a `/*! */`
comment is. Transaction control (`BEGIN`, `COMMIT`…) is refused: opsdeck runs each
script in **one transaction** of its own, and commits a change only when every statement
succeeded. Confirmed changes are recorded in the activity journal (`db.query`, with the
SQL). A script is cancelled after 2 minutes; PostgreSQL also stops the statement on the
server then, MySQL does so for `SELECT`s.

## The page

- **Tables** on the left (by schema for PostgreSQL), with estimated rows. A click shows
  the first 100 rows; the ⓘ button shows columns, keys and indexes.
- **The editor** highlights PostgreSQL or MySQL, completes keywords and table names, and
  runs with **Ctrl/⌘-Enter**. Its badge tells, as you type, whether the script is a read or
  will ask first. Recent queries are under *History* (kept in the browser).
- **Results** per statement: rows (up to the chosen limit), rows affected, time, errors.
  *Copy* puts the rows on the clipboard as tab-separated text.

## The agent

The `database` tool: `databases`, `tables`, `describe` (a table's columns and indexes) and
`query` (SQL). Reads run without approval in a read-only transaction; anything else waits
for approval with the SQL on the approval card. Results reach the model as a text table
(100 rows by default, cells cut at 200 characters), after opsdeck's redaction of secrets.
Workspace permission rules with `tool: database` apply to it; their `match` is compared
with the SQL text as written (case-sensitive).

## Limits

- PostgreSQL, MySQL and MariaDB. Other engines (MongoDB, Redis, Elasticsearch) can be
  modelled but not opened yet.
- psql meta-commands (`\dt`), `COPY … TO STDOUT` and MySQL's `DELIMITER` are client
  features, not SQL.
- Large results are cut at the row limit (at most 10,000 on the page); values longer
  than 10,000 characters are cut, binary values are shown as hex.
