package agent

import (
	"regexp"
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
// classifying every simple command in it (pipelines, &&, subshells, $(...),
// bash -c / eval strings and here-docs fed to interpreters). Read-only is an
// allowlist: anything not recognised as harmless is at least a change, and
// code the classifier cannot see (dynamic command names, scripts on stdin) is
// destructive so it always needs approval. It is a safety net for approvals,
// not a sandbox.
func ClassifyCommand(cmd string) (risk string, reasons []string) {
	return classifyScript(cmd, 0)
}

const maxDepth = 4

func classifyScript(cmd string, depth int) (risk string, reasons []string) {
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
	upper := strings.ToUpper(cmd)
	for _, pat := range sqlDestructive {
		if pat.MatchString(upper) {
			note(RiskDestructive, "destructive SQL/data statement")
		}
	}
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
						note(RiskChange, "writes to file "+target)
					}
				}
			}
		case *syntax.CallExpr:
			if len(x.Args) == 0 {
				return true // plain variable assignment
			}
			for _, a := range x.Assigns {
				if a.Name != nil && !safeEnv[a.Name.Value] {
					note(RiskChange, "runs with environment override "+a.Name.Value)
				}
			}
			var args []string
			dynamic := false
			for i, w := range x.Args {
				lit, ok := wordLiteral(w)
				if !ok {
					if i == 0 {
						note(RiskDestructive, "command name is computed at runtime")
						return true
					}
					dynamic = true
				}
				args = append(args, lit)
			}
			body, hasStdin := stdin[x]
			r, why := classifyArgs(args, body, hasStdin, depth)
			if dynamic && r == RiskRead && !readSafeWithDynamicArgs[baseName(stripWrappers(args)[0])] {
				r, why = RiskChange, []string{"command uses dynamic arguments"}
			}
			note(r, why...)
		}
		return true
	})
	return risk, dedupe(reasons)
}

var safeEnv = map[string]bool{"LANG": true, "LC_ALL": true, "LC_MESSAGES": true, "TZ": true, "TERM": true,
	"PAGER": true, "SYSTEMD_PAGER": true, "COLUMNS": true, "LINES": true, "NO_COLOR": true}

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

// Commands whose read-only nature does not depend on argument values.
var readSafeWithDynamicArgs = map[string]bool{
	"echo": true, "printf": true, "cat": true, "grep": true, "ls": true, "head": true, "tail": true,
	"wc": true, "test": true, "[": true, "stat": true, "du": true, "df": true,
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

func classifyArgs(rawArgs []string, stdinBody string, hasStdin bool, depth int) (string, []string) {
	args := stripWrappers(rawArgs)
	if len(args) == 0 {
		return RiskRead, nil
	}
	one := func(r, why string) (string, []string) { return r, []string{why} }
	cmd := baseName(args[0])
	s1, s2 := sub(args, 1), sub(args, 2)

	// Code passed to another interpreter is classified recursively.
	if interpreters[cmd] || cmd == "eval" || cmd == "source" || cmd == "." {
		if cmd == "eval" {
			return classifyScript(strings.Join(args[1:], " "), depth+1)
		}
		if c, ok := flagValue(args, "-c", ""); ok {
			return classifyScript(c, depth+1)
		}
		if len(positional(args)) == 0 && has(args, "--version") {
			return RiskRead, nil
		}
		if len(positional(args)) == 0 && hasStdin {
			return classifyScript(stdinBody, depth+1)
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
	case "dd", "mkfs", "fdisk", "sfdisk", "parted", "wipefs", "shred", "sgdisk", "mkswap", "pvremove", "vgremove", "lvremove", "zpool", "cryptsetup", "mkfs.ext4", "mkfs.xfs", "mkfs.btrfs", "mkfs.vfat":
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
		if has(args, "-l") && len(positional(args)) == 0 {
			return RiskRead, nil
		}
		return one(RiskChange, "edits the crontab")
	case "sed":
		return classifySed(args)
	case "awk", "gawk", "mawk":
		prog := sub(args, 1)
		if f, ok := flagValue(args, "-f", ""); ok && f != "" {
			return one(RiskChange, "awk program from file")
		}
		if strings.ContainsAny(prog, ">|") || strings.Contains(prog, "system") || strings.Contains(prog, "getline") || has(args, "-i", "--inplace") {
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
		for i, a := range args {
			if in(a, "-exec", "-execdir", "-ok", "-okdir") {
				var inner []string
				for _, b := range args[i+1:] {
					if b == ";" || b == "+" || b == `\;` {
						break
					}
					if b != "{}" {
						inner = append(inner, b)
					}
				}
				r, why := classifyArgs(inner, "", false, depth+1)
				return maxRisk(RiskChange, r), append([]string{"find runs a command per file"}, why...)
			}
		}
		if has(args, "-fprint", "-fprintf", "-fls", "-fprint0") {
			return one(RiskChange, "find writes files")
		}
		return RiskRead, nil
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
		if has(args, "-o", "--output", "-O", "--remote-name", "-K", "--config") {
			return one(RiskChange, "downloads to a file / reads config")
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
		if in(s1, "s_client", "x509", "version", "verify", "ciphers", "crl", "asn1parse") && !has(args, "-out", "-new") {
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
	case "docker", "podman", "nerdctl":
		return classifyDocker(args)
	case "docker-compose":
		return classifyDocker(append([]string{"docker", "compose"}, args[1:]...))
	case "kubectl", "oc", "k":
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
	case "apt", "apt-get", "yum", "dnf", "zypper", "apk", "pacman", "snap", "pip", "pip3", "npm", "gem":
		if in(s1, "list", "search", "show", "info", "policy", "history", "check-update", "outdated", "freeze") {
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
	if len(args) == 2 && in(args[1], "--version", "-v", "-V", "version", "--help", "-h") {
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
