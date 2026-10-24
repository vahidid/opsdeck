package dbx

import (
	"strings"
	"unicode"
)

// Risk classes, the same as the agent's.
const (
	RiskRead        = "read"
	RiskChange      = "change"
	RiskDestructive = "destructive"
)

func rank(r string) int {
	switch r {
	case RiskRead:
		return 0
	case RiskChange:
		return 1
	}
	return 2
}

func maxRisk(a, b string) string {
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// Statement is one SQL statement of a script.
type Statement struct {
	Text        string
	Risk        string
	Reasons     []string
	ReturnsRows bool // run as a query (a result set) instead of an exec (rows affected)
	TxControl   bool // BEGIN, COMMIT, ROLLBACK…: refused, opsdeck runs each script in one transaction
}

// Classification of a SQL script.
type Classification struct {
	Risk       string
	Reasons    []string
	Statements []Statement
}

// Classify splits a SQL script into statements and classifies each: a read
// is an allowlist (SELECT, SHOW, EXPLAIN, ... without writing clauses or
// administrative functions); anything else is a change, and statements that
// drop or erase data are destructive. engine is "postgres" or "mysql".
//
// Reads are also run in a read-only transaction, so the database enforces
// what the classifier allows; the classifier keeps out what such a
// transaction still permits (killing sessions, writing server files...).
func classifySQL(engine, sql string) Classification {
	c := Classification{Risk: RiskRead}
	for _, st := range splitStatements(engine, sql) {
		s := classifyStatement(engine, st)
		c.Risk = maxRisk(c.Risk, s.Risk)
		c.Reasons = append(c.Reasons, s.Reasons...)
		c.Statements = append(c.Statements, s)
	}
	c.Reasons = dedupe(c.Reasons)
	return c
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// token is a word (upper-cased), a symbol, or a string / quoted identifier
// (kind 's' / 'i', value kept as written for dollar-quoted bodies).
type token struct {
	kind byte // 'w' word, 'p' punctuation, 's' string, 'i' quoted identifier, 'n' number, 'd' dollar-quoted body
	val  string
}

type rawStatement struct {
	text   string
	tokens []token
	nested []rawStatement // statements inside MySQL /*! ... */ comments
}

// splitStatements tokenizes a script and splits it at semicolons outside
// strings, quoted identifiers and comments.
func splitStatements(engine, sql string) []rawStatement {
	var out []rawStatement
	var cur []token
	var nested []rawStatement
	depth := 0 // BEGIN … END of a MySQL stored routine: semicolons inside do not end it
	start := 0
	flush := func(end int) {
		text := strings.TrimSpace(sql[start:end])
		if (len(cur) > 0 || len(nested) > 0) && text != "" {
			out = append(out, rawStatement{text: text, tokens: cur, nested: nested})
		}
		cur, nested, depth = nil, nil, 0
	}
	mysql := engine != "postgres"
	// runes and their byte offsets, to cut the original text
	var r []rune
	var off []int
	for bi, ch := range sql {
		r = append(r, ch)
		off = append(off, bi)
	}
	off = append(off, len(sql))
	i := 0
	for i < len(r) {
		ch := r[i]
		next := rune(0)
		if i+1 < len(r) {
			next = r[i+1]
		}
		switch {
		case unicode.IsSpace(ch):
			i++
		case ch == '-' && next == '-', mysql && ch == '#':
			for i < len(r) && r[i] != '\n' {
				i++
			}
		case ch == '/' && next == '*':
			// MySQL runs the text of /*! ... */ and /*+ ... */ comments: it is code
			if mysql && i+2 < len(r) && (r[i+2] == '!' || r[i+2] == '+') {
				j := i + 3
				for j < len(r) && unicode.IsDigit(r[j]) {
					j++
				}
				end := j
				for end+1 < len(r) && !(r[end] == '*' && r[end+1] == '/') {
					end++
				}
				body := string(r[j:min(end, len(r))])
				inner := splitStatements(engine, body)
				if strings.Contains(body, ";") {
					nested = append(nested, inner...) // statements of their own
				} else {
					for _, st := range inner {
						cur = append(cur, st.tokens...) // part of this statement
					}
				}
				i = min(end+2, len(r))
				continue
			}
			depth := 0
			for i < len(r) {
				if r[i] == '/' && i+1 < len(r) && r[i+1] == '*' {
					depth++
					i += 2
					if mysql {
						depth = 1 // MySQL comments do not nest
					}
					continue
				}
				if r[i] == '*' && i+1 < len(r) && r[i+1] == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
					continue
				}
				i++
			}
		case ch == '\'' || (mysql && ch == '"'):
			backslash := mysql || (i > 0 && (r[i-1] == 'E' || r[i-1] == 'e'))
			j := i + 1
			for j < len(r) {
				if backslash && r[j] == '\\' {
					j += 2
					continue
				}
				if r[j] == ch {
					if j+1 < len(r) && r[j+1] == ch {
						j += 2
						continue
					}
					break
				}
				j++
			}
			cur = append(cur, token{'s', string(r[i+1 : min(j, len(r))])})
			i = j + 1
		case ch == '"' || (mysql && ch == '`'):
			j := i + 1
			for j < len(r) && !(r[j] == ch && (j+1 >= len(r) || r[j+1] != ch)) {
				if r[j] == ch {
					j++
				}
				j++
			}
			cur = append(cur, token{'i', strings.ToUpper(string(r[i+1 : min(j, len(r))]))})
			i = j + 1
		case !mysql && ch == '$' && (next == '$' || unicode.IsLetter(next) || next == '_'):
			// dollar quoting: $tag$ ... $tag$
			j := i + 1
			for j < len(r) && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_') {
				j++
			}
			if j < len(r) && r[j] == '$' {
				tag := string(r[i : j+1])
				body := string(r[j+1:])
				k := strings.Index(body, tag)
				if k < 0 {
					k = len(body)
				}
				cur = append(cur, token{'d', body[:k]})
				i = j + 1 + len([]rune(body[:k])) + len([]rune(tag))
				continue
			}
			cur = append(cur, token{'p', "$"})
			i++
		case ch == ';' && depth > 0:
			cur = append(cur, token{'p', ";"})
			i++
		case ch == ';':
			flush(off[i])
			start = off[i] + 1
			i++
		case unicode.IsLetter(ch) || ch == '_':
			j := i
			for j < len(r) && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_' || r[j] == '$') {
				j++
			}
			w := strings.ToUpper(string(r[i:j]))
			if mysql && storedRoutine(cur) {
				switch {
				case w == "BEGIN" || w == "CASE":
					if !(w == "CASE" && len(cur) > 0 && cur[len(cur)-1].val == "END") {
						depth++
					}
				case w == "END":
					k := j
					for k < len(r) && unicode.IsSpace(r[k]) {
						k++
					}
					next := ""
					for e := k; e < len(r) && (unicode.IsLetter(r[e]) || r[e] == '_'); e++ {
						next += string(unicode.ToUpper(r[e]))
					}
					if !in(next, "IF", "LOOP", "WHILE", "REPEAT") && depth > 0 {
						depth-- // END, END CASE
					}
				}
			}
			cur = append(cur, token{'w', w})
			i = j
		case unicode.IsDigit(ch):
			j := i
			for j < len(r) && (unicode.IsDigit(r[j]) || r[j] == '.' || unicode.IsLetter(r[j])) {
				j++
			}
			cur = append(cur, token{'n', string(r[i:j])})
			i = j
		default:
			cur = append(cur, token{'p', string(ch)})
			i++
		}
	}
	flush(len(sql))
	return out
}

// storedRoutine reports whether a statement so far creates a MySQL
// function, procedure, trigger or event, whose body has semicolons.
func storedRoutine(t []token) bool {
	if len(t) == 0 || t[0].val != "CREATE" {
		return false
	}
	for _, x := range t {
		if x.kind == 'w' && in(x.val, "FUNCTION", "PROCEDURE", "TRIGGER", "EVENT") {
			return true
		}
	}
	return false
}

// Functions that act outside the transaction (sessions, server files,
// replication, other databases) or run SQL given as text: never a read.
var adminFunctions = map[string]bool{
	"PG_TERMINATE_BACKEND": true, "PG_CANCEL_BACKEND": true, "PG_RELOAD_CONF": true, "PG_ROTATE_LOGFILE": true,
	"PG_PROMOTE": true, "PG_SWITCH_WAL": true, "PG_CREATE_RESTORE_POINT": true, "PG_CREATE_PHYSICAL_REPLICATION_SLOT": true,
	"PG_CREATE_LOGICAL_REPLICATION_SLOT": true, "PG_DROP_REPLICATION_SLOT": true, "PG_REPLICATION_ORIGIN_CREATE": true,
	"PG_REPLICATION_ORIGIN_DROP": true, "PG_LOGICAL_EMIT_MESSAGE": true, "PG_WAL_REPLAY_PAUSE": true, "PG_WAL_REPLAY_RESUME": true,
	"PG_BACKUP_START": true, "PG_BACKUP_STOP": true, "PG_START_BACKUP": true, "PG_STOP_BACKUP": true,
	"LO_IMPORT": true, "LO_EXPORT": true, "LO_UNLINK": true, "LO_CREATE": true, "LO_CREAT": true, "LO_PUT": true, "LO_FROM_BYTEA": true,
	"PG_READ_FILE": true, "PG_READ_BINARY_FILE": true, "PG_LS_DIR": true, "PG_STAT_FILE": true, "PG_LS_LOGDIR": true,
	"PG_LS_WALDIR": true, "PG_LS_TMPDIR": true, "PG_LS_ARCHIVE_STATUSDIR": true, "PG_FILE_WRITE": true, "PG_FILE_UNLINK": true,
	"PG_FILE_RENAME": true, "PG_FILE_SYNC": true, "PG_LOGDIR_LS": true,
	"DBLINK": true, "DBLINK_EXEC": true, "DBLINK_CONNECT": true, "DBLINK_CONNECT_U": true, "DBLINK_SEND_QUERY": true, "DBLINK_OPEN": true,
	"QUERY_TO_XML": true, "QUERY_TO_XMLSCHEMA": true, "QUERY_TO_XML_AND_XMLSCHEMA": true,
	"NEXTVAL": true, "SETVAL": true, "PG_NOTIFY": true, "TXID_CURRENT": true, "PG_CURRENT_XACT_ID": true,
	"PG_ADVISORY_LOCK": true, "PG_ADVISORY_XACT_LOCK": true, "PG_TRY_ADVISORY_LOCK": true, "PG_ADVISORY_UNLOCK_ALL": true,
	"LOAD_FILE": true, "GET_LOCK": true, "RELEASE_LOCK": true, "RELEASE_ALL_LOCKS": true, "SYS_EXEC": true, "SYS_EVAL": true,
}

// First words of statements that only read (or only change the session).
var readStatements = map[string]bool{"SELECT": true, "WITH": true, "VALUES": true, "TABLE": true, "SHOW": true,
	"DESCRIBE": true, "DESC": true, "EXPLAIN": true, "USE": true, "SET": true, "RESET": true, "CHECK": true, "CHECKSUM": true, "HELP": true}

var dmlWords = map[string]string{"INSERT": RiskChange, "UPDATE": RiskChange, "DELETE": RiskChange, "MERGE": RiskChange, "REPLACE": RiskChange}

func classifyStatement(engine string, st rawStatement) Statement {
	s := Statement{Text: st.text, Risk: RiskRead}
	note := func(r, why string) {
		s.Risk = maxRisk(s.Risk, r)
		if r != RiskRead {
			s.Reasons = append(s.Reasons, why)
		}
	}
	for _, n := range st.nested {
		inner := classifyStatement(engine, n)
		note(inner.Risk, "in a /*! */ comment: "+strings.Join(inner.Reasons, ", "))
	}
	t := st.tokens
	if len(t) == 0 {
		return s
	}
	words := func(from int) []string {
		var w []string
		for _, x := range t[from:] {
			if x.kind == 'w' {
				w = append(w, x.val)
			}
		}
		return w
	}
	first := t[0].val
	if t[0].kind == 'p' && first == `\` {
		note(RiskChange, "psql meta-commands are not SQL")
		return s
	}
	if t[0].kind == 'p' && first == "(" { // (SELECT ...) UNION ...
		for _, x := range t {
			if x.kind == 'w' {
				first = x.val
				break
			}
		}
	}
	all := words(0)
	has := func(w ...string) bool {
		for _, a := range all {
			for _, b := range w {
				if a == b {
					return true
				}
			}
		}
		return false
	}
	// functions called anywhere in the statement
	for i, x := range t {
		if (x.kind == 'w' || x.kind == 'i') && i+1 < len(t) && t[i+1].kind == 'p' && t[i+1].val == "(" && adminFunctions[x.val] {
			note(RiskChange, strings.ToLower(x.val)+"() acts outside the transaction")
		}
	}
	returning := has("RETURNING")
	switch {
	case first == "EXPLAIN":
		analyze := false
		rest := 1
		if len(t) > 1 && t[1].kind == 'p' && t[1].val == "(" { // EXPLAIN (ANALYZE, BUFFERS) ...
			j := 2
			for j < len(t) && !(t[j].kind == 'p' && t[j].val == ")") {
				if t[j].val == "ANALYZE" && !(j+1 < len(t) && in(t[j+1].val, "FALSE", "OFF", "0")) {
					analyze = true
				}
				j++
			}
			rest = j + 1
		} else {
			for rest < len(t) && t[rest].kind == 'w' && in(t[rest].val, "ANALYZE", "VERBOSE", "EXTENDED", "PARTITIONS", "FORMAT", "=", "JSON", "TREE", "TRADITIONAL") {
				if t[rest].val == "ANALYZE" {
					analyze = true
				}
				rest++
			}
		}
		s.ReturnsRows = true
		if analyze && rest < len(t) {
			inner := classifyStatement(engine, rawStatement{text: st.text, tokens: t[rest:]})
			if inner.Risk != RiskRead {
				note(inner.Risk, "EXPLAIN ANALYZE runs the statement")
				s.Reasons = append(s.Reasons, inner.Reasons...)
			}
		}
		return s
	case first == "SELECT" || first == "VALUES" || first == "TABLE" || first == "WITH":
		s.ReturnsRows = true
		for i, x := range t {
			if x.kind != 'w' {
				continue
			}
			switch x.val {
			case "INTO":
				if engine == "postgres" {
					note(RiskChange, "SELECT INTO creates a table")
				} else if i+1 < len(t) && in(t[i+1].val, "OUTFILE", "DUMPFILE") {
					note(RiskChange, "SELECT INTO "+t[i+1].val+" writes a file on the server")
				}
			case "FOR":
				if i+1 < len(t) && in(t[i+1].val, "UPDATE", "SHARE", "NO", "KEY") {
					note(RiskChange, "SELECT … FOR UPDATE/SHARE locks rows")
				}
			case "LOCK":
				if i+2 < len(t) && t[i+1].val == "IN" { // LOCK IN SHARE MODE
					note(RiskChange, "locks rows")
				}
			}
		}
		if first == "WITH" { // data-modifying common table expressions
			for _, w := range all {
				if r, ok := dmlWords[w]; ok {
					note(r, "WITH … "+w+" modifies data")
					s.ReturnsRows = true
				}
			}
			if (has("DELETE") || has("UPDATE")) && !has("WHERE") {
				note(RiskDestructive, "WITH … DELETE/UPDATE without WHERE changes every row")
			}
		}
		return s
	case first == "SHOW" || first == "DESCRIBE" || first == "DESC" || first == "HELP":
		s.ReturnsRows = true
		return s
	case first == "CHECK" || first == "CHECKSUM": // MySQL CHECK TABLE / CHECKSUM TABLE
		s.ReturnsRows = true
		if len(all) > 1 && all[1] != "TABLE" {
			note(RiskChange, strings.ToLower(first)+" "+strings.ToLower(all[1]))
		}
		return s
	case first == "USE" || first == "RESET":
		if first == "RESET" && len(all) > 1 && in(all[1], "MASTER", "SLAVE", "REPLICA", "BINARY", "PERSIST") {
			note(RiskDestructive, "RESET "+all[1]+" erases replication state or logs")
		}
		return s
	case first == "SET":
		// session settings only: SET GLOBAL / PERSIST / PASSWORD change the server
		for _, w := range all {
			if in(w, "GLOBAL", "PERSIST", "PERSIST_ONLY", "PASSWORD") {
				note(RiskChange, "SET "+w+" changes the server")
			}
		}
		for i, x := range t {
			if x.kind == 'p' && x.val == "@" && i+2 < len(t) && t[i+1].val == "@" && in(t[i+2].val, "GLOBAL", "PERSIST", "PERSIST_ONLY") {
				note(RiskChange, "SET @@"+strings.ToLower(t[i+2].val)+" changes the server")
			}
		}
		return s
	case in(first, "BEGIN", "START", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "END", "ABORT", "PREPARE", "XA"):
		note(RiskChange, "transaction control (opsdeck runs each script in its own transaction)")
		s.TxControl = first != "PREPARE"
		return s
	case first == "DROP":
		note(RiskDestructive, "DROP "+strings.Join(all[1:min(len(all), 2)], ""))
		return s
	case first == "TRUNCATE":
		note(RiskDestructive, "TRUNCATE removes all rows")
		return s
	case first == "DELETE":
		s.ReturnsRows = returning
		if !hasTopLevel(t, "WHERE") {
			note(RiskDestructive, "DELETE without WHERE removes every row")
		} else {
			note(RiskChange, "DELETE removes rows")
		}
		return s
	case first == "UPDATE":
		s.ReturnsRows = returning
		if !hasTopLevel(t, "WHERE") {
			note(RiskDestructive, "UPDATE without WHERE changes every row")
		} else {
			note(RiskChange, "UPDATE changes rows")
		}
		return s
	case first == "ALTER":
		if has("DROP") {
			note(RiskDestructive, "ALTER … DROP removes a column, constraint or partition")
		} else {
			note(RiskChange, "ALTER changes the schema")
		}
		return s
	case first == "DO" || first == "CREATE" && has("FUNCTION", "PROCEDURE", "TRIGGER"):
		note(RiskChange, strings.ToLower(first)+" runs or stores code")
		if engine == "mysql" && has("DROP", "TRUNCATE") {
			note(RiskDestructive, "the routine drops or erases data")
		}
		for _, x := range t {
			if x.kind == 'd' || (x.kind == 's' && first == "DO") {
				for _, b := range splitStatements(engine, x.val) {
					for _, y := range b.tokens {
						if y.kind == 'w' && in(y.val, "DROP", "TRUNCATE") {
							note(RiskDestructive, "the code block drops or erases data")
						}
					}
					if inner := classifyStatement(engine, b); inner.Risk == RiskDestructive {
						note(RiskDestructive, "the code block drops or erases data")
					}
				}
			}
		}
		return s
	case first == "COPY":
		if has("FROM") {
			note(RiskChange, "COPY FROM loads data")
		} else {
			note(RiskChange, "COPY TO writes a file or needs the copy protocol")
		}
		return s
	case in(first, "SHUTDOWN", "RESTART", "PURGE"):
		note(RiskDestructive, first+" stops the server or erases logs")
		return s
	case in(first, "INSERT", "MERGE", "REPLACE", "UPSERT"):
		s.ReturnsRows = returning
		note(RiskChange, first+" modifies data")
		return s
	}
	if first == "KILL" {
		note(RiskChange, "KILL ends a session or query")
		return s
	}
	note(RiskChange, first+" is not a read")
	return s
}

// hasTopLevel reports whether a word appears outside parentheses.
func hasTopLevel(t []token, word string) bool {
	depth := 0
	for _, x := range t {
		switch {
		case x.kind == 'p' && x.val == "(":
			depth++
		case x.kind == 'p' && x.val == ")":
			depth--
		case depth == 0 && x.kind == 'w' && x.val == word:
			return true
		}
	}
	return false
}

func in(s string, list ...string) bool {
	for _, x := range list {
		if s == x {
			return true
		}
	}
	return false
}
