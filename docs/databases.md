# Databases

The **Databases** page opens the PostgreSQL, MySQL, MariaDB, MongoDB and Redis databases of a
workspace: the databases on the server; their tables and views, collections or keys; the
structure of each; and an editor with the results in a grid. The agent works with the same
databases through its `database` tool.

## The resource

```yaml
- kind: Database
  name: shop-db
  environment: prod                  # protected environments: every change is confirmed
  spec:
    engine: postgres                 # postgres | mysql | mariadb | mongodb | redis (others: not supported yet)
    address: 127.0.0.1:5432          # see "How opsdeck reaches it"
    host: db-1                       # the Host it runs on (optional)
    database: shop                   # default database (PostgreSQL: postgres; MongoDB: admin; Redis: 0)
    login: vault://acme/shop-db      # a login item: username and password
    tls: prefer                      # prefer (default) | require | verify-full | disable
    authSource: admin                # MongoDB: the database the user is defined in (default admin)
```

The login stays in the vault. As for every credential, *Agent may use this item* in the
vault decides whether the agent may use it, and *Ask me before every use* makes every
agent call that uses it ask. For Redis, an empty username is the `default` user (a
password only, as with `requirepass`); `database` is the number of the database.

## How opsdeck reaches it

1. **`via`** set: through that network or jump host, like any other resource. `address` is
   the database as seen from there.
2. **No `via`, but `host`**: through an SSH tunnel to that host. `address` is the database
   as the host sees it, so `127.0.0.1:5432` works for a database that only listens
   locally. The host's own path (its VPN, its bastion) is used to get there.
3. Neither: directly from this computer.

The page's header shows the path, e.g. `via Host/bastion → Host/db-1`.

## Scripts

| Engine | Language | Example |
|---|---|---|
| PostgreSQL, MySQL, MariaDB | SQL, statements separated by `;` | `select * from orders where status = 'paid' limit 50;` |
| MongoDB | mongosh syntax, one statement per line (a chain may continue on the next line) | `db.orders.find({status: "paid"}).sort({_id: -1}).limit(50)` |
| Redis | redis-cli commands, one per line | `HGETALL session:8f2c` |

**MongoDB** scripts understand `show dbs`, `show collections`, `use <db>`,
`db.<collection>.<method>(…)` (also `db.getCollection("name")` and `db["name"]`) with
`.sort()`, `.limit()`, `.skip()`, `.projection()`, `.hint()` and `.count()`, and
`db.runCommand({…})` / `db.adminCommand({…})`. Arguments are JSON-like: unquoted keys,
single or double quotes, trailing commas, `ObjectId("…")`, `ISODate("…")`, `new Date(…)`,
`NumberLong`, `NumberInt`, `NumberDecimal`, `UUID` and `/regular expressions/`. Nothing
else of JavaScript is evaluated: no variables, loops or functions. Methods: `find`, `findOne`,
`aggregate`, `countDocuments`, `estimatedDocumentCount`, `distinct`, `getIndexes`, `stats`,
`insertOne`, `insertMany`, `updateOne`, `updateMany`, `replaceOne`, `deleteOne`,
`deleteMany`, `findOneAndUpdate`/`Replace`/`Delete`, `createIndex`, `dropIndex`,
`renameCollection`, `drop`; on `db`: `getCollectionNames`, `getCollectionInfos`, `stats`,
`version`, `createCollection`, `dropDatabase`.

**Redis** lines are split like redis-cli does: `"…"` with `\n`, `\t`, `\xHH` and `\"`
escapes, `'…'` literal; a line starting with `#` or `//` is a comment.

## Reads and changes

Every script is split into statements and classified (`internal/dbx/sqlrisk.go`,
`mongoshell.go`, `redisshell.go`):

- **Reads** run directly.
  - SQL: `SELECT`, `WITH … SELECT`, `VALUES`, `TABLE`, `SHOW`, `DESCRIBE`, `EXPLAIN` (without
    `ANALYZE` of a change), session `SET`/`USE`. They run in a **read-only transaction** that
    is rolled back, so the database itself refuses a write the classifier did not see (a
    function of yours that inserts, for example). The classifier also keeps out what a
    read-only transaction still allows: functions that end sessions or touch server files
    (`pg_terminate_backend`, `lo_export`, `pg_read_file`, `LOAD_FILE`, `dblink`,
    `query_to_xml`…), `SELECT … INTO OUTFILE`, `FOR UPDATE`, `SET GLOBAL`, `nextval`.
  - MongoDB: `find`, `findOne`, `aggregate` without `$out`/`$merge`, counts, `distinct`,
    `getIndexes`, `stats`, `show`, `use`, and commands on a read list (`ping`, `buildInfo`,
    `serverStatus`, `dbStats`, `collStats`, `listCollections`, `listIndexes`, `find`,
    `aggregate`, `count`, `distinct`, `explain`, `currentOp`, `usersInfo`…).
  - Redis: read commands (`GET`, `MGET`, `HGETALL`, `LRANGE`, `SMEMBERS`, `ZRANGE`, `XRANGE`,
    the `SCAN`s, `TYPE`, `TTL`, `INFO`, `DBSIZE`, `CONFIG GET`, `CLIENT LIST`, `SLOWLOG GET`,
    `MEMORY USAGE`, `OBJECT ENCODING`, `EVAL_RO`, `SELECT`…).

  MongoDB and Redis have no read-only transaction: there, only the operations on these
  lists run as reads, and opsdeck itself runs them (a script is never sent to a shell).
- **Changes** — everything else (SQL `INSERT`, `UPDATE … WHERE`, `CREATE`, `GRANT`, `CALL`…;
  MongoDB inserts, updates, deletes with a filter, indexes, other commands; Redis `SET`,
  `DEL`, `EXPIRE`, `CONFIG SET`, `EVAL`…). On the page they are shown for confirmation
  first; the agent asks for approval like any change on a server.
- **Destructive** — SQL `DROP`, `TRUNCATE`, `DELETE`/`UPDATE` without `WHERE`, `ALTER … DROP`,
  `RESET MASTER`, `PURGE`, `SHUTDOWN` and code blocks that drop data; MongoDB `drop()`,
  `dropDatabase()`, `deleteMany({})` and `updateMany({}, …)` (every document), and the
  `dropDatabase`, `drop`, `shutdown` and user-dropping commands; Redis `FLUSHALL`, `FLUSHDB`,
  `SHUTDOWN`, `DEBUG`, `SWAPDB`, `REPLICAOF`, `SCRIPT FLUSH`, `FUNCTION FLUSH`/`DELETE`. On a
  protected environment the page asks you to type the database's name.

SQL comments, strings, quoted identifiers, `$$` bodies and MySQL's executable `/*! … */`
comments are tokenized, so a `DROP` in a string is not a statement and one in a `/*! */`
comment is. Transaction control (`BEGIN`, `COMMIT`…) is refused: opsdeck runs each SQL
script in **one transaction** of its own, and commits a change only when every statement
succeeded. **MongoDB and Redis scripts are not one transaction**: they run statement by
statement and stop at the first error, and what ran before it stays (the confirmation says
so). Redis `MULTI`/`EXEC`, `WATCH`, subscriptions, `MONITOR` and blocking commands (`BLPOP`,
`XREAD BLOCK`…) are refused; a `SELECT` lasts for its script only.

Confirmed changes are recorded in the activity journal (`db.query`, with the script). A
script is cancelled after 2 minutes; PostgreSQL also stops the statement on the server then,
MySQL does so for `SELECT`s.

## The page

- **Tables, collections or keys** on the left — by schema for PostgreSQL, by the part before
  the first `:` for Redis keys — with estimated rows, documents or elements. A click shows
  the content (the first 100 rows or documents; a Redis key by its type: `GET`, `HGETALL`,
  `LRANGE`, `ZRANGE … WITHSCORES`, `XREVRANGE`…). The ⓘ button shows columns, keys and
  indexes; for a collection, the fields of its 200 most recent documents with their types
  and how many documents have them; for a key, its type, expiry, encoding, memory and length.
  For Redis the search box is a `SCAN` pattern (`user:*`; plain text means `*text*`), and at
  most 1,000 keys are listed.
- **The editor** highlights SQL, MongoDB queries or Redis commands, completes keywords,
  methods, commands and table, collection or key names, and runs with **Ctrl/⌘-Enter**. Its
  badge tells, as you type, whether the script is a read or will ask first. Recent scripts
  are under *History* (kept in the browser). Drag the line under it to give the results more
  room.
- **Results**, one tab per statement: rows (up to the chosen limit, at most 10,000), rows
  affected, time, notes (a `SCAN`'s next cursor, an inserted `_id`), errors. MongoDB
  documents become rows with a column per top-level field (`_id` first); nested documents
  and arrays stay JSON.

### The results grid

- Rows are virtualized, so 10,000 of them scroll smoothly. Columns are resized by dragging
  their edge (double-click fits the content) and sorted by clicking their name (numbers as
  numbers). *Filter rows* keeps the rows with the text in any column.
- A cell shows one line, with ⏎ for line breaks; the wrap button shows long text in full
  lines instead.
- **Double-click a cell** (or select it and press Enter) to open the viewer beside the grid:
  the whole value (up to 65,536 characters; a longer one says so), JSON indented and
  foldable, with search (Ctrl/⌘-F), wrapping and copy. *Row* shows every column of the row,
  and ↑/↓ move between rows. The arrow keys move the selection, Ctrl/⌘-C copies a cell, Esc
  closes the viewer.
- The `{}` view shows the rows as JSON documents. *Copy* puts the rows (as filtered and
  sorted) on the clipboard as tab-separated text, CSV or JSON; the download button saves a
  CSV or JSON file.

## The agent

The `database` tool: `databases`, `tables` (with `match` for Redis keys), `describe` (a
table's columns and indexes, a collection's fields, a key's properties) and `query` (the
script). Reads run without approval; anything else waits for approval with the script on
the approval card, which also says whether it runs as one transaction. Results reach the
model as a text table (100 rows by default, cells cut at 200 characters), after opsdeck's
redaction of secrets. Workspace permission rules with `tool: database` apply to it; their
`match` is compared with the script as written (case-sensitive).

## Limits

- PostgreSQL, MySQL, MariaDB, MongoDB and Redis (Valkey works as Redis). Other engines
  (Elasticsearch…) can be modelled but not opened yet. MongoDB is opened as one server
  (a direct connection to `address`), not as a replica set or `mongodb+srv://` URI; Redis
  Cluster is not supported.
- psql meta-commands (`\dt`), `COPY … TO STDOUT`, MySQL's `DELIMITER` and mongosh's
  JavaScript (variables, `forEach`, `printjson`) are client features, not part of the
  scripts.
- Large results are cut at the row limit; values longer than 65,536 characters are cut on the
  page (200 for the agent), binary values are shown as hex.
