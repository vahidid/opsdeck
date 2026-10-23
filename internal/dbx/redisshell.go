package dbx

import (
	"fmt"
	"strconv"
	"strings"
)

// Redis scripts are redis-cli commands, one per line: GET user:1, HGETALL
// "session:a b". Quotes work as in redis-cli ("…" with \n, \t, \xHH and \"
// escapes; '…' literal); a line starting with # or // is a comment.

type redisStmt struct {
	Text string
	Args []string
}

func parseRedis(script string) ([]redisStmt, error) {
	var out []redisStmt
	for n, line := range strings.Split(script, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		args, err := splitRedisLine(t)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", n+1, err)
		}
		if len(args) > 0 {
			out = append(out, redisStmt{Text: t, Args: args})
		}
	}
	return out, nil
}

// splitRedisLine splits a line like redis-cli does (sdssplitargs).
func splitRedisLine(s string) ([]string, error) {
	var out []string
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
			i++
		}
		if i >= len(s) {
			return out, nil
		}
		var b strings.Builder
		switch s[i] {
		case '"':
			i++
			for {
				if i >= len(s) {
					return nil, fmt.Errorf("unterminated quotes")
				}
				c := s[i]
				if c == '"' {
					i++
					break
				}
				if c == '\\' && i+1 < len(s) {
					i++
					switch e := s[i]; e {
					case 'n':
						b.WriteByte('\n')
					case 'r':
						b.WriteByte('\r')
					case 't':
						b.WriteByte('\t')
					case 'b':
						b.WriteByte('\b')
					case 'a':
						b.WriteByte('\a')
					case 'x':
						if i+2 < len(s) {
							if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
								b.WriteByte(byte(v))
								i += 3
								continue
							}
						}
						b.WriteByte('x')
					default:
						b.WriteByte(e)
					}
					i++
					continue
				}
				b.WriteByte(c)
				i++
			}
		case '\'':
			i++
			for {
				if i >= len(s) {
					return nil, fmt.Errorf("unterminated quotes")
				}
				c := s[i]
				if c == '\'' {
					i++
					break
				}
				if c == '\\' && i+1 < len(s) && s[i+1] == '\'' {
					c = '\''
					i++
				}
				b.WriteByte(c)
				i++
			}
		default:
			for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '\r' && s[i] != '"' && s[i] != '\'' {
				b.WriteByte(s[i])
				i++
			}
			out = append(out, b.String())
			continue
		}
		if i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '\r' {
			return nil, fmt.Errorf("closing quote must be followed by a space")
		}
		out = append(out, b.String())
	}
}

// redisQuote quotes an argument for a script line when it needs quotes.
func redisQuote(s string) string {
	plain := s != ""
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= ' ' || c >= 0x7f || c == '"' || c == '\'' || c == '\\' {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\r':
			b.WriteString(`\r`)
		case c < ' ' || c == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

var (
	redisReads = words(`GET MGET GETRANGE SUBSTR STRLEN EXISTS TYPE TTL PTTL EXPIRETIME PEXPIRETIME KEYS SCAN RANDOMKEY DBSIZE DUMP
		HGET HMGET HGETALL HKEYS HVALS HLEN HEXISTS HSTRLEN HSCAN HRANDFIELD HTTL HPTTL
		LRANGE LLEN LINDEX LPOS
		SMEMBERS SISMEMBER SMISMEMBER SCARD SRANDMEMBER SSCAN SINTER SUNION SDIFF SINTERCARD
		ZRANGE ZREVRANGE ZRANGEBYSCORE ZREVRANGEBYSCORE ZRANGEBYLEX ZREVRANGEBYLEX ZSCORE ZMSCORE ZRANK ZREVRANK ZCARD ZCOUNT
		ZLEXCOUNT ZSCAN ZRANDMEMBER ZINTER ZUNION ZDIFF ZINTERCARD
		XRANGE XREVRANGE XLEN XINFO XPENDING XREAD
		GETBIT BITCOUNT BITPOS BITFIELD_RO PFCOUNT GEOPOS GEODIST GEOHASH GEORADIUS_RO GEORADIUSBYMEMBER_RO GEOSEARCH SORT_RO LCS
		INFO PING ECHO TIME LASTSAVE ROLE SELECT EVAL_RO EVALSHA_RO FCALL_RO
		JSON.GET JSON.MGET JSON.TYPE JSON.STRLEN JSON.OBJKEYS JSON.OBJLEN JSON.ARRLEN JSON.ARRINDEX JSON.RESP
		FT.SEARCH FT.AGGREGATE FT.INFO FT._LIST FT.EXPLAIN FT.TAGVALS FT.SPELLCHECK FT.PROFILE
		TS.GET TS.MGET TS.RANGE TS.REVRANGE TS.MRANGE TS.MREVRANGE TS.INFO TS.QUERYINDEX
		BF.EXISTS BF.MEXISTS BF.INFO CF.EXISTS CF.MEXISTS CF.COUNT CF.INFO`)
	// container commands: the read subcommands
	redisReadSubs = map[string]map[string]bool{
		"OBJECT":   words("ENCODING FREQ IDLETIME REFCOUNT HELP"),
		"MEMORY":   words("USAGE STATS DOCTOR MALLOC-STATS HELP"),
		"CONFIG":   words("GET HELP"),
		"CLIENT":   words("LIST INFO GETNAME ID HELP"),
		"SLOWLOG":  words("GET LEN HELP"),
		"LATENCY":  words("LATEST HISTORY DOCTOR GRAPH HISTOGRAM HELP"),
		"COMMAND":  words("COUNT INFO DOCS LIST GETKEYS HELP"),
		"CLUSTER":  words("INFO NODES SLOTS SHARDS MYID KEYSLOT COUNTKEYSINSLOT HELP"),
		"ACL":      words("WHOAMI CAT USERS HELP"),
		"PUBSUB":   words("CHANNELS NUMSUB NUMPAT SHARDCHANNELS SHARDNUMSUB HELP"),
		"SCRIPT":   words("EXISTS HELP"),
		"FUNCTION": words("LIST STATS DUMP HELP"),
		"MODULE":   words("LIST HELP"),
	}
	redisDestructive     = words("FLUSHALL FLUSHDB SHUTDOWN DEBUG SWAPDB REPLICAOF SLAVEOF FAILOVER")
	redisDestructiveSubs = map[string]map[string]bool{
		"SCRIPT": words("FLUSH"), "FUNCTION": words("FLUSH DELETE"), "CLUSTER": words("RESET FLUSHSLOTS FORGET FAILOVER"),
		"ACL": words("DELUSER"), "MODULE": words("UNLOAD"), "FT.DROPINDEX": nil,
	}
	// what a script cannot do: opsdeck runs each command on its own,
	// without staying subscribed or blocked
	redisRefused = map[string]string{
		"MULTI": "transactions", "EXEC": "transactions", "DISCARD": "transactions", "WATCH": "transactions", "UNWATCH": "transactions",
		"SUBSCRIBE": "subscriptions", "PSUBSCRIBE": "subscriptions", "SSUBSCRIBE": "subscriptions", "UNSUBSCRIBE": "subscriptions",
		"PUNSUBSCRIBE": "subscriptions", "SUNSUBSCRIBE": "subscriptions", "MONITOR": "monitoring", "SYNC": "replication", "PSYNC": "replication",
		"BLPOP": "blocking commands", "BRPOP": "blocking commands", "BLMOVE": "blocking commands", "BRPOPLPUSH": "blocking commands",
		"BLMPOP": "blocking commands", "BZPOPMIN": "blocking commands", "BZPOPMAX": "blocking commands", "BZMPOP": "blocking commands",
		"WAIT": "blocking commands", "WAITAOF": "blocking commands", "AUTH": "logging in", "HELLO": "logging in", "RESET": "logging in",
		"QUIT": "logging in", "CLIENT REPLY": "replies", "CLIENT PAUSE": "blocking commands",
	}
)

// redisRefusal tells why a command is not run, or "".
func redisRefusal(args []string) string {
	name := strings.ToUpper(args[0])
	why := redisRefused[name]
	if why == "" && len(args) > 1 {
		why = redisRefused[name+" "+strings.ToUpper(args[1])]
	}
	if why == "" && (name == "XREAD" || name == "XREADGROUP") {
		for _, a := range args[1:] {
			if strings.EqualFold(a, "BLOCK") {
				why = "blocking commands"
			}
		}
	}
	if why == "" {
		return ""
	}
	return fmt.Sprintf("%s: opsdeck runs each command on its own and does not support %s", name, why)
}

func classifyRedis(script string) Classification {
	stmts, err := parseRedis(script)
	if err != nil {
		return Classification{Risk: RiskChange, Reasons: []string{"cannot read the script: " + err.Error()},
			Statements: []Statement{{Text: script, Risk: RiskChange, Reasons: []string{err.Error()}}}}
	}
	c := Classification{Risk: RiskRead}
	for _, st := range stmts {
		r, why := redisRisk(st.Args)
		c.Risk = maxRisk(c.Risk, r)
		c.Reasons = append(c.Reasons, why...)
		c.Statements = append(c.Statements, Statement{Text: st.Text, Risk: r, Reasons: why, ReturnsRows: true})
	}
	c.Reasons = dedupe(c.Reasons)
	return c
}

func redisRisk(args []string) (string, []string) {
	name := strings.ToUpper(args[0])
	sub := ""
	if len(args) > 1 {
		sub = strings.ToUpper(args[1])
	}
	if why := redisRefusal(args); why != "" {
		return RiskChange, []string{why}
	}
	if redisDestructive[name] {
		return RiskDestructive, []string{name + " removes data or stops the server"}
	}
	if subs, ok := redisDestructiveSubs[name]; ok && (subs == nil || subs[sub]) {
		return RiskDestructive, []string{strings.TrimSpace(name+" "+sub) + " removes data"}
	}
	if subs, ok := redisReadSubs[name]; ok {
		if subs[sub] {
			return RiskRead, nil
		}
		return RiskChange, []string{strings.TrimSpace(name + " " + sub)}
	}
	if redisReads[name] {
		return RiskRead, nil
	}
	switch name {
	case "EVAL", "EVALSHA", "FCALL":
		return RiskChange, []string{name + " runs a script, which can run any command"}
	case "DEL", "UNLINK":
		return RiskChange, []string{name + " removes keys"}
	}
	return RiskChange, []string{name + " is not a read"}
}
