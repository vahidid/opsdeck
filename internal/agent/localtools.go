package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"opsdeck/internal/llm"
)

// Tools for the computer opsdeck runs on. They are offered only when local
// access is enabled (Settings → Local access) and go through the same
// permission checks, redaction and audit as every other tool.

const (
	localReadMax  = 2 << 20 // bytes read from a file
	localShowMax  = 100000  // characters returned to the model
	localWriteMax = 5 << 20
)

// LocalTools returns the local machine tool set.
func LocalTools() []Tool {
	return []Tool{toolLocalReadFile(), toolLocalListDir(), toolLocalSearch(), toolLocalWriteFile(), toolLocalEditFile(), toolLocalExec(),
		toolVaultImportSSHKey(), toolKnownHostsImport()}
}

func (e *Env) local() (*LocalPolicy, error) {
	if e.Local == nil {
		return nil, errors.New("access to this computer is turned off (Settings → Local access)")
	}
	return e.Local, nil
}

func toolLocalReadFile() Tool {
	return Tool{
		Def: llm.Tool{Name: "local_read_file", Description: "Read a text file on THIS computer (the user's machine where opsdeck runs), e.g. ~/.ssh/config, ~/.ssh/known_hosts, a project's docker-compose.yml or a kubeconfig. Paths are absolute or start with ~/. Files outside the workspace and the allowed folders need the user's consent. Files containing private keys are never shown: import them with vault_import_ssh_key instead.",
			Schema: obj(map[string]any{
				"path":   str("Absolute path or ~/..."),
				"offset": num("First line to return, 1-based (default 1)"),
				"limit":  num("Number of lines (default 2000)"),
			}, "path")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Path          string
				Offset, Limit int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			lp, err := env.local()
			if err != nil {
				return nil, err
			}
			path, err := lp.Resolve(in.Path)
			if err != nil {
				return nil, err
			}
			c := &Call{Risk: RiskRead, Target: "local:" + path, Match: path, Preview: "read " + path}
			lp.readAccess(c, env, path, false)
			c.Run = func(ctx context.Context) (string, error) {
				st, err := os.Stat(path)
				if err != nil {
					return "", err
				}
				if st.IsDir() {
					return "", fmt.Errorf("%s is a directory: use local_list_dir", path)
				}
				b, err := readHead(path, localReadMax)
				if err != nil {
					return "", err
				}
				if holdsPrivateKey(b) {
					return "", fmt.Errorf("%s contains a private key, which is never shown to the model. To use it, import it into the vault with vault_import_ssh_key (path %q)", path, path)
				}
				if isBinary(b) {
					return "", fmt.Errorf("%s is a binary file (%d bytes); inspect it with local_exec (e.g. file, xxd | head) if needed", path, st.Size())
				}
				return numberLines(path, string(b), st.Size() > int64(len(b)), in.Offset, in.Limit), nil
			}
			return c, nil
		},
	}
}

func readHead(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

func numberLines(path, s string, truncated bool, offset, limit int) string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = 2000
	}
	var b strings.Builder
	end := offset - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	if offset-1 > len(lines) {
		return fmt.Sprintf("%s has %d lines", path, len(lines))
	}
	fmt.Fprintf(&b, "%s (lines %d-%d of %d", path, offset, end, len(lines))
	if truncated {
		fmt.Fprintf(&b, ", file truncated at %d bytes", localReadMax)
	}
	b.WriteString(")\n")
	for i := offset - 1; i < end; i++ {
		fmt.Fprintf(&b, "%6d\t%s", i+1, strings.TrimSuffix(lines[i], "\n")+"\n")
		if b.Len() > localShowMax {
			fmt.Fprintf(&b, "... [stopped at line %d: output limit; continue with offset=%d]\n", i+1, i+2)
			break
		}
	}
	return b.String()
}

func toolLocalListDir() Tool {
	return Tool{
		Def: llm.Tool{Name: "local_list_dir", Description: "List a folder on THIS computer (type, size, modification time, name). depth 1-3 lists sub-folders too. Hidden entries are included only with hidden=true.",
			Schema: obj(map[string]any{
				"path":   str("Absolute path or ~/... (default ~)"),
				"depth":  num("1 (default) to 3"),
				"hidden": map[string]any{"type": "boolean", "description": "Include dot-files"},
			})},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Path   string
				Depth  int
				Hidden bool
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			lp, err := env.local()
			if err != nil {
				return nil, err
			}
			if in.Path == "" {
				in.Path = "~"
			}
			path, err := lp.Resolve(in.Path)
			if err != nil {
				return nil, err
			}
			if in.Depth < 1 {
				in.Depth = 1
			}
			if in.Depth > 3 {
				in.Depth = 3
			}
			c := &Call{Risk: RiskRead, Target: "local:" + path, Match: path, Preview: "list " + path}
			lp.readAccess(c, env, path, true)
			c.Run = func(ctx context.Context) (string, error) {
				var b strings.Builder
				n := 0
				var walk func(dir, indent string, depth int) error
				walk = func(dir, indent string, depth int) error {
					entries, err := os.ReadDir(dir)
					if err != nil {
						return err
					}
					for _, e := range entries {
						if !in.Hidden && strings.HasPrefix(e.Name(), ".") {
							continue
						}
						if n++; n > 1000 {
							return errStop
						}
						full := filepath.Join(dir, e.Name())
						if lp.forbidden(full) {
							fmt.Fprintf(&b, "%s- %s (opsdeck internal, hidden)\n", indent, e.Name())
							continue
						}
						info, err := e.Info()
						if err != nil {
							continue
						}
						kind, size := "f", fmt.Sprint(info.Size())
						switch {
						case e.IsDir():
							kind, size = "d", "-"
						case info.Mode()&fs.ModeSymlink != 0:
							kind = "l"
							if t, err := os.Readlink(full); err == nil {
								size = "-> " + t
							}
						}
						fmt.Fprintf(&b, "%s%s %s %s %s\n", indent, kind, info.ModTime().Format("2006-01-02 15:04"), size, e.Name())
						if e.IsDir() && depth > 1 {
							if err := walk(full, indent+"  ", depth-1); errors.Is(err, errStop) {
								return err
							}
						}
					}
					return nil
				}
				fmt.Fprintf(&b, "%s:\n", path)
				err := walk(path, "", in.Depth)
				if errors.Is(err, errStop) {
					b.WriteString("... [stopped after 1000 entries]\n")
					err = nil
				}
				return b.String(), err
			}
			return c, nil
		},
	}
}

var errStop = errors.New("stop")

var searchSkipDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true,
	".cache": true, ".terraform": true, "vendor": true, ".idea": true, "dist": true, "target": true, ".next": true}

func toolLocalSearch() Tool {
	return Tool{
		Def: llm.Tool{Name: "local_search", Description: "Search text files under a folder on THIS computer for a regular expression (like grep -rn). Returns file:line: text. Skips .git, node_modules, virtualenvs, binaries, files over 5 MB and files holding private keys. Narrow with glob (e.g. *.yml).",
			Schema: obj(map[string]any{
				"path":        str("Folder (or file) to search, absolute or ~/..."),
				"pattern":     str("Regular expression (RE2 syntax)"),
				"glob":        str("Optional file name filter, e.g. *.conf or docker-compose*.yml"),
				"ignore_case": map[string]any{"type": "boolean"},
				"max_results": num("Default 200, max 1000"),
			}, "path", "pattern")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Path, Pattern, Glob string
				IgnoreCase          bool `json:"ignore_case"`
				MaxResults          int  `json:"max_results"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			lp, err := env.local()
			if err != nil {
				return nil, err
			}
			path, err := lp.Resolve(in.Path)
			if err != nil {
				return nil, err
			}
			expr := in.Pattern
			if in.IgnoreCase {
				expr = "(?i)" + expr
			}
			re, err := regexp.Compile(expr)
			if err != nil {
				return nil, fmt.Errorf("invalid pattern: %v", err)
			}
			if in.MaxResults <= 0 {
				in.MaxResults = 200
			}
			if in.MaxResults > 1000 {
				in.MaxResults = 1000
			}
			c := &Call{Risk: RiskRead, Target: "local:" + path, Match: path, Preview: "search " + path + " for /" + in.Pattern + "/"}
			st, err := os.Stat(path)
			lp.readAccess(c, env, path, err == nil && st.IsDir())
			c.Run = func(ctx context.Context) (string, error) {
				var b strings.Builder
				n, files := 0, 0
				err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						return nil
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if lp.forbidden(p) {
						if d.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
					if d.IsDir() {
						if p != path && (searchSkipDirs[d.Name()] || lp.Sensitive(p) && !lp.Sensitive(path)) {
							return filepath.SkipDir
						}
						return nil
					}
					if !d.Type().IsRegular() {
						return nil
					}
					if in.Glob != "" {
						if ok, _ := filepath.Match(in.Glob, d.Name()); !ok {
							return nil
						}
					}
					if lp.Sensitive(p) && !lp.Sensitive(path) {
						return nil // credential files are searched only when asked for explicitly
					}
					if info, err := d.Info(); err != nil || info.Size() > 5<<20 {
						return nil
					}
					data, err := os.ReadFile(p)
					if err != nil || isBinary(data) || holdsPrivateKey(data) {
						return nil
					}
					if files++; files > 50000 {
						return errStop
					}
					sc := bufio.NewScanner(bytes.NewReader(data))
					sc.Buffer(make([]byte, 64*1024), 1<<20)
					for line := 1; sc.Scan(); line++ {
						if re.Match(sc.Bytes()) {
							text := sc.Text()
							if r := []rune(text); len(r) > 300 {
								text = string(r[:300]) + "…"
							}
							fmt.Fprintf(&b, "%s:%d: %s\n", p, line, text)
							if n++; n >= in.MaxResults {
								return errStop
							}
						}
					}
					return nil
				})
				switch {
				case errors.Is(err, errStop):
					b.WriteString("... [stopped at the result limit]\n")
				case err != nil:
					return b.String(), err
				}
				if n == 0 {
					return "no matches", nil
				}
				return b.String(), nil
			}
			return c, nil
		},
	}
}

// localWrite writes content atomically, keeping the mode of an existing file.
func localWrite(path, content string) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".opsdeck-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		f.Close()
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readForEdit(path string) (string, bool, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if st.IsDir() {
		return "", true, fmt.Errorf("%s is a directory", path)
	}
	if st.Size() > localWriteMax {
		return "", true, fmt.Errorf("%s is larger than 5 MB", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", true, err
	}
	if isBinary(b) {
		return "", true, fmt.Errorf("%s is a binary file", path)
	}
	return string(b), true, nil
}

func toolLocalWriteFile() Tool {
	return Tool{
		Def: llm.Tool{Name: "local_write_file", Description: "Create or overwrite a text file on THIS computer (e.g. a generated docker-compose.yml, an ~/.ssh/config entry, a script in the user's project). Parent folders are created. Always shown to the user as a diff for approval. Prefer local_edit_file for small changes to existing files.",
			Schema: obj(map[string]any{"path": str("Absolute path or ~/..."), "content": str("Full file content")}, "path", "content")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Path, Content string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			lp, err := env.local()
			if err != nil {
				return nil, err
			}
			path, err := lp.Resolve(in.Path)
			if err != nil {
				return nil, err
			}
			if len(in.Content) > localWriteMax {
				return nil, errors.New("content is larger than 5 MB")
			}
			before, existed, err := readForEdit(path)
			if err != nil {
				return nil, err
			}
			if holdsPrivateKey([]byte(before)) {
				return nil, fmt.Errorf("%s holds a private key; the agent does not overwrite key files", path)
			}
			verb := "create"
			if existed {
				verb = "overwrite"
			}
			c := &Call{Risk: RiskChange, Target: "local:" + path, Match: path,
				Preview: verb + " " + path + " on this computer\n\n" + unifiedDiff(path, before, in.Content)}
			lp.writeAccess(c, path)
			c.Run = func(ctx context.Context) (string, error) {
				if err := localWrite(path, in.Content); err != nil {
					return "", err
				}
				return fmt.Sprintf("written %s (%d bytes)", path, len(in.Content)), nil
			}
			return c, nil
		},
	}
}

func toolLocalEditFile() Tool {
	return Tool{
		Def: llm.Tool{Name: "local_edit_file", Description: "Replace exact text in a file on THIS computer. old_string must match exactly once (include surrounding lines to make it unique) unless replace_all is true. Shown to the user as a diff for approval.",
			Schema: obj(map[string]any{
				"path":        str("Absolute path or ~/..."),
				"old_string":  str("Exact text to replace"),
				"new_string":  str("Replacement text"),
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence"},
			}, "path", "old_string", "new_string")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Path       string
				Old        string `json:"old_string"`
				New        string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			lp, err := env.local()
			if err != nil {
				return nil, err
			}
			path, err := lp.Resolve(in.Path)
			if err != nil {
				return nil, err
			}
			before, existed, err := readForEdit(path)
			if err != nil {
				return nil, err
			}
			if !existed {
				return nil, fmt.Errorf("%s does not exist (use local_write_file to create it)", path)
			}
			if holdsPrivateKey([]byte(before)) {
				return nil, fmt.Errorf("%s holds a private key; the agent does not edit key files", path)
			}
			if in.Old == "" {
				return nil, errors.New("old_string is empty")
			}
			n := strings.Count(before, in.Old)
			var after string
			switch {
			case n == 0:
				return nil, errors.New("old_string not found (it must match exactly, including whitespace)")
			case in.ReplaceAll:
				after = strings.ReplaceAll(before, in.Old, in.New)
			case n > 1:
				return nil, fmt.Errorf("old_string matches %d times; add surrounding lines or set replace_all", n)
			default:
				after = strings.Replace(before, in.Old, in.New, 1)
			}
			c := &Call{Risk: RiskChange, Target: "local:" + path, Match: path,
				Preview: "edit " + path + " on this computer\n\n" + unifiedDiff(path, before, after)}
			lp.writeAccess(c, path)
			c.Run = func(ctx context.Context) (string, error) {
				cur, _, err := readForEdit(path)
				if err != nil {
					return "", err
				}
				if cur != before {
					return "", errors.New("the file changed since the edit was prepared; read it again")
				}
				if err := localWrite(path, after); err != nil {
					return "", err
				}
				return "edited " + path, nil
			}
			return c, nil
		},
	}
}

func toolLocalExec() Tool {
	return Tool{
		Def: llm.Tool{Name: "local_exec", Description: "Run a non-interactive shell command on THIS computer (sh -c; cmd /C on Windows), e.g. ssh-keygen -lf, ssh -G <host>, git status in a local project, docker compose config, kubectl config get-contexts. Use it for local tooling, not for workspace hosts (use exec for those). stdin is empty; output is limited. In Ask mode every command waits for the user's approval; in Auto mode only destructive ones; in Full mode none.",
			Schema: obj(map[string]any{
				"command":     str("Shell command"),
				"cwd":         str("Working directory, absolute or ~/... (default: home)"),
				"timeout_sec": num("Timeout in seconds (default 120, max 1800)"),
			}, "command")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Command, Cwd string
				TimeoutSec   int `json:"timeout_sec"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			lp, err := env.local()
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Command) == "" {
				return nil, errors.New("command is empty")
			}
			if in.Cwd == "" {
				in.Cwd = "~"
			}
			cwd, err := lp.Resolve(in.Cwd)
			if err != nil {
				return nil, err
			}
			timeout := time.Duration(in.TimeoutSec) * time.Second
			if timeout <= 0 {
				timeout = 120 * time.Second
			}
			if timeout > 30*time.Minute {
				timeout = 30 * time.Minute
			}
			an := AnalyzeCommand(in.Command)
			risk := an.Risk
			c := &Call{Risk: risk, Target: "local", Environment: "local", Protected: true, Match: in.Command, Reasons: an.Reasons,
				Shell: true, Commands: an.Parts,
				Preview: "$ " + in.Command + "\n\non: this computer (" + runtime.GOOS + ")\nin: " + cwd}
			if risk == RiskRead && (!lp.AutoReadCommands || strings.Contains(strings.ToLower(in.Command), "opsdeck")) {
				c.Consent = "runs a command on this computer"
			}
			c.Run = func(ctx context.Context) (string, error) {
				ctx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				return runLocal(ctx, in.Command, cwd), nil
			}
			return c, nil
		},
	}
}

// capped keeps the first max bytes written to it.
type capped struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func runLocal(ctx context.Context, command, cwd string) string {
	cmd := shellCommand(ctx, command)
	cmd.Dir = cwd
	cmd.Env = localEnv()
	cmd.WaitDelay = 3 * time.Second
	stdout, stderr := &capped{max: 2 << 20}, &capped{max: 512 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	start := time.Now()
	err := cmd.Run()
	res := struct {
		code int
		err  error
	}{0, nil}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		res.code = exitErr.ExitCode()
	case err != nil:
		res.code, res.err = -1, err
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.err = errors.New("timed out")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "exit code: %d (%.1fs)\n", res.code, time.Since(start).Seconds())
	if res.err != nil {
		fmt.Fprintf(&b, "error: %v\n", res.err)
	}
	if stdout.buf.Len() > 0 {
		b.WriteString("stdout:\n" + truncate(stdout.buf.String(), 24000) + "\n")
	}
	if stderr.buf.Len() > 0 {
		b.WriteString("stderr:\n" + truncate(stderr.buf.String(), 8000) + "\n")
	}
	if stdout.truncated || stderr.truncated {
		b.WriteString("(output was truncated at the source)\n")
	}
	if stdout.buf.Len() == 0 && stderr.buf.Len() == 0 {
		b.WriteString("(no output)\n")
	}
	return b.String()
}

// localEnv is opsdeck's environment without its own secrets.
func localEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "OPSDECK_") || k == "CLAUDECODE" {
			continue
		}
		env = append(env, kv)
	}
	sort.Strings(env)
	return env
}
