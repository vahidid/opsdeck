package dbx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoConn is a client of one MongoDB server; database is the one scripts
// start in (use <db> changes it for the rest of a script).
type mongoConn struct {
	client   *mongo.Client
	database string
	address  string
	user     string
}

type dialAdapter struct{ t target }

func (d dialAdapter) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.t.dial(ctx, network, address)
}

func openMongo(ctx context.Context, t target) (Session, error) {
	o := options.Client().SetHosts([]string{t.addr}).SetDirect(true).SetAppName("opsdeck").
		SetDialer(dialAdapter{t}).SetConnectTimeout(20 * time.Second).SetServerSelectionTimeout(20 * time.Second).
		SetMaxPoolSize(4)
	if t.user != "" {
		src := t.res.Str("authSource")
		if src == "" {
			src = "admin"
		}
		o.SetAuth(options.Credential{Username: t.user, Password: t.password, AuthSource: src})
	}
	switch t.tls {
	case "require":
		o.SetTLSConfig(&tls.Config{InsecureSkipVerify: true})
	case "verify-full":
		host := t.addr
		if i := strings.LastIndex(host, ":"); i > 0 {
			host = host[:i]
		}
		o.SetTLSConfig(&tls.Config{ServerName: strings.Trim(host, "[]")})
	}
	client, err := mongo.Connect(o)
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		client.Disconnect(context.Background())
		return nil, err
	}
	db := t.database
	if db == "" {
		db = "admin"
	}
	return &mongoConn{client: client, database: db, address: t.addr, user: t.user}, nil
}

func (c *mongoConn) Engine() string   { return "mongodb" }
func (c *mongoConn) Database() string { return c.database }
func (c *mongoConn) close()           { c.client.Disconnect(context.Background()) }

func (c *mongoConn) Info(ctx context.Context) (Info, error) {
	in := Info{Engine: "mongodb", Address: c.address, Database: c.database, User: c.user}
	var bi struct {
		Version string `bson:"version"`
	}
	if err := c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&bi); err != nil {
		return in, err
	}
	in.Version = "MongoDB " + bi.Version
	var st struct {
		AuthInfo struct {
			Users []struct {
				User string `bson:"user"`
				DB   string `bson:"db"`
			} `bson:"authenticatedUsers"`
		} `bson:"authInfo"`
	}
	if c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}}).Decode(&st) == nil && len(st.AuthInfo.Users) > 0 {
		in.User = st.AuthInfo.Users[0].User + "@" + st.AuthInfo.Users[0].DB
	}
	return in, nil
}

func (c *mongoConn) Databases(ctx context.Context) ([]string, error) {
	l, err := c.client.ListDatabaseNames(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if err != nil {
		return nil, err
	}
	sort.Strings(l)
	return l, nil
}

// jsString quotes a collection name for a db.getCollection("…") call.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// collRef is how a script names a collection: db.orders or db.getCollection("a-b").
func collRef(name string) string {
	ok := name != "" && isIdentStart(rune(name[0]))
	for _, r := range name {
		if !isIdentPart(r) {
			ok = false
		}
	}
	if ok {
		return "db." + name
	}
	return "db.getCollection(" + jsString(name) + ")"
}

func (c *mongoConn) Tables(ctx context.Context, _ string) ([]Table, error) {
	db := c.client.Database(c.database)
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{}, options.ListCollections().SetAuthorizedCollections(true))
	if err != nil {
		return nil, err
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	if len(specs) > maxTables {
		specs = specs[:maxTables]
	}
	out := []Table{}
	for _, s := range specs {
		t := Table{Schema: c.database, Name: s.Name, Type: s.Type, Rows: -1, Query: collRef(s.Name) + ".find({}).limit(100)"}
		if t.Type == "" {
			t.Type = "collection"
		}
		if t.Type == "collection" && len(specs) <= 500 {
			var st struct {
				Count int64 `bson:"count"`
				Size  int64 `bson:"storageSize"`
				Index int64 `bson:"totalIndexSize"`
			}
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if db.RunCommand(sctx, bson.D{{Key: "collStats", Value: s.Name}}).Decode(&st) == nil {
				t.Rows, t.Bytes = st.Count, st.Size+st.Index
			} else if n, err := db.Collection(s.Name).EstimatedDocumentCount(sctx); err == nil {
				t.Rows = n
			}
			cancel()
		}
		out = append(out, t)
	}
	return out, nil
}

// Table infers the fields of a collection from a sample of its documents.
func (c *mongoConn) Table(ctx context.Context, schema, name string) (*TableInfo, error) {
	if schema == "" {
		schema = c.database
	}
	coll := c.client.Database(schema).Collection(name)
	ti := &TableInfo{Schema: schema, Name: name, Kind: "collection", Columns: []Column{}, Indexes: []Index{}}
	const sample = 200
	cur, err := coll.Find(ctx, bson.D{}, options.Find().SetLimit(sample).SetSort(bson.D{{Key: "$natural", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	type field struct {
		types map[string]int
		seen  int
		order int
	}
	fields := map[string]*field{}
	n := 0
	for cur.Next(ctx) {
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		n++
		for _, e := range d {
			f := fields[e.Key]
			if f == nil {
				f = &field{types: map[string]int{}, order: len(fields)}
				fields[e.Key] = f
			}
			f.seen++
			f.types[bsonType(e.Value)]++
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == "_id") != (names[j] == "_id") {
			return names[i] == "_id"
		}
		return fields[names[i]].order < fields[names[j]].order
	})
	for _, k := range names {
		f := fields[k]
		var types []string
		for t := range f.types {
			types = append(types, t)
		}
		sort.Slice(types, func(i, j int) bool { return f.types[types[i]] > f.types[types[j]] })
		col := Column{Name: k, Type: strings.Join(types, " | "), Nullable: f.seen < n || f.types["null"] > 0, Primary: k == "_id"}
		if f.seen < n {
			pct := strconv.Itoa(f.seen*100/n) + "% of documents"
			col.Default = &pct
		}
		ti.Columns = append(ti.Columns, col)
	}
	if n == 0 {
		ti.Note = "the collection is empty: no fields to show"
	} else {
		ti.Note = fmt.Sprintf("fields of the %d most recent documents; the presence column shows fields that are not in every document", n)
	}
	icur, err := coll.Indexes().List(ctx)
	if err == nil {
		defer icur.Close(ctx)
		for icur.Next(ctx) {
			var ix struct {
				Name   string `bson:"name"`
				Key    bson.D `bson:"key"`
				Unique bool   `bson:"unique"`
			}
			if icur.Decode(&ix) != nil {
				continue
			}
			def, _ := bson.MarshalExtJSON(ix.Key, false, false)
			ti.Indexes = append(ti.Indexes, Index{Name: ix.Name, Definition: string(def), Unique: ix.Unique || ix.Name == "_id_", Primary: ix.Name == "_id_"})
		}
	}
	var st struct {
		Count int64 `bson:"count"`
		Size  int64 `bson:"size"`
		Avg   int64 `bson:"avgObjSize"`
		Store int64 `bson:"storageSize"`
		Index int64 `bson:"totalIndexSize"`
	}
	if c.client.Database(schema).RunCommand(ctx, bson.D{{Key: "collStats", Value: name}}).Decode(&st) == nil {
		ti.Properties = []Property{{"documents", strconv.FormatInt(st.Count, 10)}, {"data size", byteSize(st.Size)},
			{"average document", byteSize(st.Avg)}, {"storage", byteSize(st.Store)}, {"indexes", byteSize(st.Index)}}
	}
	return ti, nil
}

func bsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case int32:
		return "int"
	case int64:
		return "long"
	case float64:
		return "double"
	case bool:
		return "bool"
	case bson.ObjectID:
		return "objectId"
	case bson.DateTime:
		return "date"
	case bson.Decimal128:
		return "decimal"
	case bson.D, bson.M:
		return "object"
	case bson.A:
		return "array"
	case bson.Binary:
		return "binary"
	case bson.Timestamp:
		return "timestamp"
	case bson.Regex:
		return "regex"
	}
	return fmt.Sprintf("%T", v)
}

// Run runs a MongoDB script statement by statement. MongoDB has no
// transaction over arbitrary commands here: a change script stops at its
// first error and what ran before stays.
func (c *mongoConn) Run(ctx context.Context, script string, opts QueryOptions) (Classification, []Result, error) {
	opts.defaults()
	cls := classifyMongo(script)
	stmts, err := parseMongo(script)
	if err != nil {
		return cls, nil, err
	}
	if len(stmts) == 0 {
		return cls, nil, errors.New("no statement")
	}
	if !opts.Write && cls.Risk != RiskRead {
		return cls, nil, &NotReadError{cls}
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	db := c.database
	budget := maxResultBytes
	results := []Result{}
	for i, st := range stmts {
		res := Result{Statement: st.Text, RowsAffected: -1}
		start := time.Now()
		// a read run as part of a change script stays a read
		err := c.exec(ctx, &db, st, opts, cls.Statements[i].Risk != RiskRead, &budget, &res)
		res.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			if opts.Write && i > 0 {
				return cls, results, fmt.Errorf("%w (the statements before it ran; MongoDB scripts are not one transaction)", err)
			}
			return cls, results, err
		}
		results = append(results, res)
	}
	return cls, results, nil
}

func argDoc(args []any, i int) (bson.D, error) {
	if i >= len(args) || args[i] == nil {
		return bson.D{}, nil
	}
	d, ok := args[i].(bson.D)
	if !ok {
		return nil, fmt.Errorf("argument %d must be a document", i+1)
	}
	return d, nil
}

func argNum(args []any, i int) (int64, bool) {
	if i >= len(args) {
		return 0, false
	}
	switch v := args[i].(type) {
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		if v == math.Trunc(v) {
			return int64(v), true
		}
	}
	return 0, false
}

func (c *mongoConn) exec(ctx context.Context, dbName *string, st mongoStmt, opts QueryOptions, write bool, budget *int, res *Result) error {
	db := c.client.Database(*dbName)
	switch st.Kind {
	case "show":
		if st.Arg == "dbs" {
			l, err := c.client.ListDatabases(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
			if err != nil {
				return err
			}
			res.Columns = []ResultColumn{{"name", "string"}, {"sizeOnDisk", "long"}, {"empty", "bool"}}
			res.Rows = [][]any{}
			for _, d := range l.Databases {
				res.Rows = append(res.Rows, []any{d.Name, d.SizeOnDisk, d.Empty})
			}
			return nil
		}
		names, err := db.ListCollectionNames(ctx, bson.D{})
		if err != nil {
			return err
		}
		sort.Strings(names)
		res.Columns = []ResultColumn{{"name", "string"}}
		res.Rows = [][]any{}
		for _, n := range names {
			res.Rows = append(res.Rows, []any{n})
		}
		return nil
	case "use":
		*dbName = st.Arg
		res.Note = "switched to database " + st.Arg
		return nil
	case "db":
		return c.dbCall(ctx, db, st.Calls[0], opts, budget, res)
	}
	return c.collCall(ctx, db.Collection(st.Coll), st, opts, write, budget, res)
}

func (c *mongoConn) dbCall(ctx context.Context, db *mongo.Database, op mongoCall, opts QueryOptions, budget *int, res *Result) error {
	var r *mongo.SingleResult
	switch op.Name {
	case "getCollectionNames":
		names, err := db.ListCollectionNames(ctx, bson.D{})
		if err != nil {
			return err
		}
		sort.Strings(names)
		a := bson.A{}
		for _, n := range names {
			a = append(a, n)
		}
		return docRows(res, []bson.D{{{Key: "names", Value: a}}}, opts, budget)
	case "getCollectionInfos":
		f, err := argDoc(op.Args, 0)
		if err != nil {
			return err
		}
		cur, err := db.ListCollections(ctx, f)
		if err != nil {
			return err
		}
		return cursorRows(ctx, res, cur, opts, budget)
	case "getName":
		return docRows(res, []bson.D{{{Key: "name", Value: db.Name()}}}, opts, budget)
	case "stats":
		r = db.RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}})
	case "version":
		var bi struct {
			Version string `bson:"version"`
		}
		if err := c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&bi); err != nil {
			return err
		}
		return docRows(res, []bson.D{{{Key: "version", Value: bi.Version}}}, opts, budget)
	case "serverStatus":
		r = db.RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}})
	case "hostInfo":
		r = c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "hostInfo", Value: 1}})
	case "runCommand", "adminCommand":
		cmd, err := argDoc(op.Args, 0)
		if err != nil || len(cmd) == 0 {
			return errors.New(op.Name + " needs a command document")
		}
		on := db
		if op.Name == "adminCommand" {
			on = c.client.Database("admin")
		}
		// commands that return a cursor (find, aggregate, listCollections…)
		if lookup(cmd, "cursor") != nil || in(strings.ToLower(cmd[0].Key), "find", "listcollections", "listindexes") {
			cur, err := on.RunCommandCursor(ctx, cmd)
			if err == nil {
				return cursorRows(ctx, res, cur, opts, budget)
			}
		}
		r = on.RunCommand(ctx, cmd)
	case "dropDatabase":
		if err := db.Drop(ctx); err != nil {
			return err
		}
		res.Note = "dropped database " + db.Name()
		return nil
	case "createCollection":
		name, ok := firstString(op.Args)
		if !ok {
			return errors.New("createCollection needs a name")
		}
		if err := db.CreateCollection(ctx, name); err != nil {
			return err
		}
		res.Note = "created collection " + name
		return nil
	default:
		return fmt.Errorf("db.%s() is not supported", op.Name)
	}
	var d bson.D
	if err := r.Decode(&d); err != nil {
		return err
	}
	return docRows(res, []bson.D{d}, opts, budget)
}

func (c *mongoConn) collCall(ctx context.Context, coll *mongo.Collection, st mongoStmt, opts QueryOptions, write bool, budget *int, res *Result) error {
	op := st.Calls[0]
	limit := int64(opts.MaxRows)
	switch op.Name {
	case "find", "findOne":
		f, err := argDoc(op.Args, 0)
		if err != nil {
			return err
		}
		fo := options.Find()
		if len(op.Args) > 1 {
			p, err := argDoc(op.Args, 1)
			if err != nil {
				return err
			}
			fo.SetProjection(p)
		}
		userLimit := int64(-1)
		count := false
		for _, m := range st.Calls[1:] {
			switch m.Name {
			case "sort":
				d, err := argDoc(m.Args, 0)
				if err != nil {
					return err
				}
				fo.SetSort(d)
			case "limit":
				if n, ok := argNum(m.Args, 0); ok && n > 0 {
					userLimit = n
				}
			case "skip":
				if n, ok := argNum(m.Args, 0); ok {
					fo.SetSkip(n)
				}
			case "projection":
				d, err := argDoc(m.Args, 0)
				if err != nil {
					return err
				}
				fo.SetProjection(d)
			case "hint":
				if len(m.Args) > 0 {
					fo.SetHint(m.Args[0])
				}
			case "maxTimeMS":
				// the script's timeout applies
			case "count", "itcount":
				count = true
			}
		}
		if count {
			n, err := coll.CountDocuments(ctx, f)
			if err != nil {
				return err
			}
			return docRows(res, []bson.D{{{Key: "count", Value: n}}}, opts, budget)
		}
		if op.Name == "findOne" {
			userLimit = 1
		}
		// one more than shown, to tell that there are more
		if userLimit > 0 && userLimit <= limit {
			fo.SetLimit(userLimit)
		} else {
			fo.SetLimit(limit + 1)
		}
		cur, err := coll.Find(ctx, f, fo)
		if err != nil {
			return err
		}
		return cursorRows(ctx, res, cur, opts, budget)
	case "aggregate":
		var pipeline bson.A
		if len(op.Args) > 0 {
			p, ok := op.Args[0].(bson.A)
			if !ok {
				return errors.New("aggregate needs a pipeline: [ {…}, … ]")
			}
			pipeline = p
		}
		if hasWriteStage(pipeline) && !write {
			return errors.New("$out and $merge write a collection")
		}
		cur, err := coll.Aggregate(ctx, pipeline)
		if err != nil {
			return err
		}
		return cursorRows(ctx, res, cur, opts, budget)
	case "countDocuments", "count":
		f, err := argDoc(op.Args, 0)
		if err != nil {
			return err
		}
		n, err := coll.CountDocuments(ctx, f)
		if err != nil {
			return err
		}
		return docRows(res, []bson.D{{{Key: "count", Value: n}}}, opts, budget)
	case "estimatedDocumentCount":
		n, err := coll.EstimatedDocumentCount(ctx)
		if err != nil {
			return err
		}
		return docRows(res, []bson.D{{{Key: "count", Value: n}}}, opts, budget)
	case "distinct":
		field, ok := firstString(op.Args)
		if !ok {
			return errors.New("distinct needs a field name")
		}
		f, err := argDoc(op.Args, 1)
		if err != nil {
			return err
		}
		var vals bson.A
		if err := coll.Distinct(ctx, field, f).Decode(&vals); err != nil {
			return err
		}
		docs := make([]bson.D, 0, len(vals))
		for _, v := range vals {
			docs = append(docs, bson.D{{Key: field, Value: v}})
		}
		return docRows(res, docs, opts, budget)
	case "getIndexes":
		cur, err := coll.Indexes().List(ctx)
		if err != nil {
			return err
		}
		return cursorRows(ctx, res, cur, opts, budget)
	case "stats":
		var d bson.D
		if err := coll.Database().RunCommand(ctx, bson.D{{Key: "collStats", Value: coll.Name()}}).Decode(&d); err != nil {
			return err
		}
		return docRows(res, []bson.D{d}, opts, budget)
	}
	if !write {
		return fmt.Errorf("%s is not a read", op.Name)
	}
	return c.collWrite(ctx, coll, op, opts, budget, res)
}

func (c *mongoConn) collWrite(ctx context.Context, coll *mongo.Collection, op mongoCall, opts QueryOptions, budget *int, res *Result) error {
	upsert := func(i int) bool {
		o, _ := argDoc(op.Args, i)
		b, _ := lookup(o, "upsert").(bool)
		return b
	}
	switch op.Name {
	case "insertOne":
		d, err := argDoc(op.Args, 0)
		if err != nil {
			return err
		}
		r, err := coll.InsertOne(ctx, d)
		if err != nil {
			return err
		}
		res.RowsAffected = 1
		res.Note = "inserted _id " + idString(r.InsertedID)
		return nil
	case "insertMany":
		a, ok := firstArray(op.Args)
		if !ok {
			return errors.New("insertMany needs an array of documents")
		}
		r, err := coll.InsertMany(ctx, a)
		if err != nil {
			return err
		}
		res.RowsAffected = int64(len(r.InsertedIDs))
		return nil
	case "updateOne", "updateMany", "replaceOne":
		f, err := argDoc(op.Args, 0)
		if err != nil {
			return err
		}
		if len(op.Args) < 2 {
			return errors.New(op.Name + " needs a filter and an update")
		}
		var r *mongo.UpdateResult
		switch op.Name {
		case "updateOne":
			r, err = coll.UpdateOne(ctx, f, op.Args[1], options.UpdateOne().SetUpsert(upsert(2)))
		case "updateMany":
			r, err = coll.UpdateMany(ctx, f, op.Args[1], options.UpdateMany().SetUpsert(upsert(2)))
		default:
			r, err = coll.ReplaceOne(ctx, f, op.Args[1], options.Replace().SetUpsert(upsert(2)))
		}
		if err != nil {
			return err
		}
		res.RowsAffected = r.ModifiedCount + r.UpsertedCount
		res.Note = fmt.Sprintf("matched %d, modified %d", r.MatchedCount, r.ModifiedCount)
		if r.UpsertedID != nil {
			res.Note += ", upserted _id " + idString(r.UpsertedID)
		}
		return nil
	case "deleteOne", "deleteMany":
		f, err := argDoc(op.Args, 0)
		if err != nil {
			return err
		}
		var r *mongo.DeleteResult
		if op.Name == "deleteOne" {
			r, err = coll.DeleteOne(ctx, f)
		} else {
			r, err = coll.DeleteMany(ctx, f)
		}
		if err != nil {
			return err
		}
		res.RowsAffected = r.DeletedCount
		return nil
	case "findOneAndUpdate", "findOneAndReplace", "findOneAndDelete":
		f, err := argDoc(op.Args, 0)
		if err != nil {
			return err
		}
		var r *mongo.SingleResult
		switch op.Name {
		case "findOneAndDelete":
			r = coll.FindOneAndDelete(ctx, f)
		case "findOneAndUpdate":
			if len(op.Args) < 2 {
				return errors.New("findOneAndUpdate needs a filter and an update")
			}
			r = coll.FindOneAndUpdate(ctx, f, op.Args[1], options.FindOneAndUpdate().SetUpsert(upsert(2)).SetReturnDocument(options.After))
		default:
			if len(op.Args) < 2 {
				return errors.New("findOneAndReplace needs a filter and a document")
			}
			r = coll.FindOneAndReplace(ctx, f, op.Args[1], options.FindOneAndReplace().SetUpsert(upsert(2)).SetReturnDocument(options.After))
		}
		var d bson.D
		if err := r.Decode(&d); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				res.RowsAffected = 0
				res.Note = "no document matched"
				return nil
			}
			return err
		}
		return docRows(res, []bson.D{d}, opts, budget)
	case "createIndex":
		keys, err := argDoc(op.Args, 0)
		if err != nil {
			return err
		}
		io := options.Index()
		o, _ := argDoc(op.Args, 1)
		if n, ok := lookup(o, "name").(string); ok {
			io.SetName(n)
		}
		if u, ok := lookup(o, "unique").(bool); ok {
			io.SetUnique(u)
		}
		name, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: keys, Options: io})
		if err != nil {
			return err
		}
		res.Note = "created index " + name
		return nil
	case "dropIndex":
		name, ok := firstString(op.Args)
		if !ok {
			return errors.New("dropIndex needs the index name")
		}
		if err := coll.Indexes().DropOne(ctx, name); err != nil {
			return err
		}
		res.Note = "dropped index " + name
		return nil
	case "drop":
		if err := coll.Drop(ctx); err != nil {
			return err
		}
		res.Note = "dropped collection " + coll.Name()
		return nil
	case "renameCollection":
		to, ok := firstString(op.Args)
		if !ok {
			return errors.New("renameCollection needs the new name")
		}
		db := coll.Database().Name()
		err := c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "renameCollection", Value: db + "." + coll.Name()}, {Key: "to", Value: db + "." + to}}).Err()
		if err != nil {
			return err
		}
		res.Note = "renamed to " + to
		return nil
	}
	return fmt.Errorf("%s() is not supported", op.Name)
}

func firstArray(args []any) (bson.A, bool) {
	if len(args) == 0 {
		return nil, false
	}
	a, ok := args[0].(bson.A)
	return a, ok
}

func idString(v any) string {
	if id, ok := v.(bson.ObjectID); ok {
		return id.Hex()
	}
	return fmt.Sprint(v)
}

func cursorRows(ctx context.Context, res *Result, cur *mongo.Cursor, opts QueryOptions, budget *int) error {
	defer cur.Close(ctx)
	docs := []bson.D{}
	n := 0
	for cur.Next(ctx) {
		if len(docs) >= opts.MaxRows || n >= *budget {
			res.Truncated = true
			break
		}
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			return err
		}
		n += len(cur.Current)
		docs = append(docs, d)
	}
	if err := cur.Err(); err != nil {
		return err
	}
	truncated := res.Truncated
	err := docRows(res, docs, opts, budget)
	res.Truncated = res.Truncated || truncated
	return err
}

// docRows turns documents into rows: the union of their top-level fields,
// _id first, in the order they first appear. Nested values are JSON.
func docRows(res *Result, docs []bson.D, opts QueryOptions, budget *int) error {
	idx := map[string]int{}
	var cols []string
	types := map[string]string{}
	for _, d := range docs {
		for _, e := range d {
			if _, ok := idx[e.Key]; !ok {
				idx[e.Key] = len(cols)
				cols = append(cols, e.Key)
			}
			if t := bsonType(e.Value); types[e.Key] == "" || types[e.Key] == "null" {
				types[e.Key] = t
			} else if types[e.Key] != t && t != "null" {
				types[e.Key] = "mixed"
			}
		}
	}
	if i, ok := idx["_id"]; ok && i > 0 {
		cols = append([]string{"_id"}, append(cols[:i:i], cols[i+1:]...)...)
		for j, c := range cols {
			idx[c] = j
		}
	}
	res.Columns = make([]ResultColumn, len(cols))
	for i, c := range cols {
		res.Columns[i] = ResultColumn{Name: c, Type: types[c]}
	}
	res.Rows = [][]any{}
	for _, d := range docs {
		if *budget <= 0 || len(res.Rows) >= opts.MaxRows {
			res.Truncated = true
			break
		}
		row := make([]any, len(cols))
		for _, e := range d {
			row[idx[e.Key]] = bsonDisplay(e.Value, opts.CellLimit)
		}
		*budget -= size(row)
		res.Rows = append(res.Rows, row)
	}
	return nil
}

// bsonDisplay turns a BSON value into what a result cell shows. A field a
// document does not have is nil too.
func bsonDisplay(v any, limit int) any {
	switch x := v.(type) {
	case nil, bool, int32, int64, string:
		return displayValue(x, limit)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fmt.Sprint(x)
		}
		return x
	case bson.ObjectID:
		return x.Hex()
	case bson.DateTime:
		return x.Time().UTC().Format(time.RFC3339Nano)
	case bson.Decimal128:
		return x.String()
	case bson.D, bson.A, bson.M:
		b, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: x}}, false, false)
		if err != nil {
			return fmt.Sprint(x)
		}
		// {"v":…} → …
		raw := b[5 : len(b)-1]
		if len(raw) > limit {
			return cut(string(raw), limit)
		}
		return json.RawMessage(raw)
	}
	b, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: v}}, false, false)
	if err != nil {
		return fmt.Sprint(v)
	}
	return cut(string(b[5:len(b)-1]), limit)
}

func byteSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}
