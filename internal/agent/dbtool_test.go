package agent

import (
	"testing"

	"opsdeck/internal/dbx"
)

func TestFormatResults(t *testing.T) {
	got := formatResults([]dbx.Result{
		{Statement: "select id, name from users", Columns: []dbx.ResultColumn{{Name: "id"}, {Name: "name"}}, Rows: [][]any{{int64(1), "Ana"}, {int64(22), nil}}, Truncated: true},
		{Statement: "update users set name = 'x' where id = 1", RowsAffected: 1},
		{Statement: "select 1/0", Error: "division by zero"},
	}, true, false)
	want := `-- 1: select id, name from users
id | name
---+-----
1  | Ana
22 | NULL
(2 rows, more not shown: use LIMIT or a narrower query)

-- 2: update users set name = 'x' where id = 1
OK, 1 row(s) affected

-- 3: select 1/0
ERROR: division by zero`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
