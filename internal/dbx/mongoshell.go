package dbx

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// MongoDB scripts are written like mongosh: show dbs, show collections, use
// <db>, db.<collection>.<method>(…)[.<modifier>(…)]…, db.getCollection("x")…
// and db.<method>(…). Arguments are JavaScript-like literals: unquoted keys,
// single or double quotes, trailing commas, ObjectId(), ISODate(), new
// Date(), NumberLong(), NumberInt(), NumberDecimal(), UUID() and /regex/.
// Nothing is evaluated: only the operations listed below are run.

type mongoCall struct {
	Name string
	Args []any
}

type mongoStmt struct {
	Text  string
	Kind  string // show | use | coll | db
	Arg   string // show: dbs | collections; use: database
	Coll  string
	Calls []mongoCall // the operation, then cursor modifiers
}

type mongoParser struct {
	src []rune
	pos int
}

func (p *mongoParser) errf(format string, a ...any) error {
	line, col := 1, 1
	for _, r := range p.src[:min(p.pos, len(p.src))] {
		if r == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return fmt.Errorf("line %d:%d: %s", line, col, fmt.Sprintf(format, a...))
}

// skip skips spaces (newlines too unless stopAtNewline) and comments.
func (p *mongoParser) skip(stopAtNewline bool) {
	for p.pos < len(p.src) {
		r := p.src[p.pos]
		switch {
		case r == '\n' && stopAtNewline:
			return
		case unicode.IsSpace(r):
			p.pos++
		case r == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case r == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			end := strings.Index(string(p.src[p.pos+2:]), "*/")
			if end < 0 {
				p.pos = len(p.src)
			} else {
				p.pos += 2 + len([]rune(string(p.src[p.pos+2:])[:end])) + 2
			}
		default:
			return
		}
	}
}

func (p *mongoParser) peek() rune {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *mongoParser) expect(r rune) error {
	p.skip(false)
	if p.peek() != r {
		return p.errf("expected %q", r)
	}
	p.pos++
	return nil
}

func isIdentStart(r rune) bool { return unicode.IsLetter(r) || r == '_' || r == '$' }
func isIdentPart(r rune) bool  { return isIdentStart(r) || unicode.IsDigit(r) }

func (p *mongoParser) ident() string {
	start := p.pos
	if p.pos < len(p.src) && isIdentStart(p.src[p.pos]) {
		p.pos++
		for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
			p.pos++
		}
	}
	return string(p.src[start:p.pos])
}

// parseMongo parses a script into statements.
func parseMongo(script string) ([]mongoStmt, error) {
	p := &mongoParser{src: []rune(script)}
	var out []mongoStmt
	for {
		p.skip(false)
		for p.peek() == ';' {
			p.pos++
			p.skip(false)
		}
		if p.pos >= len(p.src) {
			return out, nil
		}
		start := p.pos
		st, err := p.statement()
		if err != nil {
			return nil, err
		}
		st.Text = strings.TrimSpace(string(p.src[start:p.pos]))
		out = append(out, st)
		p.skip(true)
		if r := p.peek(); r != 0 && r != '\n' && r != ';' {
			return nil, p.errf("unexpected %q after the statement", r)
		}
	}
}

func (p *mongoParser) statement() (mongoStmt, error) {
	word := p.ident()
	switch word {
	case "show":
		p.skip(true)
		what := p.ident()
		switch what {
		case "dbs", "databases":
			return mongoStmt{Kind: "show", Arg: "dbs"}, nil
		case "collections", "tables":
			return mongoStmt{Kind: "show", Arg: "collections"}, nil
		}
		return mongoStmt{}, p.errf("show %s is not supported (show dbs, show collections)", what)
	case "use":
		p.skip(true)
		name := p.ident()
		for p.pos < len(p.src) && (p.src[p.pos] == '-' || p.src[p.pos] == '.' || isIdentPart(p.src[p.pos])) {
			name += string(p.src[p.pos])
			p.pos++
		}
		if name == "" {
			return mongoStmt{}, p.errf("use needs a database name")
		}
		return mongoStmt{Kind: "use", Arg: name}, nil
	case "db":
	case "":
		return mongoStmt{}, p.errf("expected a statement (db.…, show, use)")
	default:
		return mongoStmt{}, p.errf("unknown statement %q (db.…, show, use)", word)
	}
	st := mongoStmt{Kind: "db"}
	// db.coll / db["coll"] / db.getCollection("coll") / db.method(
	p.skip(false)
	switch p.peek() {
	case '[':
		p.pos++
		v, err := p.value()
		if err != nil {
			return st, err
		}
		name, ok := v.(string)
		if !ok {
			return st, p.errf("db[…] needs a collection name")
		}
		if err := p.expect(']'); err != nil {
			return st, err
		}
		st.Kind, st.Coll = "coll", name
	case '.':
		p.pos++
		p.skip(false)
		name := p.ident()
		if name == "" {
			return st, p.errf("expected a collection or method after db.")
		}
		p.skip(false)
		if p.peek() == '(' {
			args, err := p.args()
			if err != nil {
				return st, err
			}
			if name == "getCollection" {
				coll, ok := firstString(args)
				if !ok {
					return st, p.errf("getCollection needs a name")
				}
				st.Kind, st.Coll = "coll", coll
			} else {
				st.Calls = append(st.Calls, mongoCall{name, args})
				return st, p.modifiers(&st)
			}
		} else {
			st.Kind, st.Coll = "coll", name
			// db.system.users: collection names may contain dots
			for {
				save := p.pos
				p.skip(false)
				if p.peek() != '.' {
					p.pos = save
					break
				}
				p.pos++
				p.skip(false)
				part := p.ident()
				p.skip(false)
				if p.peek() == '(' {
					p.pos = save
					break
				}
				st.Coll += "." + part
			}
		}
	default:
		return st, p.errf("expected . after db")
	}
	// the collection's operation
	if err := p.expect('.'); err != nil {
		return st, err
	}
	p.skip(false)
	name := p.ident()
	args, err := p.args()
	if err != nil {
		return st, err
	}
	st.Calls = append(st.Calls, mongoCall{name, args})
	return st, p.modifiers(&st)
}

// modifiers parses .sort(…).limit(…)… (also on the next lines).
func (p *mongoParser) modifiers(st *mongoStmt) error {
	for {
		save := p.pos
		p.skip(false)
		if p.peek() != '.' {
			p.pos = save
			return nil
		}
		p.pos++
		p.skip(false)
		name := p.ident()
		if name == "" {
			return p.errf("expected a method name")
		}
		args, err := p.args()
		if err != nil {
			return err
		}
		st.Calls = append(st.Calls, mongoCall{name, args})
	}
}

func firstString(args []any) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	s, ok := args[0].(string)
	return s, ok
}

func (p *mongoParser) args() ([]any, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	var out []any
	for {
		p.skip(false)
		if p.peek() == ')' {
			p.pos++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skip(false)
		switch p.peek() {
		case ',':
			p.pos++
		case ')':
		default:
			return nil, p.errf("expected , or )")
		}
	}
}

// value parses a literal into BSON values (documents keep their key order).
func (p *mongoParser) value() (any, error) {
	p.skip(false)
	r := p.peek()
	switch {
	case r == '{':
		p.pos++
		d := bson.D{}
		for {
			p.skip(false)
			if p.peek() == '}' {
				p.pos++
				return d, nil
			}
			var key string
			if q := p.peek(); q == '"' || q == '\'' {
				s, err := p.str()
				if err != nil {
					return nil, err
				}
				key = s
			} else {
				key = p.ident()
				if key == "" {
					// numeric keys
					start := p.pos
					for p.pos < len(p.src) && unicode.IsDigit(p.src[p.pos]) {
						p.pos++
					}
					key = string(p.src[start:p.pos])
				}
				if key == "" {
					return nil, p.errf("expected a key")
				}
			}
			if err := p.expect(':'); err != nil {
				return nil, err
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			d = append(d, bson.E{Key: key, Value: v})
			p.skip(false)
			switch p.peek() {
			case ',':
				p.pos++
			case '}':
			default:
				return nil, p.errf("expected , or }")
			}
		}
	case r == '[':
		p.pos++
		a := bson.A{}
		for {
			p.skip(false)
			if p.peek() == ']' {
				p.pos++
				return a, nil
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			a = append(a, v)
			p.skip(false)
			switch p.peek() {
			case ',':
				p.pos++
			case ']':
			default:
				return nil, p.errf("expected , or ]")
			}
		}
	case r == '"' || r == '\'':
		return p.str()
	case r == '-' || r == '+' || r == '.' || unicode.IsDigit(r):
		return p.number()
	case r == '/':
		return p.regex()
	case isIdentStart(r):
		word := p.ident()
		switch word {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null", "undefined":
			return nil, nil
		case "Infinity":
			return math.Inf(1), nil
		case "NaN":
			return math.NaN(), nil
		case "new":
			p.skip(false)
			return p.value()
		}
		return p.constructor(word)
	}
	if r == 0 {
		return nil, p.errf("unexpected end")
	}
	return nil, p.errf("unexpected %q", r)
}

func (p *mongoParser) str() (string, error) {
	q := p.src[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) {
		r := p.src[p.pos]
		p.pos++
		switch {
		case r == q:
			return b.String(), nil
		case r == '\\' && p.pos < len(p.src):
			e := p.src[p.pos]
			p.pos++
			switch e {
			case 'n':
				b.WriteRune('\n')
			case 't':
				b.WriteRune('\t')
			case 'r':
				b.WriteRune('\r')
			case 'b':
				b.WriteRune('\b')
			case 'f':
				b.WriteRune('\f')
			case '0':
				b.WriteRune(0)
			case 'u':
				if p.pos+4 <= len(p.src) {
					if n, err := strconv.ParseUint(string(p.src[p.pos:p.pos+4]), 16, 32); err == nil {
						b.WriteRune(rune(n))
						p.pos += 4
						continue
					}
				}
				b.WriteRune('u')
			default:
				b.WriteRune(e)
			}
		default:
			b.WriteRune(r)
		}
	}
	return "", p.errf("unterminated string")
}

func (p *mongoParser) number() (any, error) {
	start := p.pos
	if r := p.peek(); r == '-' || r == '+' {
		p.pos++
	}
	for p.pos < len(p.src) && (unicode.IsDigit(p.src[p.pos]) || strings.ContainsRune(".eE", p.src[p.pos]) ||
		((p.src[p.pos] == '-' || p.src[p.pos] == '+') && strings.ContainsRune("eE", p.src[p.pos-1]))) {
		p.pos++
	}
	s := string(p.src[start:p.pos])
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n >= math.MinInt32 && n <= math.MaxInt32 {
			return int32(n), nil
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, p.errf("bad number %q", s)
	}
	return f, nil
}

func (p *mongoParser) regex() (any, error) {
	p.pos++
	var b strings.Builder
	inClass := false
	for p.pos < len(p.src) {
		r := p.src[p.pos]
		p.pos++
		switch {
		case r == '\\' && p.pos < len(p.src):
			b.WriteRune(r)
			b.WriteRune(p.src[p.pos])
			p.pos++
			continue
		case r == '[':
			inClass = true
		case r == ']':
			inClass = false
		case r == '/' && !inClass:
			flags := p.ident()
			return bson.Regex{Pattern: b.String(), Options: flags}, nil
		case r == '\n':
			return nil, p.errf("unterminated regular expression")
		}
		b.WriteRune(r)
	}
	return nil, p.errf("unterminated regular expression")
}

// constructor parses ObjectId("…"), ISODate("…"), Date("…"), NumberLong(…)…
func (p *mongoParser) constructor(name string) (any, error) {
	p.skip(false)
	if p.peek() != '(' {
		return nil, p.errf("unknown name %q (use quotes for strings)", name)
	}
	args, err := p.args()
	if err != nil {
		return nil, err
	}
	arg := func() (string, bool) {
		if len(args) == 0 {
			return "", false
		}
		switch v := args[0].(type) {
		case string:
			return v, true
		case int32, int64, float64:
			return fmt.Sprint(v), true
		}
		return "", false
	}
	switch name {
	case "ObjectId", "ObjectID":
		s, ok := arg()
		if !ok {
			return bson.NewObjectID(), nil
		}
		id, err := bson.ObjectIDFromHex(s)
		if err != nil {
			return nil, p.errf("ObjectId: %v", err)
		}
		return id, nil
	case "ISODate", "Date":
		s, ok := arg()
		if !ok {
			return bson.NewDateTimeFromTime(time.Now()), nil
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return bson.DateTime(n), nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return bson.NewDateTimeFromTime(t), nil
			}
		}
		return nil, p.errf("%s: cannot read %q as a date", name, s)
	case "NumberLong", "Long":
		s, _ := arg()
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, p.errf("NumberLong: %v", err)
		}
		return n, nil
	case "NumberInt", "Int32":
		s, _ := arg()
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return nil, p.errf("NumberInt: %v", err)
		}
		return int32(n), nil
	case "NumberDecimal", "Decimal128":
		s, _ := arg()
		d, err := bson.ParseDecimal128(s)
		if err != nil {
			return nil, p.errf("NumberDecimal: %v", err)
		}
		return d, nil
	case "UUID":
		s, _ := arg()
		b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
		if err != nil || len(b) != 16 {
			return nil, p.errf("UUID: not a UUID")
		}
		return bson.Binary{Subtype: 4, Data: b}, nil
	}
	return nil, p.errf("%s() is not supported", name)
}

// ---------------------------------------------------------------------------
// Classification

var (
	mongoCollReads = map[string]bool{"find": true, "findOne": true, "aggregate": true, "countDocuments": true, "count": true,
		"estimatedDocumentCount": true, "distinct": true, "getIndexes": true, "stats": true}
	mongoCollChanges = map[string]bool{"insertOne": true, "insertMany": true, "updateOne": true, "updateMany": true, "replaceOne": true,
		"deleteOne": true, "deleteMany": true, "findOneAndUpdate": true, "findOneAndReplace": true, "findOneAndDelete": true,
		"createIndex": true, "dropIndex": true, "drop": true, "renameCollection": true}
	mongoModifiers = map[string]bool{"sort": true, "limit": true, "skip": true, "projection": true, "hint": true, "toArray": true,
		"pretty": true, "batchSize": true, "maxTimeMS": true, "count": true, "itcount": true}
	mongoDBReads = map[string]bool{"getCollectionNames": true, "getCollectionInfos": true, "stats": true, "version": true, "getName": true,
		"serverStatus": true, "hostInfo": true}
	// runCommand / adminCommand by command name (lower case)
	mongoCmdReads = map[string]bool{"ping": true, "buildinfo": true, "serverstatus": true, "dbstats": true, "collstats": true,
		"listcollections": true, "listdatabases": true, "listindexes": true, "hello": true, "ismaster": true, "connectionstatus": true,
		"hostinfo": true, "getparameter": true, "find": true, "aggregate": true, "count": true, "distinct": true, "explain": true,
		"currentop": true, "top": true, "replsetgetstatus": true, "getlog": true, "usersinfo": true, "rolesinfo": true,
		"getcmdlineopts": true, "features": true, "whatsmyuri": true, "lockinfo": true, "datasize": true, "dbhash": true}
	mongoCmdDestructive = map[string]bool{"dropdatabase": true, "drop": true, "shutdown": true, "dropallusersfromdatabase": true,
		"dropuser": true, "dropallrolesfromdatabase": true, "droprole": true, "delete": true}
)

func classifyMongo(script string) Classification {
	stmts, err := parseMongo(script)
	if err != nil {
		return Classification{Risk: RiskChange, Reasons: []string{"cannot read the script: " + err.Error()},
			Statements: []Statement{{Text: script, Risk: RiskChange, Reasons: []string{err.Error()}}}}
	}
	c := Classification{Risk: RiskRead}
	for _, st := range stmts {
		r, why := mongoRisk(st)
		s := Statement{Text: st.Text, Risk: r, Reasons: why, ReturnsRows: true}
		c.Risk = maxRisk(c.Risk, r)
		c.Reasons = append(c.Reasons, why...)
		c.Statements = append(c.Statements, s)
	}
	c.Reasons = dedupe(c.Reasons)
	return c
}

func mongoRisk(st mongoStmt) (string, []string) {
	one := func(r, why string) (string, []string) { return r, []string{why} }
	switch st.Kind {
	case "show", "use":
		return RiskRead, nil
	case "db":
		op := st.Calls[0]
		switch {
		case mongoDBReads[op.Name]:
			return RiskRead, nil
		case op.Name == "runCommand" || op.Name == "adminCommand":
			doc, ok := firstDoc(op.Args)
			if !ok || len(doc) == 0 {
				return one(RiskChange, op.Name+" needs a command document")
			}
			name := strings.ToLower(doc[0].Key)
			switch {
			case mongoCmdDestructive[name]:
				return one(RiskDestructive, "command "+doc[0].Key+" removes data or stops the server")
			case mongoCmdReads[name]:
				if hasWriteStage(lookup(doc, "pipeline")) {
					return one(RiskChange, "aggregate with $out or $merge writes a collection")
				}
				return RiskRead, nil
			}
			return one(RiskChange, "command "+doc[0].Key)
		case op.Name == "dropDatabase":
			return one(RiskDestructive, "dropDatabase removes the database")
		case op.Name == "createCollection":
			return one(RiskChange, "createCollection")
		}
		return one(RiskChange, "db."+op.Name+"() is not a known read")
	}
	op := st.Calls[0]
	for _, m := range st.Calls[1:] {
		if !mongoModifiers[m.Name] {
			return one(RiskChange, "."+m.Name+"() is not supported")
		}
	}
	switch {
	case op.Name == "aggregate":
		if len(op.Args) > 0 && hasWriteStage(op.Args[0]) {
			return one(RiskChange, "aggregate with $out or $merge writes a collection")
		}
		return RiskRead, nil
	case mongoCollReads[op.Name]:
		return RiskRead, nil
	case op.Name == "drop":
		return one(RiskDestructive, "drop() removes the collection")
	case op.Name == "deleteMany":
		if f, _ := firstDoc(op.Args); len(f) == 0 {
			return one(RiskDestructive, "deleteMany({}) removes every document")
		}
		return one(RiskChange, "deleteMany removes documents")
	case op.Name == "updateMany":
		if f, _ := firstDoc(op.Args); len(f) == 0 {
			return one(RiskDestructive, "updateMany({}) changes every document")
		}
		return one(RiskChange, "updateMany changes documents")
	case mongoCollChanges[op.Name]:
		return one(RiskChange, op.Name+" modifies the collection")
	}
	return one(RiskChange, op.Name+"() is not a known operation")
}

func firstDoc(args []any) (bson.D, bool) {
	if len(args) == 0 {
		return nil, false
	}
	d, ok := args[0].(bson.D)
	return d, ok
}

func lookup(d bson.D, key string) any {
	for _, e := range d {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

// hasWriteStage reports whether an aggregation pipeline writes ($out, $merge).
func hasWriteStage(pipeline any) bool {
	a, ok := pipeline.(bson.A)
	if !ok {
		return false
	}
	for _, s := range a {
		if d, ok := s.(bson.D); ok {
			for _, e := range d {
				if e.Key == "$out" || e.Key == "$merge" {
					return true
				}
			}
		}
	}
	return false
}
