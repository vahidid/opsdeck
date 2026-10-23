package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"opsdeck/internal/dbx"
	"opsdeck/internal/llm"
)

func toolDatabase() Tool {
	return Tool{
		Def: llm.Tool{Name: "database", Description: `Work with a workspace database (Database resources: PostgreSQL, MySQL, MariaDB). opsdeck logs in with the resource's vault login and reaches it through its VPN, jump host or an SSH tunnel to the host it runs on — never run psql/mysql with passwords through exec.
Actions: databases (the databases on the server), tables (tables and views with estimated rows and size), describe (columns, keys and indexes of a table), query (SQL; several statements separated by ;).
Reads (SELECT, SHOW, EXPLAIN, WITH … SELECT) run directly, in a read-only transaction. Anything else (INSERT, UPDATE, DELETE, DDL, GRANT, …) waits for the user's approval and runs as one transaction: if a statement fails nothing is committed. Look at describe before writing queries, use LIMIT, and give a change as one script.`,
			Schema: obj(map[string]any{
				"database": str("Database resource name"),
				"action":   enum("Action", "databases", "tables", "describe", "query"),
				"db":       str("Database on the server to use (default: the resource's database)"),
				"schema":   str("describe: schema (PostgreSQL, default public) or database (MySQL)"),
				"table":    str("describe: table or view name"),
				"sql":      str("query: SQL script"),
				"max_rows": num("query: rows per result (default 100, max 1000)"),
			}, "database", "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Database, Action, DB, Schema, Table, SQL string
				MaxRows                                  int `json:"max_rows"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			res := env.WS.Find("Database", strings.TrimPrefix(in.Database, "database/"))
			if res == nil {
				var names []string
				for _, r := range env.WS.Resources {
					if r.Kind == "Database" {
						names = append(names, r.Name)
					}
				}
				return nil, fmt.Errorf("database %q not found; databases: %s", in.Database, strings.Join(names, ", "))
			}
			engine, err := dbx.Engine(res)
			if err != nil {
				return nil, err
			}
			if in.MaxRows <= 0 {
				in.MaxRows = 100
			}
			if in.MaxRows > 1000 {
				in.MaxRows = 1000
			}
			envName, protected := targetInfo(env.WS, res)
			c := &Call{Risk: RiskRead, Target: "database/" + res.Name, Environment: envName, Protected: protected, Match: in.Action}
			if ref := res.Str("login"); ref != "" {
				c.Credentials = []string{ref}
			}
			on := res.ID()
			if in.DB != "" {
				on += " (database " + in.DB + ")"
			}
			switch in.Action {
			case "databases", "tables":
			case "describe":
				if in.Table == "" {
					return nil, fmt.Errorf("describe needs table")
				}
				if in.Schema == "" && engine == "postgres" {
					in.Schema = "public"
				}
			case "query":
				if strings.TrimSpace(in.SQL) == "" {
					return nil, fmt.Errorf("query needs sql")
				}
				cls := dbx.Classify(engine, in.SQL)
				c.Risk, c.Reasons, c.Match = cls.Risk, cls.Reasons, in.SQL
				c.Preview = in.SQL + "\n\non: " + on
				if c.Risk != RiskRead {
					c.Preview += "\nruns as one transaction: nothing is committed if a statement fails"
				}
			default:
				return nil, fmt.Errorf("unknown action %q", in.Action)
			}
			c.Run = func(ctx context.Context) (string, error) {
				conn, err := env.DB.Conn(ctx, env.WS, res, in.DB)
				if err != nil {
					return "", err
				}
				if in.Schema == "" && engine == "mysql" {
					in.Schema = conn.Database
				}
				switch in.Action {
				case "databases":
					l, err := conn.Databases(ctx)
					return strings.Join(l, "\n"), err
				case "tables":
					l, err := conn.Tables(ctx)
					if err != nil {
						return "", err
					}
					rows := [][]any{}
					for _, t := range l {
						est := "?"
						if t.Rows >= 0 {
							est = fmt.Sprint(t.Rows)
						}
						rows = append(rows, []any{t.Schema + "." + t.Name, t.Type, est, humanBytes(t.Bytes)})
					}
					return textTable([]string{"table", "type", "rows (estimate)", "size"}, rows, 200), nil
				case "describe":
					ti, err := conn.Table(ctx, in.Schema, in.Table)
					if err != nil {
						return "", err
					}
					rows := [][]any{}
					for _, col := range ti.Columns {
						def := ""
						if col.Default != nil {
							def = *col.Default
						}
						key := ""
						if col.Primary {
							key = "PK"
						}
						rows = append(rows, []any{col.Name, col.Type, map[bool]string{true: "null", false: "not null"}[col.Nullable], def, key})
					}
					out := textTable([]string{"column", "type", "null", "default", "key"}, rows, 0)
					if len(ti.Indexes) > 0 {
						out += "\n\nindexes:"
						for _, ix := range ti.Indexes {
							out += "\n- " + ix.Name + ": " + ix.Definition
						}
					}
					return out, nil
				}
				_, results, err := conn.Run(ctx, in.SQL, dbx.QueryOptions{Write: c.Risk != RiskRead, MaxRows: in.MaxRows})
				return formatResults(results, len(results) > 1, c.Risk != RiskRead && err == nil), err
			}
			return c, nil
		},
	}
}

// formatResults renders query results as text tables for the model.
func formatResults(results []dbx.Result, numbered, committed bool) string {
	var b strings.Builder
	for i, r := range results {
		if numbered {
			if i > 0 {
				b.WriteString("\n\n")
			}
			st := strings.Join(strings.Fields(r.Statement), " ")
			if len(st) > 120 {
				st = st[:120] + "…"
			}
			fmt.Fprintf(&b, "-- %d: %s\n", i+1, st)
		}
		switch {
		case r.Error != "":
			b.WriteString("ERROR: " + r.Error)
		case r.Columns != nil:
			names := make([]string, len(r.Columns))
			for j, c := range r.Columns {
				names[j] = c.Name
			}
			b.WriteString(textTable(names, r.Rows, 0))
			n := fmt.Sprintf("\n(%d row%s", len(r.Rows), map[bool]string{true: "", false: "s"}[len(r.Rows) == 1])
			if r.Truncated {
				n += ", more not shown: use LIMIT or a narrower query"
			}
			b.WriteString(n + ")")
		default:
			fmt.Fprintf(&b, "OK, %d row(s) affected", r.RowsAffected)
		}
	}
	if committed {
		b.WriteString("\n\ncommitted")
	}
	return b.String()
}

// textTable aligns rows under a header; cells are cut at 200 characters
// and line breaks shown as ⏎. maxRows 0 = all.
func textTable(header []string, rows [][]any, maxRows int) string {
	cell := func(v any) string {
		if v == nil {
			return "NULL"
		}
		s := strings.ReplaceAll(fmt.Sprint(v), "\n", "⏎")
		if r := []rune(s); len(r) > 200 {
			s = string(r[:200]) + "…"
		}
		return s
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	grid := [][]string{header}
	for _, r := range rows {
		line := make([]string, len(header))
		for i := range header {
			if i < len(r) {
				line[i] = cell(r[i])
			}
		}
		grid = append(grid, line)
	}
	width := make([]int, len(header))
	for _, line := range grid {
		for i, s := range line {
			width[i] = max(width[i], len([]rune(s)))
		}
	}
	var b strings.Builder
	for n, line := range grid {
		for i, s := range line {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(s)
			if i < len(line)-1 {
				b.WriteString(strings.Repeat(" ", width[i]-len([]rune(s))))
			}
		}
		b.WriteString("\n")
		if n == 0 {
			for i := range line {
				if i > 0 {
					b.WriteString("-+-")
				}
				b.WriteString(strings.Repeat("-", width[i]))
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}
