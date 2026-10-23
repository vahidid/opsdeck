package agent

import (
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Risk classes, ordered.
const (
	RiskRead        = "read"
	RiskChange      = "change"
	RiskDestructive = "destructive"
)

func riskRank(r string) int {
	switch r {
	case RiskRead:
		return 0
	case RiskChange:
		return 1
	default:
		return 2
	}
}

func maxRisk(a, b string) string {
	if riskRank(b) > riskRank(a) {
		return b
	}
	return a
}

// ClassifyCommand estimates the risk of a shell command by parsing it and
// classifying every simple command in it (pipelines, &&, loops, subshells,
// $(...), bash -c / eval strings and here-docs fed to interpreters). Read-only
// is an allowlist: anything not recognised as harmless is at least a change,
// and code the classifier cannot see (dynamic command names, scripts on
// stdin) is destructive so it always needs approval. Arguments only known at
// runtime take the values the script gives them (a for loop over a literal
// list, a literal assignment) or else are probed with hostile values: the
// command stays a read only if it is one whatever the value. It is a safety
// net for approvals, not a sandbox.
func ClassifyCommand(cmd string) (risk string, reasons []string) {
	a := AnalyzeCommand(cmd)
	return a.Risk, a.Reasons
}

// CommandPart is one simple command of a script, for permission rules that
// match commands: Text as written, Command without wrappers such as sudo.
// A write through a redirection, a destructive SQL statement and a variable
// such as PATH set for later commands are parts of their own (no Command).
type CommandPart struct {
	Text    string
	Command string
	Risk    string
}

// Analysis is the classification of a script and its simple commands.
type Analysis struct {
	Risk    string
	Reasons []string
	Parts   []CommandPart
}

// AnalyzeCommand classifies a script and lists its simple commands (those
// inside bash -c strings and here-docs fed to a shell instead of the shell).
func AnalyzeCommand(cmd string) Analysis {
	parts := []CommandPart{}
	r, why := classifyScript(cmd, 0, &parts)
	if _, err := syntax.NewParser().Parse(strings.NewReader(cmd), ""); err != nil {
		parts = nil
	}
	return Analysis{Risk: r, Reasons: why, Parts: parts}
}

const maxDepth = 4

func classifyScript(cmd string, depth int, parts *[]CommandPart) (risk string, reasons []string) {
	if depth > maxDepth {
		return RiskDestructive, []string{"too deeply nested"}
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return RiskChange, []string{"command could not be parsed"}
	}
	risk = RiskRead
	note := func(r string, why ...string) {
		risk = maxRisk(risk, r)
		if r != RiskRead {
			reasons = append(reasons, why...)
		}
	}
	// other is risk that does not belong to one command (a write through a
	// redirection, a variable for the commands after it): it is listed as a
	// part of its own, so a rule allowing a command never covers it
	other := func(text, r, why string) {
		note(r, why)
		if parts != nil {
			*parts = append(*parts, CommandPart{Text: text, Risk: r})
		}
	}
	upper := strings.ToUpper(cmd)
	for _, pat := range sqlDestructive {
		if pat.MatchString(upper) {
			other("destructive SQL statement", RiskDestructive, "destructive SQL/data statement")
		}
	}
	vars := scriptVars(f)
	stdin := map[*syntax.CallExpr]string{} // here-doc / here-string bodies per command
	syntax.Walk(f, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.Stmt:
			if call, ok := x.Cmd.(*syntax.CallExpr); ok {
				for _, rd := range x.Redirs {
					switch rd.Op {
					case syntax.Hdoc, syntax.DashHdoc:
						if rd.Hdoc != nil {
							stdin[call] += wordLit(rd.Hdoc)
						}
					case syntax.WordHdoc:
						if rd.Word != nil {
							stdin[call] += wordLit(rd.Word)
						}
					}
				}
			}
		case *syntax.Redirect:
			if x.Word != nil {
				target := wordLit(x.Word)
				switch x.Op {
				case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut, syntax.RdrInOut:
					if target != "/dev/null" && target != "/dev/stderr" && target != "/dev/stdout" {
						other(printNode(x), RiskChange, "writes to file "+target)
					}
				}
			}
		case *syntax.DeclClause:
			// export / declare / local / readonly: a variable that changes how
			// later commands run (PATH, LD_PRELOAD...) makes the script a change
			for _, a := range x.Args {
				if a.Name != nil && dangerousVar(a.Name.Value) {
					other(printNode(x), RiskChange, "sets "+a.Name.Value+" for the commands after it")
				}
			}
		case *syntax.CallExpr:
			if len(x.Args) == 0 { // plain variable assignment
				for _, a := range x.Assigns {
					if a.Name != nil && dangerousVar(a.Name.Value) {
						other(printNode(x), RiskChange, "sets "+a.Name.Value+" for the commands after it")
					}
				}
				return true
			}
			for _, a := range x.Assigns {
				if a.Name != nil && !safeEnv(a.Name.Value) {
					note(RiskChange, "runs with environment override "+a.Name.Value)
				}
			}
			args := make([]string, len(x.Args))
			var dyn []int
			for i, w := range x.Args {
				lit, ok := wordLiteral(w)
				if !ok {
					if i == 0 && !knownValues(w, vars) {
						note(RiskDestructive, "command name is computed at runtime")
						return true
					}
					dyn = append(dyn, i)
				}
				args[i] = lit
			}
			body, hasStdin := stdin[x]
			var r string
			var why []string
			if len(dyn) == 0 {
				r, why = classifyArgs(args, body, hasStdin, depth, parts)
			} else {
				r, why = classifyDynamic(x.Args, args, dyn, vars, body, hasStdin, depth)
			}
			note(r, why...)
			if parts != nil && !runsNestedScript(args, hasStdin) {
				*parts = append(*parts, CommandPart{Text: printNode(x), Command: strings.Join(stripWrappers(args), " "), Risk: r})
			}
		}
		return true
	})
	return risk, dedupe(reasons)
}

// runsNestedScript reports whether a command runs a shell script given as a
// string or here-doc, whose own commands are listed instead of the shell.
func runsNestedScript(args []string, hasStdin bool) bool {
	a := stripWrappers(args)
	if len(a) == 0 {
		return false
	}
	cmd := baseName(a[0])
	if cmd == "eval" {
		return true
	}
	if !interpreters[cmd] {
		return false
	}
	_, c := flagValue(a, "-c", "")
	return c || (len(positional(a)) == 0 && hasStdin)
}

func printNode(n syntax.Node) string {
	var b strings.Builder
	syntax.NewPrinter().Print(&b, n)
	return strings.TrimSpace(b.String())
}

// Values tried for an argument only known at runtime: it may become a
// subcommand, an option, several words or a sed/awk/shell program.
var dynamicProbes = []string{"x", "rm", "rm -rf /", "-rf", "--force", "-delete", "--delete", "delete", "restart",
	"drop", "-i", "-w", "-e", "-c", "-o /tmp/x", "-o/tmp/x", "--output=/tmp/x", "-X POST", "-d x", "-exec rm {} +",
	"1w /tmp/x", "e id", `{system("id")}`, "FLUSHALL", "POST"}

const maxCombinations = 64

// classifyDynamic classifies a command whose arguments contain expansions,
// as the highest risk over the values the arguments can take.
func classifyDynamic(words []*syntax.Word, args []string, dyn []int, vars map[string][]string, body string, hasStdin bool, depth int) (string, []string) {
	risk, reasons := RiskRead, []string(nil)
	try := func(argv []string) bool {
		r, why := classifyArgs(argv, body, hasStdin, depth+1, nil)
		if riskRank(r) > riskRank(risk) {
			risk, reasons = r, why
		}
		return risk != RiskDestructive
	}
	// every argument at once with the values the script gives it
	known := true
	var each [][][]string
	for _, i := range dyn {
		v, ok := expandWord(words[i], vars)
		if !ok {
			known = false
			break
		}
		each = append(each, v)
	}
	if known {
		n := 1
		for _, v := range each {
			n *= len(v)
		}
		if n <= maxCombinations {
			idx := make([]int, len(dyn))
			for {
				var argv []string
				d := 0
				for i, a := range args {
					if d < len(dyn) && dyn[d] == i {
						argv = append(argv, each[d][idx[d]]...)
						d++
					} else {
						argv = append(argv, a)
					}
				}
				if !try(argv) {
					return risk, reasons
				}
				k := 0
				for k < len(idx) {
					idx[k]++
					if idx[k] < len(each[k]) {
						break
					}
					idx[k] = 0
					k++
				}
				if k == len(idx) {
					return risk, reasons
				}
			}
		}
	}
	// otherwise one argument at a time with hostile values, the others neutral
	for _, i := range dyn {
		prefix, suffix, quoted := wordShape(words[i])
		for _, p := range dynamicProbes {
			var argv []string
			for j, a := range args {
				switch {
				case j == i && quoted:
					argv = append(argv, prefix+p+suffix)
				case j == i:
					w := strings.Fields(p)
					w[0] = prefix + w[0]
					w[len(w)-1] += suffix
					argv = append(argv, w...)
				case slices.Contains(dyn, j):
					argv = append(argv, "x")
				default:
					argv = append(argv, a)
				}
			}
			if !try(argv) {
				break
			}
		}
	}
	if risk != RiskRead {
		reasons = append([]string{"depends on values only known when it runs"}, reasons...)
	}
	return risk, reasons
}

// wordShape describes an argument with expansions: the literal text before
// and after them, and whether they are quoted (one word whatever the value).
func wordShape(w *syntax.Word) (prefix, suffix string, quoted bool) {
	quoted = true
	seen := false
	add := func(s string) {
		if seen {
			suffix += s
		} else {
			prefix += s
		}
	}
	for _, p := range w.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			add(x.Value)
		case *syntax.SglQuoted:
			add(x.Value)
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				if l, ok := q.(*syntax.Lit); ok {
					add(l.Value)
				} else {
					seen, suffix = true, ""
				}
			}
		default:
			seen, suffix, quoted = true, "", false
		}
	}
	return prefix, suffix, quoted
}

// scriptVars collects the values of variables that only ever get literal
// values in the script (for loops over literal lists, literal assignments).
// Variables set any other way, or not set in the script, are left out.
func scriptVars(f *syntax.File) map[string][]string {
	vals := map[string][]string{}
	unknown := map[string]bool{}
	set := func(name string, w *syntax.Word) {
		if w == nil {
			vals[name] = append(vals[name], "")
			return
		}
		if v, ok := wordLiteral(w); ok {
			vals[name] = append(vals[name], v)
		} else {
			unknown[name] = true
		}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.WordIter:
			if x.Name == nil {
				return true
			}
			if len(x.Items) == 0 && x.InPos == (syntax.Pos{}) {
				unknown[x.Name.Value] = true // for v; do (the positional parameters)
			}
			for _, w := range x.Items {
				set(x.Name.Value, w)
			}
		case *syntax.CStyleLoop, *syntax.ArithmCmd, *syntax.ArithmExp:
			syntax.Walk(x, func(m syntax.Node) bool {
				if l, ok := m.(*syntax.Lit); ok {
					unknown[l.Value] = true
				}
				return true
			})
		case *syntax.DeclClause:
			for _, a := range x.Args {
				if a.Name == nil {
					continue
				}
				if a.Array != nil || a.Append || a.Index != nil {
					unknown[a.Name.Value] = true
				} else {
					set(a.Name.Value, a.Value)
				}
			}
		case *syntax.CallExpr:
			if len(x.Args) == 0 {
				for _, a := range x.Assigns {
					if a.Name == nil {
						continue
					}
					if a.Array != nil || a.Append || a.Index != nil {
						unknown[a.Name.Value] = true
					} else {
						set(a.Name.Value, a.Value)
					}
				}
				return true
			}
			// commands that set variables by name (read x, mapfile x, printf -v x...)
			for _, w := range x.Args[1:] {
				if v, ok := wordLiteral(w); ok {
					unknown[v] = true
				}
			}
		}
		return true
	})
	for k := range unknown {
		delete(vals, k)
	}
	return vals
}

// expandWord returns the words an argument can expand to when every
// expansion in it is a plain variable with known values.
func expandWord(w *syntax.Word, vars map[string][]string) ([][]string, bool) {
	type piece struct {
		lits   []string
		quoted bool
	}
	var pieces []piece
	param := func(p *syntax.ParamExp, quoted bool) bool {
		if p.Param == nil || p.Excl || p.Length || p.Width || p.Index != nil || p.Slice != nil || p.Repl != nil || p.Exp != nil || p.Names != 0 {
			return false
		}
		v, ok := vars[p.Param.Value]
		if !ok {
			return false
		}
		pieces = append(pieces, piece{v, quoted})
		return true
	}
	for _, p := range w.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			pieces = append(pieces, piece{[]string{x.Value}, true})
		case *syntax.SglQuoted:
			pieces = append(pieces, piece{[]string{x.Value}, true})
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				switch y := q.(type) {
				case *syntax.Lit:
					pieces = append(pieces, piece{[]string{y.Value}, true})
				case *syntax.ParamExp:
					if !param(y, true) {
						return nil, false
					}
				default:
					return nil, false
				}
			}
		case *syntax.ParamExp:
			if !param(x, false) {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	out := [][]string{{""}}
	for _, pc := range pieces {
		var next [][]string
		for _, prev := range out {
			for _, v := range pc.lits {
				words := append([]string(nil), prev...)
				if pc.quoted {
					words[len(words)-1] += v
				} else if f := strings.Fields(v); len(f) > 0 {
					words[len(words)-1] += f[0]
					words = append(words, f[1:]...)
				}
				next = append(next, words)
			}
		}
		if len(next) > maxCombinations {
			return nil, false
		}
		out = next
	}
	return out, true
}

// knownValues reports whether a command name expands to known literal values.
func knownValues(w *syntax.Word, vars map[string][]string) bool {
	v, ok := expandWord(w, vars)
	if !ok {
		return false
	}
	for _, words := range v {
		if len(words) == 0 || words[0] == "" {
			return false
		}
	}
	return true
}

// safeEnv: variables that may prefix a read-only command without making it
// a change (they only change formatting, paging or which server is asked).
func safeEnv(name string) bool {
	if strings.HasPrefix(name, "LC_") {
		return true
	}
	switch name {
	case "LANG", "LANGUAGE", "TZ", "TERM", "COLUMNS", "LINES", "NO_COLOR", "IFS", "SYSTEMD_COLORS", "SYSTEMD_LESS",
		"KUBECONFIG", "DOCKER_HOST", "DOCKER_CONTEXT", "PGHOST", "PGPORT", "PGUSER", "PGDATABASE", "PGPASSWORD",
		"PGSSLMODE", "MYSQL_PWD", "REDISCLI_AUTH", "AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "GREP_COLORS":
		return true
	}
	return false
}

// dangerousVar: variables that change which program runs or what it loads,
// so setting them for the rest of a script is never a read.
func dangerousVar(name string) bool {
	for _, p := range []string{"LD_", "DYLD_", "GIT_", "PYTHON", "PERL5", "RUBY", "BASH_FUNC_", "XDG_"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	switch name {
	case "PATH", "HOME", "ENV", "BASH_ENV", "PROMPT_COMMAND", "SHELLOPTS", "BASHOPTS", "PS4", "NODE_OPTIONS",
		"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "LESSOPEN", "LESSCLOSE", "PAGER", "MANPAGER", "SYSTEMD_PAGER",
		"EDITOR", "VISUAL", "SSH_ASKPASS", "SUDO_ASKPASS", "GCONV_PATH", "SSL_CERT_FILE", "CURL_HOME", "WGETRC":
		return true
	}
	return false
}

var sqlDestructive = []*regexp.Regexp{
	regexp.MustCompile(`\bDROP\s+(DATABASE|TABLE|SCHEMA|INDEX|USER|ROLE)\b`),
	regexp.MustCompile(`\bTRUNCATE\s+(TABLE\s+)?\w`),
	regexp.MustCompile(`\bDELETE\s+FROM\s+\S+\s*(;|"|'|$)`),
	regexp.MustCompile(`\bFLUSHALL\b|\bFLUSHDB\b`),
	regexp.MustCompile(`\bDB\.DROPDATABASE\(`),
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func wordLit(w *syntax.Word) string {
	s, _ := wordLiteral(w)
	return s
}

// wordLiteral returns the literal text of a word; ok=false when it contains
// expansions (the text then has them stripped).
func wordLiteral(w *syntax.Word) (string, bool) {
	var b strings.Builder
	ok := true
	for _, p := range w.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			b.WriteString(x.Value)
		case *syntax.SglQuoted:
			b.WriteString(x.Value)
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				if l, isLit := q.(*syntax.Lit); isLit {
					b.WriteString(l.Value)
				} else {
					ok = false
				}
			}
		default:
			ok = false
		}
	}
	return b.String(), ok
}

func baseName(cmd string) string {
	if i := strings.LastIndex(cmd, "/"); i >= 0 {
		return cmd[i+1:]
	}
	return cmd
}

// Read-only regardless of arguments. Commands with write modes are handled
// explicitly in classifyArgs instead.
var readOnly = map[string]bool{
	"ls": true, "ll": true, "cat": true, "head": true, "tail": true, "less": true, "more": true, "grep": true,
	"egrep": true, "fgrep": true, "rg": true, "zgrep": true, "zcat": true, "bzcat": true, "xzcat": true, "wc": true,
	"cut": true, "tr": true, "jq": true, "column": true, "stat": true,
	"file": true, "du": true, "df": true, "free": true, "uptime": true, "w": true, "who": true, "whoami": true,
	"id": true, "uname": true, "ps": true, "pgrep": true, "pidof": true, "vmstat": true,
	"iostat": true, "mpstat": true, "sar": true, "lsof": true, "ss": true, "netstat": true, "ping": true,
	"ping6": true, "traceroute": true, "tracepath": true, "mtr": true, "dig": true, "nslookup": true, "host": true,
	"printenv": true, "echo": true, "printf": true, "which": true, "whereis": true, "type": true,
	"test": true, "[": true, "true": true, "false": true, "sleep": true, "pwd": true, "cd": true,
	"basename": true, "dirname": true, "realpath": true, "readlink": true, "md5sum": true, "sha1sum": true,
	"sha256sum": true, "sha512sum": true, "base64": true, "xxd": true, "hexdump": true, "od": true,
	"strings": true, "diff": true, "cmp": true, "tree": true, "lsblk": true, "blkid": true, "findmnt": true,
	"lscpu": true, "lsmod": true, "lspci": true, "lsusb": true, "getent": true, "last": true, "lastlog": true,
	"nproc": true, "arch": true, "seq": true, "tac": true, "nl": true, "fold": true, "paste": true, "join": true,
	"comm": true, "expand": true, "top": true, "htop": true, "iotop": true, "nload": true, "iftop": true,
	"apt-cache": true, "dpkg-query": true, "lsb_release": true, "getfacl": true, "namei": true, "ldd": true,
	"groups": true, "systemd-analyze": true, "iptables-save": true, "ip6tables-save": true, "showmount": true,
	"locale": true, "sensors": true, "lshw": true, "dmidecode": true, "hostid": true, "getconf": true, "pstree": true,
	"pmap": true, "pidstat": true, "lsns": true, "lslocks": true, "lsipc": true, "lsattr": true, "systemd-cgls": true,
	"systemd-cgtop": true, "systemd-detect-virt": true, "virt-what": true, "ntpstat": true, "numactl": true,
	"tty": true, "logname": true, "users": true, "lastb": true, "pinky": true, "cal": true, "expr": true, "numfmt": true,
	"zless": true, "zmore": true, "bzless": true, "zstdcat": true, "lz4cat": true, "bzgrep": true, "xzgrep": true,
	"zstdgrep": true, "sdiff": true, "colordiff": true, "atop": true, "glances": true, "nethogs": true, "dstat": true,
	"ssh-keyscan": true, "whois": true, "ldapsearch": true, "ntpq": true,
	"btop": true, "ncdu": true, "duf": true, "dust": true, "procs": true, "bat": true, "batcat": true, "fd": true, "fdfind": true,
}

// Prefix wrappers that run the rest of the line as a command, with the
// options that take a value.
var wrappers = map[string][]string{
	"sudo": {"-u", "-g", "-C", "-h", "-p", "-r", "-t", "-U", "-D"}, "doas": {"-u", "-C"},
	"nice": {"-n"}, "ionice": {"-c", "-n", "-p"}, "nohup": nil, "time": {"-f", "-o"}, "stdbuf": {"-i", "-o", "-e"},
	"timeout": {"-s", "--signal", "-k", "--kill-after"}, "command": nil, "exec": {"-a"}, "xargs": {"-I", "-n", "-P", "-d", "-L", "-s", "-E", "-a"},
	"watch": {"-n", "--interval", "-d"}, "chroot": nil, "nsenter": {"-t", "--target"}, "setsid": nil, "unbuffer": nil,
	"env": {"-u", "--unset", "-C", "--chdir", "-S"},
}

func stripWrappers(args []string) []string {
	for len(args) > 0 {
		cmd := baseName(args[0])
		valueOpts, ok := wrappers[cmd]
		if !ok {
			return args
		}
		if cmd == "command" && len(args) > 1 && in(args[1], "-v", "-V") {
			return []string{"true"} // only looks the name up
		}
		i := 1
		for i < len(args) && (strings.HasPrefix(args[i], "-") || (cmd == "env" && strings.Contains(args[i], "="))) {
			if args[i] == "--" {
				i++
				break
			}
			if in(args[i], valueOpts...) {
				i++
			}
			i++
		}
		if cmd == "timeout" && i < len(args) {
			i++ // duration
		}
		if cmd == "chroot" && i < len(args) {
			i++ // new root
		}
		if i >= len(args) {
			if cmd == "env" {
				return []string{"printenv"}
			}
			return []string{"true"}
		}
		args = args[i:]
	}
	return args
}

func has(args []string, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a == f || (strings.HasPrefix(f, "--") && strings.HasPrefix(a, f+"=")) {
				return true
			}
			// combined short flags: -rf contains r and f
			if len(f) == 2 && f[0] == '-' && f[1] != '-' && len(a) > 2 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a[1:], rune(f[1])) {
				return true
			}
		}
	}
	return false
}

// flagValue returns the value of -X v / -Xv / --long v / --long=v.
func flagValue(args []string, short, long string) (string, bool) {
	for i, a := range args {
		switch {
		case (a == short || a == long) && i+1 < len(args):
			return args[i+1], true
		case long != "" && strings.HasPrefix(a, long+"="):
			return strings.TrimPrefix(a, long+"="), true
		case short != "" && len(a) > len(short) && strings.HasPrefix(a, short) && !strings.HasPrefix(a, "--"):
			return a[len(short):], true
		}
	}
	return "", false
}

// positional returns the non-flag arguments after the command.
func positional(args []string) []string {
	var out []string
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

func sub(args []string, n int) string {
	p := positional(args)
	if n-1 < len(p) {
		return p[n-1]
	}
	return ""
}

func in(s string, list ...string) bool {
	for _, x := range list {
		if s == x {
			return true
		}
	}
	return false
}

var interpreters = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true, "fish": true}
var scriptRunners = map[string]bool{"python": true, "python3": true, "perl": true, "ruby": true, "node": true, "php": true, "lua": true}

func classifyArgs(rawArgs []string, stdinBody string, hasStdin bool, depth int, parts *[]CommandPart) (string, []string) {
	args := stripWrappers(rawArgs)
	if len(args) == 0 {
		return RiskRead, nil
	}
	one := func(r, why string) (string, []string) { return r, []string{why} }
	cmd := baseName(args[0])
	s1, s2 := sub(args, 1), sub(args, 2)

	if (cmd == "source" || cmd == ".") && len(args) == 2 && in(args[1], "/etc/os-release", "/usr/lib/os-release", "/etc/lsb-release") {
		return RiskRead, nil // variables describing the system
	}
	// Code passed to another interpreter is classified recursively.
	if interpreters[cmd] || cmd == "eval" || cmd == "source" || cmd == "." {
		if cmd == "eval" {
			return classifyScript(strings.Join(args[1:], " "), depth+1, parts)
		}
		if c, ok := flagValue(args, "-c", ""); ok {
			return classifyScript(c, depth+1, parts)
		}
		if len(positional(args)) == 0 && has(args, "--version") {
			return RiskRead, nil
		}
		if len(positional(args)) == 0 && hasStdin {
			return classifyScript(stdinBody, depth+1, parts)
		}
		if len(positional(args)) == 0 {
			return one(RiskDestructive, "runs shell code from stdin that cannot be inspected")
		}
		return one(RiskDestructive, "runs a script file that cannot be inspected")
	}
	if scriptRunners[cmd] {
		if len(args) == 2 && in(args[1], "--version", "-V", "-v") {
			return RiskRead, nil
		}
		return one(RiskDestructive, "runs "+cmd+" code that cannot be inspected")
	}

	switch cmd {
	case "rm":
		if has(args, "-r", "-R", "--recursive", "-f", "--force") {
			return one(RiskDestructive, "rm -r/-f deletes files")
		}
		return one(RiskChange, "rm deletes files")
	case "dd", "mkfs", "fdisk", "sfdisk", "parted", "wipefs", "shred", "sgdisk", "mkswap", "pvremove", "vgremove", "lvremove", "cryptsetup", "mkfs.ext4", "mkfs.xfs", "mkfs.btrfs", "mkfs.vfat":
		return one(RiskDestructive, cmd+" can destroy data")
	case "shutdown", "reboot", "halt", "poweroff", "kexec", "telinit":
		return one(RiskDestructive, cmd+" takes the host down")
	case "init":
		if in(s1, "0", "6") {
			return one(RiskDestructive, "init 0/6 takes the host down")
		}
		return one(RiskChange, "init")
	case "userdel", "deluser", "groupdel", "delgroup", "killall5":
		return one(RiskDestructive, cmd)
	case "truncate":
		return one(RiskDestructive, "truncate erases file contents")
	case "kill", "pkill", "killall":
		if has(args, "-9", "-KILL", "-SIGKILL") || in("-1", args...) {
			return one(RiskDestructive, "force kill")
		}
		if cmd == "kill" && has(args, "-l") {
			return RiskRead, nil
		}
		return one(RiskChange, "stops processes")
	case "chmod", "chown", "chgrp":
		if has(args, "-R", "--recursive") {
			for _, a := range args {
				if in(strings.TrimRight(a, "/"), "", "/etc", "/usr", "/var", "/home", "/bin", "/lib", "/boot", "/root") {
					return one(RiskDestructive, "recursive permission change on a system directory")
				}
			}
		}
		return one(RiskChange, "changes permissions")
	case "iptables", "ip6tables":
		if has(args, "-F", "--flush", "-X", "--delete-chain", "-P", "--policy") {
			return one(RiskDestructive, "flushes firewall rules / changes default policy")
		}
		if has(args, "-L", "--list", "-S", "--list-rules") && !has(args, "-A", "-I", "-D", "-R", "-N", "-Z") {
			return RiskRead, nil
		}
		return one(RiskChange, "changes firewall rules")
	case "nft":
		if s1 == "list" {
			return RiskRead, nil
		}
		if s1 == "flush" {
			return one(RiskDestructive, "flushes nftables")
		}
		return one(RiskChange, "changes firewall rules")
	case "ufw":
		if in(s1, "status") {
			return RiskRead, nil
		}
		if in(s1, "disable", "reset") {
			return one(RiskDestructive, "disables/resets the firewall")
		}
		return one(RiskChange, "changes firewall rules")
	case "crontab":
		if has(args, "-r") {
			return one(RiskDestructive, "removes the crontab")
		}
		rest := args
		if u, ok := flagValue(args, "-u", ""); ok {
			rest = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == u })
		}
		if has(args, "-l") && len(positional(rest)) == 0 {
			return RiskRead, nil
		}
		return one(RiskChange, "edits the crontab")
	case "sed":
		return classifySed(args)
	case "awk", "gawk", "mawk":
		if _, ok := flagValue(args, "-f", "--file"); ok || has(args, "-i", "--include", "-l", "--load", "-E", "--exec") {
			return one(RiskChange, "awk program from a file or extension")
		}
		prog := awkProgram(args)
		if awkWrites(prog) {
			return one(RiskChange, "awk program writes files or runs commands")
		}
		return RiskRead, nil
	case "sort":
		if has(args, "-o", "--output") {
			return one(RiskChange, "sort -o writes a file")
		}
		return RiskRead, nil
	case "uniq":
		if len(positional(args)) >= 2 {
			return one(RiskChange, "uniq writes an output file")
		}
		return RiskRead, nil
	case "yq":
		if has(args, "-i", "--inplace") {
			return one(RiskChange, "yq -i edits files")
		}
		return RiskRead, nil
	case "find":
		if has(args, "-delete") {
			return one(RiskDestructive, "find -delete removes files")
		}
		if has(args, "-fprint", "-fprintf", "-fls", "-fprint0") {
			return one(RiskChange, "find writes files")
		}
		risk, reasons := RiskRead, []string(nil)
		for i, a := range args {
			if in(a, "-ok", "-okdir") {
				return one(RiskChange, "find asks before each command")
			}
			if !in(a, "-exec", "-execdir") {
				continue
			}
			var inner []string
			for _, b := range args[i+1:] {
				if b == ";" || b == "+" || b == `\;` {
					break
				}
				// find passes paths under its starting points, never options
				inner = append(inner, strings.ReplaceAll(b, "{}", "/x"))
			}
			r, why := classifyArgs(inner, "", false, depth+1, nil)
			if len(inner) > 0 && in(baseName(stripWrappers(inner)[0]), "rm", "unlink", "shred", "truncate") {
				r, why = RiskDestructive, []string{"find deletes every file it matches"}
			}
			if riskRank(r) > riskRank(risk) {
				risk, reasons = r, append([]string{"find runs a command per file"}, why...)
			}
		}
		return risk, reasons
	case "curl":
		method := ""
		if v, ok := flagValue(args, "-X", "--request"); ok {
			method = strings.ToUpper(v)
		}
		if method != "" && !in(method, "GET", "HEAD", "OPTIONS") {
			return one(RiskChange, "HTTP "+method+" request")
		}
		if has(args, "-d", "--data", "--data-raw", "--data-binary", "--data-urlencode", "-F", "--form", "-T", "--upload-file", "--json") {
			return one(RiskChange, "HTTP request with a body")
		}
		if has(args, "-O", "--remote-name", "--remote-name-all", "-K", "--config", "-c", "--cookie-jar", "--trace", "--trace-ascii", "--libcurl", "--etag-save", "--hsts", "--alt-svc") {
			return one(RiskChange, "downloads to a file / reads config")
		}
		for _, f := range [][2]string{{"-o", "--output"}, {"-D", "--dump-header"}, {"", "--stderr"}} {
			if v, ok := flagValue(args, f[0], f[1]); ok && !in(v, "/dev/null", "-", "/dev/stdout", "/dev/stderr") {
				return one(RiskChange, "curl writes "+v)
			}
		}
		return RiskRead, nil
	case "wget":
		if has(args, "--spider") {
			return RiskRead, nil
		}
		if v, ok := flagValue(args, "-O", "--output-document"); ok && v == "-" {
			if !has(args, "--post-data", "--post-file", "--method") {
				return RiskRead, nil
			}
		}
		if has(args, "-qO-") {
			return RiskRead, nil
		}
		return one(RiskChange, "downloads to a file")
	case "nc", "ncat", "telnet":
		if has(args, "-z") && !has(args, "-e", "-c", "-l") {
			return RiskRead, nil
		}
		return one(RiskChange, "opens a raw connection")
	case "openssl":
		if in(s1, "s_client", "x509", "version", "verify", "ciphers", "crl", "asn1parse", "rand", "dgst", "req", "pkey", "rsa", "ec", "pkcs12", "list", "errstr", "speed") &&
			!has(args, "-out", "-new", "-sign", "-newkey", "-x509toreq", "-export", "-passout", "-writerand") &&
			(!in(s1, "req", "pkey", "rsa", "ec", "pkcs12") || has(args, "-noout", "-text", "-pubout", "-info", "-nokeys")) {
			return RiskRead, nil
		}
		return one(RiskChange, "openssl may write keys/files")
	case "ip":
		obj := s1
		verb := s2
		if obj == "" || (in(obj, "a", "addr", "address", "r", "route", "l", "link", "n", "neigh", "neighbor", "rule", "netns", "maddr", "-s", "-br", "-4", "-6", "-j") && in(verb, "", "show", "list", "ls", "get", "lst")) {
			return RiskRead, nil
		}
		return one(RiskChange, "changes network configuration")
	case "ifconfig", "route", "hostname", "mount", "dmesg", "date", "env":
		if len(args) == 1 || (cmd == "dmesg" && !has(args, "-c", "-C", "--clear", "-D", "-E", "-n")) ||
			(cmd == "route" && len(positional(args)) == 0) || (cmd == "date" && !has(args, "-s", "--set") && len(positional(args)) <= 1 && strings.HasPrefix(s1+"+", "+")) ||
			(cmd == "hostname" && has(args, "-f", "-i", "-I", "-s", "-d") && len(positional(args)) == 0) {
			return RiskRead, nil
		}
		return one(RiskChange, cmd+" changes system state")
	case "umount":
		return one(RiskChange, "unmounts a filesystem")
	case "journalctl":
		if has(args, "--vacuum-size", "--vacuum-time", "--vacuum-files", "--rotate", "--flush", "--sync", "--relinquish-var", "--setup-keys") {
			return one(RiskChange, "journal maintenance")
		}
		return RiskRead, nil
	case "systemctl":
		if in(s1, "status", "show", "is-active", "is-enabled", "is-failed", "list-units", "list-unit-files", "list-timers", "list-sockets", "list-dependencies", "list-jobs", "cat", "get-default", "") {
			return RiskRead, nil
		}
		if in(s1, "poweroff", "reboot", "halt", "kexec", "isolate", "rescue", "emergency", "suspend", "hibernate") {
			return one(RiskDestructive, "systemctl "+s1+" takes the host down")
		}
		return one(RiskChange, "systemctl "+s1+" "+s2)
	case "service":
		if s2 == "status" || has(args, "--status-all") {
			return RiskRead, nil
		}
		return one(RiskChange, "service "+s1+" "+s2)
	case "hostnamectl", "timedatectl", "localectl", "loginctl", "resolvectl", "nmcli", "networkctl", "busctl":
		if s1 == "" || in(s1, "status", "show", "list", "list-sessions", "list-users", "list-seats", "show-session", "show-user",
			"query", "statistics", "dns", "domain", "general", "connection", "device", "radio", "monitor", "tree", "introspect", "timesync-status", "show-timesync", "list-timezones") {
			// "nmcli dev/conn/general" without a modifying verb, "resolvectl dns" without values
			if (in(s1, "dns", "domain") && len(positional(args)) > 1) ||
				(in(s1, "connection", "device", "radio", "general", "networking") && !in(s2, "", "show", "status", "list", "monitor")) {
				return one(RiskChange, cmd+" "+s1+" "+s2)
			}
			return RiskRead, nil
		}
		return one(RiskChange, cmd+" "+s1+" "+s2)
	case "ethtool":
		if has(args, "-s", "--change", "-K", "--offload", "-A", "--pause", "-G", "--set-ring", "-L", "--set-channels", "-C", "--coalesce", "-E", "-f", "--flash", "-r", "--negotiate") {
			return one(RiskChange, "changes NIC settings")
		}
		return RiskRead, nil
	case "exportfs":
		if has(args, "-v", "-s") && !has(args, "-a", "-r", "-u", "-f") {
			return RiskRead, nil
		}
		return one(RiskChange, "changes NFS exports")
	case "rpm":
		if has(args, "-q", "--query", "-V", "--verify") && !has(args, "-e", "--erase", "-i", "--install", "-U", "--upgrade", "-F") {
			return RiskRead, nil
		}
		return one(RiskChange, "rpm modifies packages")
	case "sysctl":
		if has(args, "-w", "--write", "-p", "--load", "--system") || strings.Contains(strings.Join(args, " "), "=") {
			return one(RiskChange, "sysctl changes kernel parameters")
		}
		return RiskRead, nil
	case "git":
		return classifyGit(args)
	// shell builtins that only affect the shell running the script
	case "set", ":", "exit", "return", "break", "continue", "shift", "wait", "read", "mapfile", "readarray",
		"getopts", "let", "unset", "ulimit", "umask", "hash", "pushd", "popd", "dirs", "shopt", "local", "declare", "typeset":
		return RiskRead, nil
	case "trap":
		if p := positional(args); len(p) >= 2 {
			return classifyScript(p[0], depth+1, nil)
		}
		return RiskRead, nil
	case "dpkg":
		if has(args, "-l", "--list", "-L", "--listfiles", "-s", "--status", "-S", "--search", "-p", "--print-avail",
			"--get-selections", "--print-architecture", "--print-foreign-architectures", "-c", "--contents", "-I", "--info", "--audit", "-C", "--compare-versions") &&
			!has(args, "-i", "--install", "-r", "--remove", "-P", "--purge", "--configure", "--unpack", "--set-selections", "--clear-selections") {
			return RiskRead, nil
		}
		return one(RiskChange, "dpkg changes packages")
	case "apt-mark":
		if strings.HasPrefix(s1, "show") {
			return RiskRead, nil
		}
		return one(RiskChange, "apt-mark "+s1)
	case "update-alternatives":
		if has(args, "--display", "--query", "--list", "--get-selections") {
			return RiskRead, nil
		}
		return one(RiskChange, "changes alternatives")
	case "pacman":
		if len(args) > 1 && (strings.HasPrefix(args[1], "-Q") || in(args[1], "-Ss", "-Si", "-Sl", "-Sg")) {
			return RiskRead, nil
		}
		return one(RiskChange, "pacman changes packages")
	case "zpool":
		switch s1 {
		case "status", "list", "iostat", "get", "history", "events", "version":
			return RiskRead, nil
		case "destroy", "labelclear", "create", "split":
			return one(RiskDestructive, "zpool "+s1+" can destroy data")
		}
		return one(RiskChange, "zpool "+s1)
	case "zfs":
		switch s1 {
		case "list", "get", "holds", "userspace", "groupspace", "projectspace", "diff", "version":
			return RiskRead, nil
		case "destroy", "rollback":
			return one(RiskDestructive, "zfs "+s1+" removes data")
		}
		return one(RiskChange, "zfs "+s1)
	case "pvs", "vgs", "lvs", "pvdisplay", "vgdisplay", "lvdisplay", "pvscan", "vgscan", "lvscan":
		if has(args, "--cache", "--mknodes", "-a", "--activate", "--uuid") {
			return one(RiskChange, cmd+" changes LVM state")
		}
		return RiskRead, nil
	case "mdadm":
		if has(args, "-D", "--detail", "-E", "--examine", "-Q", "--query", "--detail-platform") &&
			!has(args, "--fail", "-f", "--remove", "-r", "--add", "-a", "--stop", "-S", "--zero-superblock", "--grow", "-G", "--create", "-C", "--assemble", "-A") {
			return RiskRead, nil
		}
		return one(RiskChange, "mdadm changes arrays")
	case "smartctl":
		if has(args, "-t", "--test", "-s", "--smart", "-o", "--offlineauto", "-S", "--saveauto", "-X", "--abort", "-g", "--get", "--set") {
			return one(RiskChange, "smartctl changes drive settings or starts a test")
		}
		return RiskRead, nil
	case "ssh-keygen":
		if has(args, "-l", "-F", "-y", "-B") && !has(args, "-R", "-H", "-p", "-c", "-N") {
			return RiskRead, nil
		}
		return one(RiskChange, "ssh-keygen writes keys or known_hosts")
	case "firewall-cmd":
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "--list-") && !strings.HasPrefix(a, "--get-") && !strings.HasPrefix(a, "--query-") && !strings.HasPrefix(a, "--info-") &&
				!in(a, "--state", "--check-config", "--permanent") && !strings.HasPrefix(a, "--zone=") && !strings.HasPrefix(a, "--policy=") {
				return one(RiskChange, "changes the firewall")
			}
		}
		return RiskRead, nil
	case "fail2ban-client":
		if in(s1, "status", "ping", "version", "get", "banned", "echo") {
			return RiskRead, nil
		}
		return one(RiskChange, "fail2ban-client "+s1)
	case "certbot":
		if in(s1, "certificates", "plugins", "show_account") || has(args, "--version") {
			return RiskRead, nil
		}
		return one(RiskChange, "certbot "+s1)
	case "haproxy", "sshd", "named-checkconf", "named-checkzone", "visudo", "caddy", "promtool", "postconf":
		if (cmd == "haproxy" && has(args, "-c", "-v", "-vv")) || (cmd == "sshd" && has(args, "-t", "-T")) ||
			in(cmd, "named-checkconf", "named-checkzone") || (cmd == "visudo" && has(args, "-c", "--check")) ||
			(cmd == "caddy" && in(s1, "validate", "version", "list-modules", "build-info", "environ", "adapt", "fmt") && !has(args, "--overwrite", "-w")) ||
			(cmd == "promtool" && in(s1, "check", "query")) || (cmd == "postconf" && !has(args, "-e", "-X", "-#", "-M", "-P")) {
			return RiskRead, nil
		}
		return one(RiskChange, cmd)
	case "nvidia-smi":
		if has(args, "-pm", "--persistence-mode", "-pl", "--power-limit", "-ac", "--applications-clocks", "-rac", "-lgc", "-rgc",
			"-r", "--gpu-reset", "-c", "--compute-mode", "-e", "--ecc-config", "-mig", "--multi-instance-gpu", "-am") {
			return one(RiskChange, "nvidia-smi changes GPU settings")
		}
		return RiskRead, nil
	case "chronyc":
		if s1 == "" || in(s1, "tracking", "sources", "sourcestats", "activity", "ntpdata", "clients", "serverstats", "selectdata", "authdata", "rtcdata", "smoothing", "waitsync") {
			return RiskRead, nil
		}
		return one(RiskChange, "chronyc "+s1)
	case "arp":
		if has(args, "-d", "--delete", "-s", "--set", "-f", "--file") {
			return one(RiskChange, "changes the ARP table")
		}
		return RiskRead, nil
	case "tc", "bridge":
		if (s1 != "" && in(s2, "", "show", "ls", "list")) || (s1 == "monitor") {
			return RiskRead, nil
		}
		return one(RiskChange, cmd+" changes network configuration")
	case "conntrack":
		if has(args, "-L", "--dump", "-S", "--stats", "-C", "--count", "-G", "--get", "-E", "--event") && !has(args, "-D", "--delete", "-F", "--flush", "-U", "--update", "-I", "--create") {
			return RiskRead, nil
		}
		return one(RiskChange, "changes connection tracking")
	case "ipset":
		if in(s1, "list", "save", "test", "-L", "-S", "-T", "version") {
			return RiskRead, nil
		}
		return one(RiskChange, "ipset "+s1)
	case "tcpdump":
		if has(args, "-w", "-z", "-Z") {
			return one(RiskChange, "tcpdump writes a capture file or runs a command")
		}
		return RiskRead, nil
	case "fuser":
		if has(args, "-k", "--kill") {
			return one(RiskChange, "fuser -k kills processes")
		}
		return RiskRead, nil
	case "ldconfig":
		if has(args, "-p", "--print-cache") {
			return RiskRead, nil
		}
		return one(RiskChange, "rebuilds the library cache")
	case "gzip", "gunzip", "bzip2", "bunzip2", "xz", "unxz", "zstd", "unzstd", "lz4":
		if has(args, "-c", "--stdout", "-l", "--list", "-t", "--test") && !has(args, "-f", "--force") {
			return RiskRead, nil
		}
		return one(RiskChange, cmd+" writes files")
	case "pg_isready", "pg_lsclusters", "pg_controldata", "mongostat", "mongotop":
		return RiskRead, nil
	case "mysqladmin":
		ps := positional(args)
		for _, p := range ps {
			if !in(p, "status", "ping", "version", "processlist", "variables", "extended-status") {
				if in(p, "shutdown", "drop") {
					return one(RiskDestructive, "mysqladmin "+p)
				}
				return one(RiskChange, "mysqladmin "+p)
			}
		}
		if len(ps) == 0 {
			return one(RiskChange, "mysqladmin")
		}
		return RiskRead, nil
	case "virsh":
		if in(s1, "list", "dominfo", "domstate", "dumpxml", "net-list", "net-info", "net-dumpxml", "pool-list", "pool-info", "vol-list",
			"nodeinfo", "version", "domblklist", "domiflist", "domstats", "capabilities", "vcpuinfo", "domblkinfo", "dommemstat", "snapshot-list", "uri", "hostname") {
			return RiskRead, nil
		}
		if in(s1, "destroy", "undefine", "vol-delete", "pool-delete", "snapshot-delete") {
			return one(RiskDestructive, "virsh "+s1)
		}
		return one(RiskChange, "virsh "+s1)
	case "qm", "pct":
		if in(s1, "list", "status", "config", "pending", "listsnapshot") {
			return RiskRead, nil
		}
		if in(s1, "destroy") {
			return one(RiskDestructive, cmd+" destroy")
		}
		return one(RiskChange, cmd+" "+s1)
	case "crictl":
		if in(s1, "ps", "pods", "images", "img", "inspect", "inspectp", "inspecti", "logs", "stats", "statsp", "info", "version", "imagefsinfo") {
			return RiskRead, nil
		}
		return one(RiskChange, "crictl "+s1)
	case "needrestart":
		if has(args, "-b") || (has(args, "-r") && func() bool { v, _ := flagValue(args, "-r", ""); return v == "l" }()) {
			return RiskRead, nil
		}
		return one(RiskChange, "needrestart may restart services")
	case "gcloud", "az", "doctl", "hcloud":
		return classifyCloudCLI(cmd, args)
	case "docker", "podman", "nerdctl", "docker-compose":
		if cmd == "docker-compose" {
			args = append([]string{"docker", "compose"}, args[1:]...)
		}
		if inner, ok := dockerExecCommand(args); ok {
			r, why := classifyArgs(inner, stdinBody, hasStdin, depth+1, nil)
			return r, prefixReasons("in the container: ", why)
		}
		return classifyDocker(args)
	case "kubectl", "oc", "k":
		if inner, ok := kubectlExecCommand(args); ok {
			r, why := classifyArgs(inner, stdinBody, hasStdin, depth+1, nil)
			return r, prefixReasons("in the pod: ", why)
		}
		return classifyKubectl(args)
	case "helm":
		if in(s1, "list", "ls", "status", "get", "history", "show", "template", "version", "search", "lint", "env") || (s1 == "repo" && in(s2, "list", "ls")) {
			return RiskRead, nil
		}
		if in(s1, "uninstall", "delete", "del", "un") {
			return one(RiskDestructive, "helm uninstall removes a release")
		}
		return one(RiskChange, "helm "+s1)
	case "terraform", "tofu":
		if in(s1, "plan", "show", "validate", "version", "output", "providers", "graph") || (s1 == "fmt" && has(args, "-check")) || (s1 == "state" && in(s2, "list", "show", "pull")) {
			if s1 == "plan" && has(args, "-out") {
				return one(RiskChange, "terraform plan -out writes a file")
			}
			return RiskRead, nil
		}
		if s1 == "destroy" || (s1 == "apply" && has(args, "-destroy")) || (s1 == "state" && in(s2, "rm", "push")) {
			return one(RiskDestructive, "terraform destroys infrastructure")
		}
		return one(RiskChange, "terraform "+s1)
	case "ansible-playbook":
		if has(args, "--syntax-check", "--list-tasks", "--list-hosts", "--list-tags") {
			return RiskRead, nil
		}
		return one(RiskChange, "runs an ansible playbook")
	case "nginx":
		if has(args, "-t", "-T", "-v", "-V") && !has(args, "-s", "-g") {
			return RiskRead, nil
		}
		return one(RiskChange, "nginx signal/start")
	case "apachectl", "apache2ctl", "httpd":
		if (has(args, "-t", "-S", "-M", "-v", "-V") || s1 == "configtest") && !has(args, "-k") {
			return RiskRead, nil
		}
		return one(RiskChange, "apache control")
	case "psql", "mysql", "mariadb", "clickhouse-client", "sqlite3", "mongosh", "mongo":
		return classifySQL(cmd, args)
	case "redis-cli":
		return classifyRedis(args)
	case "apt", "apt-get", "yum", "dnf", "zypper", "apk", "snap", "pip", "pip3", "npm", "gem":
		if in(s1, "list", "ls", "search", "show", "info", "policy", "history", "check-update", "outdated", "freeze", "view",
			"repolist", "provides", "whatprovides", "deplist", "repoquery", "check", "madison", "changelog") || (s1 == "audit" && s2 != "fix") {
			return RiskRead, nil
		}
		if in(s1, "remove", "purge", "autoremove", "erase", "uninstall", "del") {
			return one(RiskChange, "removes packages")
		}
		return one(RiskChange, cmd+" "+s1)
	case "tar":
		if has(args, "-t", "--list") && !has(args, "-x", "-c", "--extract", "--create", "-r", "-u", "--delete") {
			return RiskRead, nil
		}
		return one(RiskChange, "tar writes files")
	case "unzip":
		if has(args, "-l", "-v", "-t", "-Z") {
			return RiskRead, nil
		}
		return one(RiskChange, "unzip writes files")
	case "aws":
		action := s2
		if strings.HasPrefix(action, "describe") || strings.HasPrefix(action, "list") || (strings.HasPrefix(action, "get") && !strings.HasPrefix(action, "get-secret")) || (s1 == "sts" && action == "get-caller-identity") || (s1 == "s3" && action == "ls") {
			return RiskRead, nil
		}
		if strings.HasPrefix(action, "delete") || strings.HasPrefix(action, "terminate") || in(action, "rb", "rm") {
			return one(RiskDestructive, "aws "+s1+" "+action)
		}
		return one(RiskChange, "aws "+s1+" "+action)
	}
	if readOnly[cmd] {
		return RiskRead, nil
	}
	if len(args) == 2 && in(args[1], "--version", "-v", "-V", "version", "-version", "--help", "-h") {
		return RiskRead, nil
	}
	return one(RiskChange, "unrecognised command "+cmd)
}

var (
	sedPrint  = regexp.MustCompile(`^\s*(\d+|\$|/[^/]*/)?(\s*,\s*(\d+|\$|/[^/]*/))?\s*[pdqlnN=]?\s*$`)
	sedSubst  = regexp.MustCompile(`^\s*(\d+|\$|/[^/]*/)?(\s*,\s*(\d+|\$|/[^/]*/))?\s*s(.)(.*)$`)
	sedFlagOK = regexp.MustCompile(`^[gIpi0-9]*$`)
)

// classifySed allows only print/delete/substitute scripts without write or
// execute commands and without in-place editing.
// awkProgram returns the program text of an awk command line.
func awkProgram(args []string) string {
	var progs []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case in(a, "-e", "--source") && i+1 < len(args):
			progs = append(progs, args[i+1])
			i++
		case in(a, "-F", "-v", "--assign", "--field-separator"):
			i++
		case a == "--" && i+1 < len(args):
			return strings.Join(append(progs, args[i+1]), "\n")
		case strings.HasPrefix(a, "-") && a != "-":
		default:
			return strings.Join(append(progs, a), "\n")
		}
	}
	return strings.Join(progs, "\n")
}

var (
	awkString   = regexp.MustCompile(`"(\\.|[^"\\])*"`)
	awkPrintOut = regexp.MustCompile(`\bprintf?\b[^;{}\n]*(>|\|)`)
	awkRun      = regexp.MustCompile(`\bsystem\s*\(|\|\s*getline|\|&|@load|@include`)
)

// awkWrites reports whether an awk program can write files or run commands:
// print/printf with > or |, a command piped into getline, system(). A > in
// a pattern or condition ($3 > 80) is a comparison.
func awkWrites(prog string) bool {
	p := awkString.ReplaceAllString(prog, `""`)
	return awkPrintOut.MatchString(p) || awkRun.MatchString(p)
}

// classifyCloudCLI classifies gcloud / az / doctl / hcloud by their verb: the
// first word that is a known read or write verb after the command groups.
func classifyCloudCLI(cmd string, args []string) (string, []string) {
	pos := positional(args)
	for i, p := range pos {
		secret := i > 0 && in(pos[i-1], "secret", "secrets", "versions")
		switch {
		case in(p, "delete", "destroy", "remove", "rm", "purge", "terminate"):
			return RiskDestructive, []string{cmd + " " + p}
		case (in(p, "show", "get", "access") || strings.HasPrefix(p, "get-")) && (secret || secretWord.MatchString(p)):
			return RiskChange, []string{cmd + " " + p + " reveals a secret"}
		case in(p, "list", "describe", "show", "get", "info", "version", "status") || strings.HasPrefix(p, "list-") ||
			strings.HasPrefix(p, "describe-") || strings.HasPrefix(p, "show-") || strings.HasPrefix(p, "get-"):
			return RiskRead, nil
		case in(p, "create", "update", "set", "add", "start", "stop", "restart", "reset", "deploy", "ssh", "scp", "import", "export",
			"move", "resize", "attach", "detach", "enable", "disable", "apply", "run", "exec", "login", "logout", "rotate", "invoke",
			"access", "print-access-token", "print-identity-token", "upload", "download", "cp", "sync", "mv", "tag", "untag"):
			return RiskChange, []string{cmd + " " + p}
		}
	}
	return RiskChange, []string{cmd + " " + strings.Join(pos, " ")}
}

var secretWord = regexp.MustCompile(`token|secret|credential|password|access-key|keys?$`)

func classifySed(args []string) (string, []string) {
	if has(args, "-i", "--in-place") || has(args, "-f", "--file") {
		return RiskChange, []string{"sed edits files or runs a script file"}
	}
	var scripts []string
	for i, a := range args[1:] {
		if (a == "-e" || a == "--expression") && i+2 < len(args) {
			scripts = append(scripts, args[i+2])
		}
	}
	if len(scripts) == 0 {
		scripts = append(scripts, sub(args, 1))
	}
	for _, script := range scripts {
		for _, part := range strings.Split(script, ";") {
			if sedPrint.MatchString(part) {
				continue
			}
			if m := sedSubst.FindStringSubmatch(part); m != nil {
				delim := m[4]
				pieces := strings.Split(m[5], delim)
				if len(pieces) == 3 && sedFlagOK.MatchString(pieces[2]) {
					continue
				}
			}
			return RiskChange, []string{"sed script may write files or run commands"}
		}
	}
	return RiskRead, nil
}

func classifyGit(args []string) (string, []string) {
	// skip global options before the subcommand
	i := 1
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if in(args[i], "-C", "-c", "--git-dir", "--work-tree", "--namespace") {
			if args[i] == "-c" {
				return RiskChange, []string{"git -c overrides configuration"}
			}
			i++
		}
		i++
	}
	if i >= len(args) {
		return RiskRead, nil
	}
	rest := append([]string{"git"}, args[i:]...)
	s1, s2 := args[i], sub(rest, 2)
	switch s1 {
	case "status", "log", "show", "rev-parse", "ls-files", "describe", "blame", "shortlog", "ls-remote", "reflog", "grep", "cat-file", "ls-tree", "rev-list", "for-each-ref", "whatchanged":
		if s1 == "reflog" && in(s2, "expire", "delete") {
			return RiskChange, []string{"git reflog " + s2}
		}
		return RiskRead, nil
	case "diff":
		if has(rest, "--output") {
			return RiskChange, []string{"git diff --output writes a file"}
		}
		return RiskRead, nil
	case "branch":
		if len(positional(rest)) <= 1 && !has(rest, "-d", "-D", "--delete", "-m", "-M", "-c", "-C", "--set-upstream-to", "-u", "--unset-upstream", "-f", "--force", "--edit-description") {
			return RiskRead, nil
		}
	case "tag":
		if len(positional(rest)) <= 1 && (len(rest) == 2 || has(rest, "-l", "--list", "-n")) && !has(rest, "-d", "--delete", "-f", "-a", "-s") {
			return RiskRead, nil
		}
	case "remote":
		if s2 == "" || in(s2, "-v", "show", "get-url") {
			return RiskRead, nil
		}
	case "config":
		if has(rest, "--get", "--get-all", "--get-regexp", "-l", "--list") {
			return RiskRead, nil
		}
	case "push":
		if has(rest, "-f", "--force", "--force-with-lease", "--delete", "--mirror", "--prune") {
			return RiskDestructive, []string{"force push / remote delete"}
		}
	case "reset":
		if has(rest, "--hard") {
			return RiskDestructive, []string{"discards local changes"}
		}
	case "clean":
		if has(rest, "-f", "--force") {
			return RiskDestructive, []string{"deletes untracked files"}
		}
	}
	return RiskChange, []string{"git " + s1}
}

func classifySQL(cmd string, args []string) (string, []string) {
	var stmt string
	var ok bool
	switch cmd {
	case "psql":
		stmt, ok = flagValue(args, "-c", "--command")
	case "mysql", "mariadb", "clickhouse-client":
		stmt, ok = flagValue(args, "-e", "--execute")
		if !ok && cmd == "clickhouse-client" {
			stmt, ok = flagValue(args, "-q", "--query")
		}
	case "mongosh", "mongo":
		stmt, ok = flagValue(args, "", "--eval")
	case "sqlite3":
		if p := positional(args); len(p) >= 2 {
			stmt, ok = p[1], true
		}
	}
	if !ok {
		return RiskChange, []string{cmd + " session or script"}
	}
	s := strings.ToLower(strings.TrimSpace(stmt))
	for _, bad := range []string{`\!`, "program", "system", "outfile", "dumpfile", "copy ", "pg_read", "pg_write", "lo_", "load_file", "\\o", "\\w", "\\copy", "attach", ".shell", ".system", "dblink", "into "} {
		if strings.Contains(s, bad) {
			return RiskChange, []string{"SQL statement can write files or run commands"}
		}
	}
	// a single statement starting with a read-only keyword
	if body := strings.TrimRight(s, "; \n"); strings.Contains(body, ";") {
		return RiskChange, []string{"multiple SQL statements"}
	}
	for _, kw := range []string{"select", "show", "explain", "describe", "desc ", `\d`, `\l`, `\dt`, `\du`, `\conninfo`, "db.stats", "db.getcollectionnames", "rs.status", ".tables", ".schema"} {
		if strings.HasPrefix(s, kw) {
			if strings.HasPrefix(s, "explain") && strings.Contains(s, "analyze") && !strings.Contains(s, "select") {
				return RiskChange, []string{"EXPLAIN ANALYZE executes the statement"}
			}
			return RiskRead, nil
		}
	}
	return RiskChange, []string{"SQL statement modifies data"}
}

func classifyRedis(args []string) (string, []string) {
	valueFlags := []string{"-h", "-p", "-a", "-n", "-u", "-s", "--user", "--pass", "-r", "-i", "--tls-cert", "--cacert"}
	var pos []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if in(a, valueFlags...) {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			if in(a, "--stat", "--bigkeys", "--memkeys", "--latency", "--latency-history", "--scan", "--no-auth-warning", "--raw", "--no-raw", "--tls", "-c", "-e") {
				continue
			}
			return RiskChange, []string{"redis-cli option " + a}
		}
		pos = append(pos, strings.ToUpper(a))
	}
	if len(pos) == 0 {
		if has(args, "--stat", "--bigkeys", "--memkeys", "--latency", "--latency-history", "--scan") {
			return RiskRead, nil
		}
		return RiskChange, []string{"interactive redis-cli session"}
	}
	c := pos[0]
	if len(pos) > 1 {
		c2 := c + " " + pos[1]
		if in(c2, "CLIENT LIST", "CONFIG GET", "SLOWLOG GET", "SLOWLOG LEN", "MEMORY USAGE", "MEMORY STATS", "CLUSTER INFO", "CLUSTER NODES", "XINFO STREAM", "XINFO GROUPS", "ACL WHOAMI", "ACL LIST") {
			return RiskRead, nil
		}
	}
	if in(c, "INFO", "PING", "GET", "MGET", "KEYS", "SCAN", "TTL", "PTTL", "TYPE", "HGET", "HGETALL", "HKEYS", "HLEN", "LRANGE", "LLEN", "SMEMBERS", "SCARD", "ZRANGE", "ZCARD", "DBSIZE", "EXISTS", "STRLEN", "TIME", "ROLE", "LASTSAVE", "XLEN", "XRANGE") {
		return RiskRead, nil
	}
	if in(c, "FLUSHALL", "FLUSHDB", "SHUTDOWN", "DEBUG") {
		return RiskDestructive, []string{"redis " + c}
	}
	return RiskChange, []string{"redis " + c}
}

// skipGlobal returns the positional args of a CLI after its global options.
// Options that take a value are skipped with their value; an unknown global
// option makes the command unclassifiable (ok=false).
func skipGlobal(args []string, valueFlags, boolFlags []string) (pos []string, ok bool) {
	i := 1
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		a := args[i]
		name, _, hasEq := strings.Cut(a, "=")
		switch {
		case in(name, valueFlags...):
			if !hasEq {
				i++
			}
		case in(name, boolFlags...):
		default:
			return nil, false
		}
		i++
	}
	for _, a := range args[i:] {
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
	}
	return pos, true
}

func classifyDocker(args []string) (string, []string) {
	pos, ok := skipGlobal(args,
		[]string{"-H", "--host", "-l", "--log-level", "--config", "-c", "--context", "--tlscacert", "--tlscert", "--tlskey", "--url", "--identity", "--connection"},
		[]string{"-D", "--debug", "--tls", "--tlsverify"})
	if !ok {
		return RiskChange, []string{"docker with unrecognised global options"}
	}
	if len(pos) == 0 {
		return RiskRead, nil
	}
	s1 := pos[0]
	s2 := ""
	if len(pos) > 1 {
		s2 = pos[1]
	}
	switch s1 {
	case "ps", "logs", "inspect", "images", "stats", "version", "info", "top", "port", "diff", "history", "events", "search":
		return RiskRead, nil
	case "container", "image", "network", "volume", "node", "service", "stack", "secret", "config", "plugin", "context", "buildx", "manifest":
		if in(s2, "ls", "list", "inspect", "ps", "logs", "history", "top", "port", "diff", "stats") {
			return RiskRead, nil
		}
		if in(s2, "rm", "prune", "remove") {
			return RiskDestructive, []string{"docker " + s1 + " " + s2 + " removes data"}
		}
		return RiskChange, []string{"docker " + s1 + " " + s2}
	case "system":
		if in(s2, "df", "info", "events") {
			return RiskRead, nil
		}
		return RiskDestructive, []string{"docker system " + s2}
	case "compose":
		cpos, ok := skipGlobal(append([]string{"compose"}, args[indexOf(args, "compose")+1:]...),
			[]string{"-f", "--file", "-p", "--project-name", "--env-file", "--profile", "--project-directory", "--ansi", "--progress", "--parallel"},
			[]string{"--compatibility", "--dry-run", "--all-resources"})
		if !ok || len(cpos) == 0 {
			return RiskChange, []string{"docker compose"}
		}
		c := cpos[0]
		if in(c, "ps", "logs", "config", "ls", "images", "top", "version", "port", "events") {
			return RiskRead, nil
		}
		if c == "down" && has(args, "-v", "--volumes", "--rmi") {
			return RiskDestructive, []string{"compose down removes volumes/images"}
		}
		if c == "rm" {
			return RiskDestructive, []string{"compose rm removes containers"}
		}
		return RiskChange, []string{"docker compose " + c}
	case "rm", "rmi", "prune":
		return RiskDestructive, []string{"docker " + s1 + " removes containers/images"}
	}
	return RiskChange, []string{"docker " + s1}
}

// dockerExecCommand returns the command of docker exec / docker container
// exec / docker compose exec, which is classified like any other command.
func dockerExecCommand(args []string) ([]string, bool) {
	i := 1
	for i < len(args) && strings.HasPrefix(args[i], "-") { // global options
		if name, _, eq := strings.Cut(args[i], "="); !eq && in(name, "-H", "--host", "-l", "--log-level", "--config", "-c", "--context") {
			i++
		}
		i++
	}
	var valueOpts []string
	switch {
	case i < len(args) && args[i] == "exec":
		valueOpts = []string{"-u", "--user", "-w", "--workdir", "-e", "--env", "--env-file", "--detach-keys"}
		i++
	case i+1 < len(args) && args[i] == "container" && args[i+1] == "exec":
		valueOpts = []string{"-u", "--user", "-w", "--workdir", "-e", "--env", "--env-file", "--detach-keys"}
		i += 2
	case i < len(args) && args[i] == "compose":
		i++
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			if name, _, eq := strings.Cut(args[i], "="); !eq && in(name, "-f", "--file", "-p", "--project-name", "--env-file", "--profile", "--project-directory", "--ansi", "--progress", "--parallel") {
				i++
			}
			i++
		}
		if i >= len(args) || args[i] != "exec" {
			return nil, false
		}
		valueOpts = []string{"-u", "--user", "-w", "--workdir", "-e", "--env", "--index"}
		i++
	default:
		return nil, false
	}
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if name, _, eq := strings.Cut(args[i], "="); !eq && in(name, valueOpts...) {
			i++
		}
		i++
	}
	if i+1 >= len(args) { // container (or service) and a command
		return nil, false
	}
	return args[i+1:], true
}

// kubectlExecCommand returns the command of kubectl exec ... -- command.
func kubectlExecCommand(args []string) ([]string, bool) {
	dash := slices.Index(args, "--")
	if dash < 0 || dash+1 >= len(args) || !slices.Contains(args[1:dash], "exec") {
		return nil, false
	}
	// the verb is the first word that is not an option or an option's value
	for i := 1; i < dash; i++ {
		a := args[i]
		if a == "exec" {
			return args[dash+1:], true
		}
		if strings.HasPrefix(a, "-") {
			if name, _, eq := strings.Cut(a, "="); !eq && in(name, "-n", "--namespace", "--context", "--kubeconfig", "--cluster", "--user", "-s", "--server", "--token", "--as", "--request-timeout") {
				i++
			}
			continue
		}
		return nil, false
	}
	return nil, false
}

func prefixReasons(p string, why []string) []string {
	out := make([]string, len(why))
	for i, w := range why {
		out[i] = p + w
	}
	return out
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return len(list) - 1
}

func classifyKubectl(args []string) (string, []string) {
	valueFlags := []string{"-n", "--namespace", "--context", "-o", "--output", "-l", "--selector", "-c", "--container",
		"--kubeconfig", "--cluster", "--user", "-s", "--server", "--token", "--as", "--as-group", "--cache-dir",
		"--request-timeout", "--field-selector", "--sort-by", "--tail", "--since", "--since-time", "--template", "-f", "--filename", "-L", "--label-columns", "--chunk-size", "-v"}
	var pos []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		name, _, hasEq := strings.Cut(a, "=")
		if in(name, valueFlags...) {
			if !hasEq {
				i++
			}
			continue
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return RiskRead, nil
	}
	verb := pos[0]
	s2 := ""
	if len(pos) > 1 {
		s2 = pos[1]
	}
	switch verb {
	case "get", "describe", "logs", "top", "explain", "version", "cluster-info", "api-resources", "api-versions", "events", "diff":
		return RiskRead, nil
	case "config":
		if in(s2, "view", "get-contexts", "current-context", "get-clusters", "get-users") {
			return RiskRead, nil
		}
		return RiskChange, []string{"kubectl config " + s2}
	case "auth":
		if in(s2, "can-i", "whoami") {
			return RiskRead, nil
		}
		return RiskChange, []string{"kubectl auth " + s2}
	case "rollout":
		if in(s2, "status", "history") {
			return RiskRead, nil
		}
		return RiskChange, []string{"rollout " + s2}
	case "delete", "drain":
		return RiskDestructive, []string{"kubectl " + verb}
	case "replace":
		if has(args, "--force") {
			return RiskDestructive, []string{"kubectl replace --force"}
		}
	}
	return RiskChange, []string{"kubectl " + verb}
}
