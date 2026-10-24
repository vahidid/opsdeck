package dbx

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"opsdeck/internal/workspace"
)

func TestParseMongo(t *testing.T) {
	stmts, err := parseMongo(`
		// orders of a customer
		db.orders.find({ customer: 'c-1', total: {$gt: 10.5}, tags: ["a", "b",], }, {_id: 0})
		  .sort({created: -1})
		  .limit(20);
		db.getCollection("audit-log").countDocuments({at: {$gte: ISODate("2026-01-02T00:00:00Z")}})
		db['a.b'].findOne({_id: ObjectId("65a1b2c3d4e5f60718293a4b")}) /* by id */
		db.system.users.find()
		show collections
		use reporting
		db.runCommand({ping: 1})
		db.x.find({name: /^ab[/]c/i, n: NumberLong("9007199254740993"), d: new Date("2026-05-01")})`)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 8 {
		t.Fatalf("got %d statements: %+v", len(stmts), stmts)
	}
	s := stmts[0]
	if s.Kind != "coll" || s.Coll != "orders" || len(s.Calls) != 3 || s.Calls[0].Name != "find" || s.Calls[2].Name != "limit" {
		t.Fatalf("find: %+v", s)
	}
	f := s.Calls[0].Args[0].(bson.D)
	if f[0].Key != "customer" || f[0].Value != "c-1" || len(f[2].Value.(bson.A)) != 2 {
		t.Fatalf("filter: %#v", f)
	}
	if !strings.HasPrefix(s.Text, "db.orders.find(") || !strings.HasSuffix(s.Text, ".limit(20)") {
		t.Fatalf("text: %q", s.Text)
	}
	if stmts[1].Coll != "audit-log" || stmts[2].Coll != "a.b" || stmts[3].Coll != "system.users" {
		t.Fatalf("collections: %q %q %q", stmts[1].Coll, stmts[2].Coll, stmts[3].Coll)
	}
	if _, ok := stmts[2].Calls[0].Args[0].(bson.D)[0].Value.(bson.ObjectID); !ok {
		t.Fatal("ObjectId not parsed")
	}
	if stmts[4].Kind != "show" || stmts[5].Kind != "use" || stmts[5].Arg != "reporting" || stmts[6].Kind != "db" {
		t.Fatalf("show/use/db: %+v %+v %+v", stmts[4], stmts[5], stmts[6])
	}
	last := stmts[7].Calls[0].Args[0].(bson.D)
	if re, ok := last[0].Value.(bson.Regex); !ok || re.Pattern != "^ab[/]c" || re.Options != "i" {
		t.Fatalf("regex: %#v", last[0].Value)
	}
	if last[1].Value != int64(9007199254740993) {
		t.Fatalf("NumberLong: %#v", last[1].Value)
	}
	if _, ok := last[2].Value.(bson.DateTime); !ok {
		t.Fatalf("Date: %#v", last[2].Value)
	}

	for _, bad := range []string{`db.orders.find({a: })`, `db.orders.find(`, `x = 1`, `db.orders.find({}) db.x.find()`, `db.orders.find({a: foo})`} {
		if _, err := parseMongo(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestClassifyMongo(t *testing.T) {
	cases := []struct{ script, risk string }{
		{`db.orders.find({}).sort({a: 1}).limit(5)`, RiskRead},
		{`db.orders.aggregate([{$match: {a: 1}}, {$group: {_id: "$b", n: {$sum: 1}}}])`, RiskRead},
		{`db.orders.countDocuments(); db.orders.distinct("status")`, RiskRead},
		{`show dbs`, RiskRead},
		{`db.runCommand({serverStatus: 1})`, RiskRead},
		{`db.getCollectionNames()`, RiskRead},
		{`db.orders.aggregate([{$match: {}}, {$out: "copy"}])`, RiskChange},
		{`db.runCommand({aggregate: "orders", pipeline: [{$merge: "x"}], cursor: {}})`, RiskChange},
		{`db.orders.insertOne({a: 1})`, RiskChange},
		{`db.orders.updateOne({_id: 1}, {$set: {a: 2}})`, RiskChange},
		{`db.orders.deleteMany({status: "old"})`, RiskChange},
		{`db.orders.createIndex({a: 1})`, RiskChange},
		{`db.runCommand({createUser: "x", pwd: "y", roles: []})`, RiskChange},
		{`db.orders.find().forEach(printjson)`, RiskChange},
		{`db.orders.mapReduce()`, RiskChange},
		{`db.orders.find({a: `, RiskChange},
		{`db.orders.deleteMany({})`, RiskDestructive},
		{`db.orders.updateMany({}, {$set: {a: 1}})`, RiskDestructive},
		{`db.orders.drop()`, RiskDestructive},
		{`db.dropDatabase()`, RiskDestructive},
		{`db.adminCommand({shutdown: 1})`, RiskDestructive},
		{`db.orders.find(); db.orders.drop()`, RiskDestructive},
	}
	for _, c := range cases {
		if got := Classify("mongodb", c.script); got.Risk != c.risk {
			t.Errorf("%s: %s, want %s (%v)", c.script, got.Risk, c.risk, got.Reasons)
		}
	}
}

func FuzzParseMongo(f *testing.F) {
	f.Add(`db.a.find({x: /a/, y: "é", z: [1, 2.5e3, -1]}).sort({x: -1})`)
	f.Add("db['a'].findOne(ObjectId('65a1b2c3d4e5f60718293a4b'))\nshow dbs; use x")
	f.Add("/* x */ db.a.aggregate([{$out: 'b'}]) // y")
	f.Fuzz(func(t *testing.T, s string) {
		parseMongo(s)
		Classify("mongodb", s)
	})
}

// OPSDECK_TEST_MONGO=user:pass@host:port[/db] runs this against a real server.
func TestMongoLive(t *testing.T) {
	spec := os.Getenv("OPSDECK_TEST_MONGO")
	if spec == "" {
		t.Skip("OPSDECK_TEST_MONGO not set")
	}
	cred, addr, _ := strings.Cut(spec, "@")
	addr, _, _ = strings.Cut(addr, "/") // the database is the test's own
	user, pass, _ := strings.Cut(cred, ":")
	ctx := context.Background()
	res := &workspace.Resource{Kind: "Database", Name: "m", Spec: map[string]any{"engine": "mongodb"}}
	s, err := openMongo(ctx, target{res: res, addr: addr, user: user, password: pass, database: "opsdeck_test", dial: (&net.Dialer{}).DialContext})
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	run := func(script string, write bool) []Result {
		t.Helper()
		_, r, err := s.Run(ctx, script, QueryOptions{Write: write})
		if err != nil {
			t.Fatalf("%s: %v (%+v)", script, err, r)
		}
		return r
	}
	run(`db.people.drop()`, true)
	r := run(`db.people.insertMany([{_id: 1, name: "Ada", langs: ["en", "fr"], born: ISODate("1815-12-10")},
		{_id: 2, name: "Linus", address: {city: "Helsinki"}}, {_id: 3, name: "Grace", n: NumberDecimal("1.50")}])`, true)
	if r[0].RowsAffected != 3 {
		t.Fatalf("insertMany: %+v", r)
	}
	if _, _, err := s.Run(ctx, `db.people.deleteOne({_id: 1})`, QueryOptions{}); err == nil {
		t.Fatal("a change ran as a read")
	}
	r = run(`db.people.find({}).sort({_id: 1})`, false)
	cols := []string{}
	for _, c := range r[0].Columns {
		cols = append(cols, c.Name)
	}
	if strings.Join(cols, ",") != "_id,name,langs,born,address,n" || len(r[0].Rows) != 3 {
		t.Fatalf("find: %v %+v", cols, r[0].Rows)
	}
	if raw, _ := r[0].Rows[0][2].(json.RawMessage); string(raw) != `["en","fr"]` || r[0].Rows[0][4] != nil || r[0].Rows[0][3] != "1815-12-10T00:00:00Z" {
		t.Fatalf("row: %#v", r[0].Rows[0])
	}
	r = run(`db.people.find().limit(2)`, false)
	if len(r[0].Rows) != 2 || r[0].Truncated {
		t.Fatalf("limit: %+v", r[0])
	}
	_, r, _ = s.Run(ctx, `db.people.find()`, QueryOptions{MaxRows: 2})
	if len(r[0].Rows) != 2 || !r[0].Truncated {
		t.Fatalf("max rows: %+v", r[0])
	}
	r = run(`db.people.aggregate([{$group: {_id: null, n: {$sum: 1}}}]); db.people.countDocuments({name: /a/i}); use admin; db.runCommand({ping: 1})`, false)
	if len(r) != 4 || r[1].Rows[0][0] != int64(2) {
		t.Fatalf("aggregate/count: %+v", r)
	}
	ts, err := s.Tables(ctx, "")
	if err != nil || len(ts) == 0 || ts[0].Name != "people" || ts[0].Rows != 3 || ts[0].Query != "db.people.find({}).limit(100)" {
		t.Fatalf("tables: %+v %v", ts, err)
	}
	ti, err := s.Table(ctx, "", "people")
	if err != nil || ti.Columns[0].Name != "_id" || len(ti.Indexes) != 1 {
		t.Fatalf("table: %+v %v", ti, err)
	}
	if _, err := s.Info(ctx); err != nil {
		t.Fatal(err)
	}
}
