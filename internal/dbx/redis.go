package dbx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisConn is a client of one numbered Redis database.
type redisConn struct {
	client  *redis.Client
	db      int
	address string
	user    string
}

func openRedis(ctx context.Context, t target) (Session, error) {
	db := 0
	if t.database != "" {
		n, err := strconv.Atoi(t.database)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("a Redis database is a number (0, 1, …), not %q", t.database)
		}
		db = n
	}
	o := &redis.Options{
		Addr: t.addr, Username: t.user, Password: t.password, DB: db, Protocol: 2,
		Dialer: t.dial, DialTimeout: 20 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
		ContextTimeoutEnabled: true, PoolSize: 4, MinIdleConns: 0, ClientName: "opsdeck", DisableIdentity: true,
	}
	if t.user == "default" {
		o.Username = ""
	}
	switch t.tls {
	case "require":
		o.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	case "verify-full":
		host := t.addr
		if i := strings.LastIndex(host, ":"); i > 0 {
			host = host[:i]
		}
		o.TLSConfig = &tls.Config{ServerName: strings.Trim(host, "[]")}
	}
	c := redis.NewClient(o)
	if err := c.Ping(ctx).Err(); err != nil {
		c.Close()
		return nil, err
	}
	return &redisConn{client: c, db: db, address: t.addr, user: t.user}, nil
}

func (c *redisConn) Engine() string   { return "redis" }
func (c *redisConn) Database() string { return strconv.Itoa(c.db) }
func (c *redisConn) close()           { c.client.Close() }

func (c *redisConn) Info(ctx context.Context) (Info, error) {
	in := Info{Engine: "redis", Address: c.address, Database: c.Database(), User: c.user}
	s, err := c.client.Info(ctx, "server").Result()
	if err != nil {
		return in, err
	}
	for _, l := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "redis_version:"); ok {
			in.Version = "Redis " + v
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "valkey_version:"); ok {
			in.Version = "Valkey " + v
		}
	}
	if u, err := c.client.Do(ctx, "ACL", "WHOAMI").Text(); err == nil {
		in.User = u
	} else if in.User == "" {
		in.User = "default"
	}
	return in, nil
}

// Databases lists the numbered databases: the configured count, or the ones
// with keys when CONFIG is not allowed.
func (c *redisConn) Databases(ctx context.Context) ([]string, error) {
	n := 0
	if v, err := c.client.ConfigGet(ctx, "databases").Result(); err == nil {
		n, _ = strconv.Atoi(v["databases"])
	}
	set := map[int]bool{c.db: true}
	for i := 0; i < n; i++ {
		set[i] = true
	}
	if s, err := c.client.Info(ctx, "keyspace").Result(); err == nil {
		for _, l := range strings.Split(s, "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "db"); ok {
				if i, err := strconv.Atoi(strings.SplitN(rest, ":", 2)[0]); err == nil {
					set[i] = true
				}
			}
		}
	}
	nums := make([]int, 0, len(set))
	for i := range set {
		nums = append(nums, i)
	}
	sort.Ints(nums)
	out := make([]string, len(nums))
	for i, n := range nums {
		out[i] = strconv.Itoa(n)
	}
	return out, nil
}

const maxKeys = 1000

// Tables lists keys matching match (a SCAN pattern, default *), at most
// maxKeys, grouped by the part before their first colon.
func (c *redisConn) Tables(ctx context.Context, match string) ([]Table, error) {
	if match == "" {
		match = "*"
	}
	var keys []string
	var cursor uint64
	deadline := time.Now().Add(10 * time.Second)
	for {
		ks, next, err := c.client.Scan(ctx, cursor, match, 1000).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, ks...)
		cursor = next
		if cursor == 0 || len(keys) >= maxKeys || time.Now().After(deadline) {
			break
		}
	}
	if len(keys) > maxKeys {
		keys = keys[:maxKeys]
	}
	sort.Strings(keys)
	pipe := c.client.Pipeline()
	types := make([]*redis.StatusCmd, len(keys))
	for i, k := range keys {
		types[i] = pipe.Type(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	pipe = c.client.Pipeline()
	lens := make([]*redis.IntCmd, len(keys))
	for i, k := range keys {
		switch types[i].Val() {
		case "string":
			lens[i] = pipe.StrLen(ctx, k)
		case "hash":
			lens[i] = pipe.HLen(ctx, k)
		case "list":
			lens[i] = pipe.LLen(ctx, k)
		case "set":
			lens[i] = pipe.SCard(ctx, k)
		case "zset":
			lens[i] = pipe.ZCard(ctx, k)
		case "stream":
			lens[i] = pipe.XLen(ctx, k)
		}
	}
	pipe.Exec(ctx)
	out := []Table{}
	for i, k := range keys {
		typ := types[i].Val()
		if typ == "none" || typ == "" {
			continue // expired meanwhile
		}
		t := Table{Name: k, Type: typ, Rows: -1, Query: redisKeyQuery(typ, k)}
		if p, _, ok := strings.Cut(k, ":"); ok {
			t.Schema = p
		}
		if lens[i] != nil {
			if typ == "string" {
				t.Bytes = lens[i].Val()
			} else {
				t.Rows = lens[i].Val()
			}
		}
		out = append(out, t)
	}
	return out, nil
}

func redisKeyQuery(typ, key string) string {
	k := redisQuote(key)
	switch typ {
	case "string":
		return "GET " + k
	case "hash":
		return "HGETALL " + k
	case "list":
		return "LRANGE " + k + " 0 99"
	case "set":
		return "SSCAN " + k + " 0 COUNT 100"
	case "zset":
		return "ZRANGE " + k + " 0 99 WITHSCORES"
	case "stream":
		return "XREVRANGE " + k + " + - COUNT 100"
	case "ReJSON-RL":
		return "JSON.GET " + k
	}
	return "TYPE " + k
}

// Table describes a key: its type, expiry, encoding, memory and length.
func (c *redisConn) Table(ctx context.Context, _, name string) (*TableInfo, error) {
	typ, err := c.client.Type(ctx, name).Result()
	if err != nil {
		return nil, err
	}
	if typ == "none" {
		return nil, fmt.Errorf("key %q not found", name)
	}
	ti := &TableInfo{Name: name, Kind: "key", Columns: []Column{}, Indexes: []Index{}}
	ti.Properties = append(ti.Properties, Property{"type", typ})
	if d, err := c.client.PTTL(ctx, name).Result(); err == nil {
		v := "none (persistent)"
		if d > 0 {
			v = d.Round(time.Second).String() + " (expires " + time.Now().Add(d).UTC().Format(time.RFC3339) + ")"
		}
		ti.Properties = append(ti.Properties, Property{"expires in", v})
	}
	if e, err := c.client.ObjectEncoding(ctx, name).Result(); err == nil {
		ti.Properties = append(ti.Properties, Property{"encoding", e})
	}
	if m, err := c.client.MemoryUsage(ctx, name).Result(); err == nil {
		ti.Properties = append(ti.Properties, Property{"memory", byteSize(m)})
	}
	var n int64 = -1
	label := "elements"
	switch typ {
	case "string":
		n, label = c.client.StrLen(ctx, name).Val(), "length"
	case "hash":
		n, label = c.client.HLen(ctx, name).Val(), "fields"
	case "list":
		n = c.client.LLen(ctx, name).Val()
	case "set":
		n, label = c.client.SCard(ctx, name).Val(), "members"
	case "zset":
		n, label = c.client.ZCard(ctx, name).Val(), "members"
	case "stream":
		n, label = c.client.XLen(ctx, name).Val(), "entries"
	}
	if n >= 0 {
		ti.Properties = append(ti.Properties, Property{label, strconv.FormatInt(n, 10)})
	}
	ti.Properties = append(ti.Properties, Property{"show it", redisKeyQuery(typ, name)})
	return ti, nil
}

// Run runs commands one by one on one connection. Redis has no
// transaction over a script here: a change script stops at its first error
// and the commands before it stay.
func (c *redisConn) Run(ctx context.Context, script string, opts QueryOptions) (Classification, []Result, error) {
	opts.defaults()
	cls := classifyRedis(script)
	stmts, err := parseRedis(script)
	if err != nil {
		return cls, nil, err
	}
	if len(stmts) == 0 {
		return cls, nil, errors.New("no command")
	}
	for _, st := range stmts {
		if why := redisRefusal(st.Args); why != "" {
			return cls, nil, errors.New(why)
		}
	}
	if !opts.Write && cls.Risk != RiskRead {
		return cls, nil, &NotReadError{cls}
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	conn := c.client.Conn()
	selected := false
	defer func() {
		// the connection goes back to the pool: back to this database
		if selected {
			conn.Select(context.Background(), c.db)
		}
		conn.Close()
	}()
	budget := maxResultBytes
	results := []Result{}
	for i, st := range stmts {
		res := Result{Statement: st.Text, RowsAffected: -1}
		start := time.Now()
		args := make([]any, len(st.Args))
		for j, a := range st.Args {
			args[j] = a
		}
		cmd := redis.NewCmd(ctx, args...)
		conn.Process(ctx, cmd)
		v, err := cmd.Result()
		if errors.Is(err, redis.Nil) {
			v, err = nil, nil
		}
		res.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			if opts.Write && i > 0 {
				return cls, results, fmt.Errorf("%w (the commands before it ran; Redis scripts are not one transaction)", err)
			}
			return cls, results, err
		}
		if strings.EqualFold(st.Args[0], "SELECT") {
			selected = true
		}
		redisRows(&res, st.Args, v, opts, &budget)
		results = append(results, res)
	}
	return cls, results, nil
}

// pairReplies are commands whose array reply is field, value, field, value…
func pairReply(args []string) bool {
	name := strings.ToUpper(args[0])
	switch name {
	case "HGETALL", "CONFIG", "XINFO", "FT.INFO", "TS.INFO", "BF.INFO", "CF.INFO":
		return true
	case "HRANDFIELD", "ZRANGE", "ZREVRANGE", "ZRANGEBYSCORE", "ZREVRANGEBYSCORE", "ZRANDMEMBER", "ZINTER", "ZUNION", "ZDIFF":
		for _, a := range args[1:] {
			if strings.EqualFold(a, "WITHSCORES") || strings.EqualFold(a, "WITHVALUES") {
				return true
			}
		}
	}
	return false
}

// redisRows shapes a reply into a result table.
func redisRows(res *Result, args []string, v any, opts QueryOptions, budget *int) {
	name := strings.ToUpper(args[0])
	add := func(row ...any) bool {
		if len(res.Rows) >= opts.MaxRows || *budget <= 0 {
			res.Truncated = true
			return false
		}
		for i, x := range row {
			row[i] = redisDisplay(x, opts.CellLimit)
		}
		*budget -= size(row)
		res.Rows = append(res.Rows, row)
		return true
	}
	res.Rows = [][]any{}
	arr, isArr := v.([]any)
	switch {
	case name == "INFO":
		s, _ := v.(string)
		res.Columns = []ResultColumn{{"section", ""}, {"name", ""}, {"value", ""}}
		section := ""
		for _, l := range strings.Split(s, "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "# ") {
				section = l[2:]
			} else if k, val, ok := strings.Cut(l, ":"); ok && !add(section, k, val) {
				break
			}
		}
	case !isArr:
		res.Columns = []ResultColumn{{"value", redisType(v)}}
		add(v)
	case (strings.HasSuffix(name, "SCAN") && len(arr) == 2):
		cursor := fmt.Sprint(arr[0])
		items, _ := arr[1].([]any)
		if cursor != "0" {
			res.Note = "more: next cursor " + cursor + " (run the command again with it)"
		}
		if name == "HSCAN" || name == "ZSCAN" {
			res.Columns = []ResultColumn{{map[bool]string{true: "field", false: "member"}[name == "HSCAN"], ""}, {map[bool]string{true: "value", false: "score"}[name == "HSCAN"], ""}}
			for i := 0; i+1 < len(items) && add(items[i], items[i+1]); i += 2 {
			}
		} else {
			res.Columns = []ResultColumn{{map[bool]string{true: "key", false: "member"}[name == "SCAN"], ""}}
			for _, it := range items {
				if !add(it) {
					break
				}
			}
		}
	case name == "XRANGE" || name == "XREVRANGE":
		res.Columns = []ResultColumn{{"id", ""}, {"fields", "json"}}
		for _, e := range arr {
			p, _ := e.([]any)
			if len(p) == 2 && !add(p[0], pairsJSON(p[1])) {
				break
			}
		}
	case pairReply(args):
		res.Columns = []ResultColumn{{"field", ""}, {"value", ""}}
		if strings.HasPrefix(name, "Z") {
			res.Columns = []ResultColumn{{"member", ""}, {"score", ""}}
		}
		for i := 0; i+1 < len(arr) && add(arr[i], arr[i+1]); i += 2 {
		}
	default:
		res.Columns = []ResultColumn{{"#", ""}, {"value", ""}}
		for i, x := range arr {
			if !add(int64(i+1), x) {
				break
			}
		}
	}
}

func redisType(v any) string {
	switch v.(type) {
	case nil:
		return "nil"
	case int64:
		return "integer"
	case string:
		return "string"
	}
	return ""
}

// pairsJSON turns [f1 v1 f2 v2] into {"f1": "v1", …} (keeping the order).
func pairsJSON(v any) any {
	a, _ := v.([]any)
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(a); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(fmt.Sprint(a[i]))
		val, _ := json.Marshal(redisPlain(a[i+1]))
		b.Write(k)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return json.RawMessage(b.String())
}

func redisPlain(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redisPlain(e)
		}
		return out
	case error:
		return "(error) " + x.Error()
	}
	return v
}

func redisDisplay(v any, limit int) any {
	switch x := v.(type) {
	case []any:
		b, _ := json.Marshal(redisPlain(x))
		if len(b) > limit {
			return cut(string(b), limit)
		}
		return json.RawMessage(b)
	case error:
		return "(error) " + x.Error()
	case string:
		return displayValue([]byte(x), limit) // binary values as hex
	}
	return displayValue(v, limit)
}
