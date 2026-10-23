package dbx

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct{ engine, sql, want string }{
		// reads
		{"postgres", "select * from orders where id = 1", RiskRead},
		{"postgres", "SELECT count(*) FROM users; SELECT now()", RiskRead},
		{"postgres", "with t as (select 1) select * from t", RiskRead},
		{"postgres", "(select 1) union (select 2)", RiskRead},
		{"postgres", "explain select * from orders", RiskRead},
		{"postgres", "explain delete from orders", RiskRead}, // a plan only
		{"postgres", "EXPLAIN (ANALYZE, BUFFERS) SELECT 1", RiskRead},
		{"postgres", "show search_path", RiskRead},
		{"postgres", "set search_path to app", RiskRead},
		{"postgres", "table orders", RiskRead},
		{"postgres", "values (1), (2)", RiskRead},
		{"postgres", "select 'drop table x; delete from y' as s", RiskRead},
		{"postgres", "select $$drop table x$$", RiskRead},
		{"postgres", "-- drop table x\nselect 1", RiskRead},
		{"postgres", "/* delete from x; */ select 1", RiskRead},
		{"postgres", `select "drop" from t`, RiskRead},
		{"postgres", "select pg_size_pretty(pg_database_size(current_database()))", RiskRead},
		{"mysql", "SHOW TABLES", RiskRead},
		{"mysql", "describe orders", RiskRead},
		{"mysql", "select * from `orders` limit 10", RiskRead},
		{"mysql", "use shop; select 1", RiskRead},
		{"mysql", "SELECT 'a\\'; drop table x; --' AS s", RiskRead},
		{"mysql", "select 1 # ; drop table x", RiskRead},
		{"mysql", "check table orders", RiskRead},
		{"mysql", "SELECT x INTO @v FROM t", RiskRead},
		// changes
		{"postgres", "insert into t values (1)", RiskChange},
		{"postgres", "update t set a = 1 where id = 2", RiskChange},
		{"postgres", "delete from t where id = 2", RiskChange},
		{"postgres", "create index on t (a)", RiskChange},
		{"postgres", "select * into backup from t", RiskChange},
		{"postgres", "select * from t for update", RiskChange},
		{"postgres", "with d as (delete from t where id = 1 returning *) select * from d", RiskChange},
		{"postgres", "explain analyze delete from t where id = 1", RiskChange},
		{"postgres", "EXPLAIN (ANALYZE) UPDATE t SET a = 1 WHERE b = 2", RiskChange},
		{"postgres", "select pg_terminate_backend(123)", RiskChange},
		{"postgres", `select pg_catalog.pg_terminate_backend(pid) from pg_stat_activity`, RiskChange},
		{"postgres", `select "pg_terminate_backend"(1)`, RiskChange},
		{"postgres", "select pg_terminate_backend/**/(1)", RiskChange},
		{"postgres", "select lo_export(1, '/tmp/x')", RiskChange},
		{"postgres", "select pg_read_file('/etc/passwd')", RiskChange},
		{"postgres", "select query_to_xml('delete from t', true, true, '')", RiskChange},
		{"postgres", "select * from dblink('host=x', 'delete from t') as t(a int)", RiskChange},
		{"postgres", "select nextval('s')", RiskChange},
		{"postgres", "copy t to '/tmp/x'", RiskChange},
		{"postgres", "copy (select 1) to program 'id'", RiskChange},
		{"postgres", "commit; select 1", RiskChange},
		{"postgres", "vacuum t", RiskChange},
		{"postgres", "do $$ begin perform 1; end $$", RiskChange},
		{"postgres", `\dt`, RiskChange},
		{"postgres", "grant select on t to bob", RiskChange},
		{"mysql", "select * from t into outfile '/tmp/x'", RiskChange},
		{"mysql", "select load_file('/etc/passwd')", RiskChange},
		{"mysql", "SET GLOBAL max_connections = 10", RiskChange},
		{"mysql", "set @@global.read_only = 1", RiskChange},
		{"mysql", "kill 42", RiskChange},
		{"mysql", "/*!50000 insert into t values (1) */", RiskChange},
		{"mysql", "select 1 /*! ; insert into t values (1) */", RiskChange},
		{"mysql", "replace into t values (1)", RiskChange},
		{"mysql", "optimize table t", RiskChange},
		{"mysql", "start transaction; select 1", RiskChange},
		// destructive
		{"postgres", "drop table users", RiskDestructive},
		{"postgres", "select 1; drop table users", RiskDestructive},
		{"postgres", "truncate orders", RiskDestructive},
		{"postgres", "delete from orders", RiskDestructive},
		{"postgres", "update users set admin = true", RiskDestructive},
		{"postgres", "update t set a = (select 1 where true)", RiskDestructive}, // WHERE only in a subquery
		{"postgres", "alter table t drop column a", RiskDestructive},
		{"postgres", "with d as (delete from t returning *) select * from d", RiskDestructive},
		{"postgres", "do $$ begin drop table t; end $$", RiskDestructive},
		{"postgres", "create function f() returns void as $$ truncate t $$ language sql", RiskDestructive},
		{"mysql", "DROP DATABASE shop", RiskDestructive},
		{"mysql", "reset master", RiskDestructive},
		{"mysql", "purge binary logs before now()", RiskDestructive},
		{"mysql", "shutdown", RiskDestructive},
	}
	for _, c := range cases {
		got := Classify(c.engine, c.sql)
		if got.Risk != c.want {
			t.Errorf("%s %q: got %s want %s (%v)", c.engine, c.sql, got.Risk, c.want, got.Reasons)
		}
	}
}

func TestSplitStatements(t *testing.T) {
	c := Classify("postgres", "select 1;\n-- x;\nselect ';' ; insert into t values (1) returning id;\n\n")
	if len(c.Statements) != 3 {
		t.Fatalf("statements: %+v", c.Statements)
	}
	want := []struct {
		text string
		rows bool
	}{{"select 1", true}, {"-- x;\nselect ';'", true}, {"insert into t values (1) returning id", true}}
	for i, w := range want {
		if c.Statements[i].Text != w.text || c.Statements[i].ReturnsRows != w.rows {
			t.Errorf("%d: %q rows=%v", i, c.Statements[i].Text, c.Statements[i].ReturnsRows)
		}
	}
	if s := Classify("mysql", "update t set a=1 where id=2").Statements[0]; s.ReturnsRows {
		t.Error("update without RETURNING is an exec")
	}
}

func TestMySQLHints(t *testing.T) {
	for _, sql := range []string{"SELECT /*!40001 SQL_NO_CACHE */ * FROM t", "SELECT /*+ MAX_EXECUTION_TIME(1000) */ * FROM t"} {
		if c := Classify("mysql", sql); c.Risk != RiskRead {
			t.Errorf("%q: %s %v", sql, c.Risk, c.Reasons)
		}
	}
}

func TestInvalidUTF8(t *testing.T) {
	c := Classify("postgres", "\xc9; select 1")
	if len(c.Statements) != 2 {
		t.Errorf("statements: %+v", c.Statements)
	}
}

func TestMySQLRoutines(t *testing.T) {
	sql := `create procedure p() begin
  declare x int;
  case when 1 then set x = 1; else set x = 2; end case;
  if x > 0 then select x; end if;
  select case when x = 1 then 'a' else 'b' end;
end;
select 1`
	c := Classify("mysql", sql)
	if len(c.Statements) != 2 || c.Risk != RiskChange || c.Statements[1].Text != "select 1" {
		t.Fatalf("%s %+v", c.Risk, c.Statements)
	}
	if c := Classify("mysql", "create trigger t before insert on x for each row begin delete from y; end"); len(c.Statements) != 1 {
		t.Fatalf("trigger: %+v", c.Statements)
	}
	if c := Classify("mysql", "create procedure p() begin truncate x; end"); c.Risk != RiskDestructive {
		t.Fatalf("truncate in a routine: %s", c.Risk)
	}
}
