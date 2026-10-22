package gitx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Change is one changed file of the working copy.
type Change struct {
	Path   string `json:"path"`
	Orig   string `json:"orig,omitempty"`   // renamed from
	Status string `json:"status"`           // modified | added | deleted | renamed | untracked | conflict | typechange
	Staged bool   `json:"staged,omitempty"` // the change is in the index
}

// Status is the state of a working copy.
type Status struct {
	Cloned   bool     `json:"cloned"`
	Branch   string   `json:"branch"` // "" when detached
	Head     string   `json:"head"`   // commit
	Upstream string   `json:"upstream,omitempty"`
	Ahead    int      `json:"ahead"`
	Behind   int      `json:"behind"`
	Default  string   `json:"default,omitempty"` // remote's default branch
	Changes  []Change `json:"changes"`
	Busy     string   `json:"busy,omitempty"` // clone | pull | push | … running, or a merge | rebase left in progress
}

func (r *Repo) needClone() error {
	if !r.Cloned() {
		return fmt.Errorf("repository %s is not cloned yet (clone it first)", r.R.Name)
	}
	return nil
}

func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	out, _, err := run(ctx, localEnv(), r.Dir, nil, 0, args...)
	return out, err
}

// Clone makes the working copy (branch: "" = the configured branch, else
// the remote's default).
func (r *Repo) Clone(ctx context.Context, branch string) (string, error) {
	defer r.m.lock(r.key(), "clone")()
	if r.Cloned() {
		return "", fmt.Errorf("repository %s is already cloned", r.R.Name)
	}
	if entries, err := os.ReadDir(r.Dir); err == nil && len(entries) > 0 {
		return "", fmt.Errorf("%s exists and is not a Git working copy", r.Dir)
	}
	if err := os.MkdirAll(filepath.Dir(r.Dir), 0o700); err != nil {
		return "", err
	}
	r.W.IgnoreRepos()
	e, err := r.netEnv(ctx)
	if err != nil {
		return "", err
	}
	defer e.close()
	if branch == "" {
		branch = r.R.Str("branch")
	}
	args := []string{"clone", "--origin", "origin"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", r.Remote.URL, r.Dir)
	if _, _, err := run(ctx, e, filepath.Dir(r.Dir), nil, 0, args...); err != nil {
		os.RemoveAll(r.Dir)
		return "", err
	}
	return r.headLine(ctx), nil
}

// Remove deletes the working copy.
func (r *Repo) Remove() error {
	defer r.m.lock(r.key(), "remove")()
	return os.RemoveAll(r.Dir)
}

func (r *Repo) headLine(ctx context.Context) string {
	out, err := r.git(ctx, "log", "-1", "--format=%h %s (%an, %cr)")
	if err != nil {
		return "empty repository"
	}
	b, _ := r.git(ctx, "branch", "--show-current")
	return "on " + firstStr(strings.TrimSpace(b), "detached HEAD") + ": " + strings.TrimSpace(out)
}

// Status reads branch, upstream and changed files.
func (r *Repo) Status(ctx context.Context) (*Status, error) {
	st := &Status{Changes: []Change{}}
	op := r.m.running(r.key())
	if !r.Cloned() || op == "clone" {
		st.Busy = op
		return st, nil
	}
	st.Cloned = true
	// Read-only: no lock, and git must not refresh the index meanwhile.
	e := localEnv()
	e.vars = append(e.vars, "GIT_OPTIONAL_LOCKS=0")
	out, _, err := run(ctx, e, r.Dir, nil, 0, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	st.Busy = op
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if rec == "" {
			continue
		}
		switch rec[0] {
		case '#':
			f := strings.Fields(rec)
			if len(f) < 3 {
				continue
			}
			switch f[1] {
			case "branch.oid":
				if f[2] != "(initial)" {
					st.Head = f[2]
				}
			case "branch.head":
				if f[2] != "(detached)" {
					st.Branch = f[2]
				}
			case "branch.upstream":
				st.Upstream = f[2]
			case "branch.ab":
				if len(f) >= 4 {
					st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[2], "+"))
					st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[3], "-"))
				}
			}
		case '1', '2':
			n := 9
			if rec[0] == '2' {
				n = 10
			}
			f := strings.SplitN(rec, " ", n)
			if len(f) < n {
				continue
			}
			c := Change{Path: f[n-1], Status: xyStatus(f[1])}
			c.Staged = f[1][0] != '.'
			if rec[0] == '2' && i+1 < len(recs) {
				c.Orig = recs[i+1]
				c.Status = "renamed"
				i++
			}
			st.Changes = append(st.Changes, c)
		case 'u':
			f := strings.SplitN(rec, " ", 11)
			if len(f) == 11 {
				st.Changes = append(st.Changes, Change{Path: f[10], Status: "conflict"})
			}
		case '?':
			st.Changes = append(st.Changes, Change{Path: rec[2:], Status: "untracked"})
		}
	}
	gitDir := filepath.Join(r.Dir, ".git")
	if _, err := os.Stat(filepath.Join(gitDir, "MERGE_HEAD")); err == nil {
		st.Busy = "merge"
	}
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, d)); err == nil {
			st.Busy = "rebase"
		}
	}
	st.Default = r.defaultBranch(ctx)
	return st, nil
}

func xyStatus(xy string) string {
	if len(xy) != 2 {
		return "modified"
	}
	c := xy[1]
	if c == '.' {
		c = xy[0]
	}
	switch c {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R', 'C':
		return "renamed"
	case 'T':
		return "typechange"
	case 'U':
		return "conflict"
	}
	return "modified"
}

// defaultBranch is the remote's default branch as seen at the last fetch,
// else the configured branch.
func (r *Repo) defaultBranch(ctx context.Context) string {
	if out, err := r.git(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(out), "origin/")
	}
	return r.R.Str("branch")
}

// DefaultBranch is the branch merge requests target by default.
func (r *Repo) DefaultBranch(ctx context.Context) string {
	if r.Cloned() {
		if b := r.defaultBranch(ctx); b != "" {
			return b
		}
	}
	return firstStr(r.R.Str("branch"), "main")
}

// Fetch updates remote-tracking branches.
func (r *Repo) Fetch(ctx context.Context) (string, error) {
	if err := r.needClone(); err != nil {
		return "", err
	}
	defer r.m.lock(r.key(), "fetch")()
	return r.fetch(ctx)
}

func (r *Repo) fetch(ctx context.Context) (string, error) {
	e, err := r.netEnv(ctx)
	if err != nil {
		return "", err
	}
	defer e.close()
	out, _, err := run(ctx, e, r.Dir, nil, 0, "fetch", "--prune", "origin")
	if err != nil {
		return "", err
	}
	// Keep origin/HEAD in step with the server's default branch.
	run(ctx, e, r.Dir, nil, 0, "remote", "set-head", "origin", "--auto")
	return out, nil
}

// Pull fetches and integrates the upstream branch: fast-forward only, or a
// rebase of local commits (with uncommitted changes stashed meanwhile).
func (r *Repo) Pull(ctx context.Context, rebase bool) (string, error) {
	if err := r.needClone(); err != nil {
		return "", err
	}
	defer r.m.lock(r.key(), "pull")()
	if _, err := r.fetch(ctx); err != nil {
		return "", err
	}
	cur := strings.TrimSpace(must(r.git(ctx, "branch", "--show-current")))
	if cur == "" {
		return "", errors.New("HEAD is detached: check out a branch first")
	}
	if _, err := r.git(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err != nil {
		if _, err := r.git(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+cur); err != nil {
			return "fetched; branch " + cur + " does not exist on the remote yet (push it)", nil
		}
		if _, err := r.git(ctx, "branch", "--set-upstream-to=origin/"+cur); err != nil {
			return "", err
		}
	}
	before := strings.TrimSpace(must(r.git(ctx, "rev-parse", "--short", "HEAD")))
	if rebase {
		if _, err := r.git(ctx, "-c", "user.name=opsdeck", "-c", "user.email=opsdeck@localhost", "rebase", "--autostash", "@{u}"); err != nil {
			r.git(ctx, "rebase", "--abort")
			return "", fmt.Errorf("%w\nthe rebase was aborted, nothing changed: resolve by hand or merge instead", err)
		}
	} else if _, err := r.git(ctx, "merge", "--ff-only", "@{u}"); err != nil {
		if strings.Contains(err.Error(), "Not possible to fast-forward") || strings.Contains(err.Error(), "fast-forward") {
			return "", fmt.Errorf("local and remote branch %s have diverged: pull with rebase to put your commits on top", cur)
		}
		return "", err
	}
	after := strings.TrimSpace(must(r.git(ctx, "rev-parse", "--short", "HEAD")))
	if before == after {
		return "already up to date — " + r.headLine(ctx), nil
	}
	stat, _ := r.git(ctx, "diff", "--stat", before, after)
	return fmt.Sprintf("updated %s..%s — %s\n%s", before, after, r.headLine(ctx), strings.TrimRight(stat, "\n")), nil
}

func must(s string, _ error) string { return s }

// Branch is a local or remote branch.
type Branch struct {
	Name     string    `json:"name"`
	Remote   bool      `json:"remote,omitempty"`
	Current  bool      `json:"current,omitempty"`
	Commit   string    `json:"commit"`
	Upstream string    `json:"upstream,omitempty"`
	Track    string    `json:"track,omitempty"` // e.g. "ahead 1, behind 2"
	Subject  string    `json:"subject"`
	Date     time.Time `json:"date"`
}

func (r *Repo) Branches(ctx context.Context) ([]Branch, error) {
	if err := r.needClone(); err != nil {
		return nil, err
	}
	out, err := r.git(ctx, "for-each-ref", "--format=%(HEAD)%1f%(refname)%1f%(objectname:short)%1f%(upstream:short)%1f%(upstream:track,nobracket)%1f%(committerdate:iso-strict)%1f%(subject)",
		"refs/heads", "refs/remotes/origin")
	if err != nil {
		return nil, err
	}
	list := []Branch{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) < 7 {
			continue
		}
		b := Branch{Current: f[0] == "*", Commit: f[2], Upstream: f[3], Track: f[4], Subject: f[6]}
		b.Date, _ = time.Parse(time.RFC3339, f[5])
		switch {
		case strings.HasPrefix(f[1], "refs/heads/"):
			b.Name = strings.TrimPrefix(f[1], "refs/heads/")
		case f[1] == "refs/remotes/origin/HEAD":
			continue
		default:
			b.Name, b.Remote = strings.TrimPrefix(f[1], "refs/remotes/origin/"), true
		}
		list = append(list, b)
	}
	return list, nil
}

// validBranch refuses names git would read as options or odd refs.
func validBranch(b string) error {
	if b == "" || strings.HasPrefix(b, "-") || strings.ContainsAny(b, " ~^:?*[\\\x7f") || strings.Contains(b, "..") ||
		strings.HasSuffix(b, ".lock") || strings.HasSuffix(b, "/") || strings.Contains(b, "@{") {
		return fmt.Errorf("invalid branch name %q", b)
	}
	return nil
}

// Checkout switches to branch, creating it (from start, default HEAD) when
// create is set. An existing remote branch is checked out with tracking.
func (r *Repo) Checkout(ctx context.Context, branch string, create bool, start string) (string, error) {
	if err := r.needClone(); err != nil {
		return "", err
	}
	if err := validBranch(branch); err != nil {
		return "", err
	}
	defer r.m.lock(r.key(), "checkout")()
	args := []string{"switch"}
	if create {
		args = append(args, "--no-track", "-c", branch)
		if start != "" {
			if strings.HasPrefix(start, "-") {
				return "", fmt.Errorf("invalid start point %q", start)
			}
			args = append(args, start)
		}
	} else {
		args = append(args, branch)
	}
	if _, err := r.git(ctx, args...); err != nil {
		return "", err
	}
	return r.headLine(ctx), nil
}

// File is an entry of the repository's file list.
type Tree struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated,omitempty"`
}

// Files lists tracked and untracked (not ignored) files of the working copy,
// or the files of a commit/branch when ref is set.
func (r *Repo) Files(ctx context.Context, ref string, max int) (*Tree, error) {
	if err := r.needClone(); err != nil {
		return nil, err
	}
	if max <= 0 {
		max = 20000
	}
	var out string
	var err error
	if ref != "" {
		if strings.HasPrefix(ref, "-") {
			return nil, fmt.Errorf("invalid ref %q", ref)
		}
		out, err = r.git(ctx, "ls-tree", "-r", "-z", "--name-only", ref)
	} else {
		out, err = r.git(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	t := &Tree{Files: []string{}}
	for _, p := range strings.Split(out, "\x00") {
		if p == "" || seen[p] {
			continue
		}
		if ref == "" {
			if _, err := os.Lstat(filepath.Join(r.Dir, filepath.FromSlash(p))); err != nil {
				continue // deleted in the working copy
			}
		}
		seen[p] = true
		if len(t.Files) >= max {
			t.Truncated = true
			break
		}
		t.Files = append(t.Files, p)
	}
	sort.Strings(t.Files)
	return t, nil
}

// abs resolves a repository-relative path, refusing escapes and .git.
func (r *Repo) abs(rel string) (string, error) {
	rel = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rel)), "./")
	clean := path.Clean(rel)
	if rel == "" || clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("invalid path %q: use a path relative to the repository root", rel)
	}
	for _, seg := range strings.Split(clean, "/") {
		if strings.EqualFold(seg, ".git") {
			return "", errors.New("the .git directory is off limits")
		}
	}
	abs := filepath.Join(r.Dir, filepath.FromSlash(clean))
	root, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return "", err
	}
	for check := abs; ; {
		if resolved, err := filepath.EvalSymlinks(check); err == nil {
			if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
				return "", errors.New("path leaves the repository")
			}
			break
		}
		parent := filepath.Dir(check)
		if parent == check {
			break
		}
		check = parent
	}
	return abs, nil
}

// Content is a file read from the repository.
type Content struct {
	Path      string `json:"path"`
	Ref       string `json:"ref,omitempty"`
	Text      string `json:"text"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Read returns a file of the working copy, or of ref when set.
func (r *Repo) Read(ctx context.Context, rel, ref string, max int) (*Content, error) {
	if err := r.needClone(); err != nil {
		return nil, err
	}
	if max <= 0 {
		max = 1 << 20
	}
	abs, err := r.abs(rel)
	if err != nil {
		return nil, err
	}
	relp, _ := filepath.Rel(r.Dir, abs)
	c := &Content{Path: filepath.ToSlash(relp), Ref: ref}
	var data []byte
	if ref != "" {
		if strings.HasPrefix(ref, "-") {
			return nil, fmt.Errorf("invalid ref %q", ref)
		}
		size, err := r.git(ctx, "cat-file", "-s", ref+":"+c.Path)
		if err != nil {
			return nil, fmt.Errorf("%s does not exist at %s", c.Path, ref)
		}
		c.Size, _ = strconv.ParseInt(strings.TrimSpace(size), 10, 64)
		out, trunc, err := run(ctx, localEnv(), r.Dir, nil, max, "cat-file", "blob", ref+":"+c.Path)
		if err != nil {
			return nil, err
		}
		data, c.Truncated = []byte(out), trunc
	} else {
		info, err := os.Stat(abs)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%s does not exist", c.Path)
			}
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s is a directory", c.Path)
		}
		c.Size = info.Size()
		f, err := os.Open(abs)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data = make([]byte, min(int64(max), info.Size()))
		n, _ := f.Read(data)
		data = data[:n]
		c.Truncated = info.Size() > int64(max)
	}
	if isBinary(data) {
		c.Binary = true
		return c, nil
	}
	c.Text = string(data)
	return c, nil
}

func isBinary(b []byte) bool {
	head := b[:min(len(b), 8000)]
	if strings.IndexByte(string(head), 0) >= 0 {
		return true
	}
	return !utf8.Valid(trimPartialRune(head))
}

func trimPartialRune(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0; i++ {
		if utf8.Valid(b) {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// Write creates or replaces a file in the working copy.
func (r *Repo) Write(rel, content string) error {
	if err := r.needClone(); err != nil {
		return err
	}
	defer r.m.lock(r.key(), "write")()
	abs, err := r.abs(rel)
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Lstat(abs); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", rel)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", rel)
		}
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp := abs + ".opsdeck-tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	os.Chmod(tmp, mode)
	return os.Rename(tmp, abs)
}

// Delete removes a file from the working copy.
func (r *Repo) Delete(rel string) error {
	if err := r.needClone(); err != nil {
		return err
	}
	defer r.m.lock(r.key(), "delete")()
	abs, err := r.abs(rel)
	if err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("%s does not exist", rel)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory: delete its files", rel)
	}
	if err := os.Remove(abs); err != nil {
		return err
	}
	// Drop directories left empty.
	for d := filepath.Dir(abs); d != r.Dir && strings.HasPrefix(d, r.Dir); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Diff shows uncommitted changes (all, or of paths), new files included.
func (r *Repo) Diff(ctx context.Context, paths []string, max int) (string, bool, error) {
	if err := r.needClone(); err != nil {
		return "", false, err
	}
	if max <= 0 {
		max = 512 << 10
	}
	st, err := r.Status(ctx)
	if err != nil {
		return "", false, err
	}
	defer r.m.lock(r.key(), "diff")()
	var specs []string
	for _, p := range paths {
		abs, err := r.abs(p)
		if err != nil {
			return "", false, err
		}
		rel, _ := filepath.Rel(r.Dir, abs)
		specs = append(specs, filepath.ToSlash(rel))
	}
	base := "HEAD"
	if st.Head == "" {
		base = emptyTree
	}
	args := append([]string{"diff", "--no-color", "--no-ext-diff", base, "--"}, specs...)
	out, trunc, err := run(ctx, localEnv(), r.Dir, nil, max, args...)
	if err != nil {
		return "", false, err
	}
	var b strings.Builder
	b.WriteString(out)
	for _, c := range st.Changes {
		if c.Status != "untracked" || (len(specs) > 0 && !matchesAny(c.Path, specs)) {
			continue
		}
		if b.Len() >= max {
			trunc = true
			break
		}
		d, t, err := run(ctx, localEnv(), r.Dir, nil, max-b.Len(), "diff", "--no-color", "--no-index", "--", os.DevNull, c.Path)
		if err != nil && exitCode(err) != 1 {
			continue
		}
		b.WriteString(d)
		trunc = trunc || t
	}
	return b.String(), trunc, nil
}

func matchesAny(p string, specs []string) bool {
	for _, s := range specs {
		if p == s || strings.HasPrefix(p, strings.TrimSuffix(s, "/")+"/") {
			return true
		}
	}
	return false
}

// Commit is an entry of the history.
type Commit struct {
	Hash    string    `json:"hash"`
	Short   string    `json:"short"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	Date    time.Time `json:"date"`
	Subject string    `json:"subject"`
	Refs    string    `json:"refs,omitempty"`
}

// Log lists commits of ref (default HEAD), optionally touching path.
func (r *Repo) Log(ctx context.Context, ref, file string, n int) ([]Commit, error) {
	if err := r.needClone(); err != nil {
		return nil, err
	}
	if n <= 0 || n > 500 {
		n = 50
	}
	if strings.HasPrefix(ref, "-") {
		return nil, fmt.Errorf("invalid ref %q", ref)
	}
	args := []string{"log", "-n", strconv.Itoa(n), "--format=%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%s%x1f%D%x1e"}
	if ref != "" {
		args = append(args, ref)
	}
	args = append(args, "--")
	if file != "" {
		abs, err := r.abs(file)
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(r.Dir, abs)
		args = append(args, filepath.ToSlash(rel))
	}
	out, err := r.git(ctx, args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") {
			return []Commit{}, nil
		}
		return nil, err
	}
	list := []Commit{}
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x1f")
		if len(f) < 7 {
			continue
		}
		c := Commit{Hash: f[0], Short: f[1], Author: f[2], Email: f[3], Subject: f[5], Refs: f[6]}
		c.Date, _ = time.Parse(time.RFC3339, f[4])
		list = append(list, c)
	}
	return list, nil
}

// Show returns a commit with its patch.
func (r *Repo) Show(ctx context.Context, rev string, max int) (string, bool, error) {
	if err := r.needClone(); err != nil {
		return "", false, err
	}
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", false, fmt.Errorf("invalid commit %q", rev)
	}
	if max <= 0 {
		max = 512 << 10
	}
	return run(ctx, localEnv(), r.Dir, nil, max, "show", "--no-color", "--no-ext-diff", "--stat", "--patch", "--format=fuller", rev, "--")
}

// Grep searches the working copy (tracked and untracked files).
func (r *Repo) Grep(ctx context.Context, pattern string, regex, ignoreCase bool, max int) (string, bool, error) {
	if err := r.needClone(); err != nil {
		return "", false, err
	}
	if pattern == "" {
		return "", false, errors.New("pattern is required")
	}
	args := []string{"grep", "-n", "-I", "--no-color", "--untracked", "--max-count=50"}
	if !regex {
		args = append(args, "-F")
	} else {
		args = append(args, "-E")
	}
	if ignoreCase {
		args = append(args, "-i")
	}
	args = append(args, "-e", pattern, "--")
	if max <= 0 {
		max = 64 << 10
	}
	out, trunc, err := run(ctx, localEnv(), r.Dir, nil, max, args...)
	if err != nil && exitCode(err) == 1 {
		return "", false, nil // no match
	}
	return out, trunc, err
}

// CommitOptions describe a commit.
type CommitOptions struct {
	Message string
	Paths   []string // only these (default: every change)
	Session string   // agent session (added as a trailer)
	Name    string   // author (default: settings, then the token owner)
	Email   string
}

// identity picks the commit author.
func (r *Repo) identity(ctx context.Context, o CommitOptions) (string, string) {
	name, email := o.Name, o.Email
	if (name == "" || email == "") && r.m.Identity != nil {
		n, e := r.m.Identity()
		name, email = firstStr(name, n), firstStr(email, e)
	}
	if name == "" || email == "" {
		if u := r.tokenUser(ctx); u != nil {
			name, email = firstStr(name, u.Name), firstStr(email, u.Email)
		}
	}
	return firstStr(name, "opsdeck"), firstStr(email, "opsdeck@localhost")
}

func (r *Repo) tokenUser(ctx context.Context) *User {
	if !r.HasToken() {
		return nil
	}
	r.m.mu.Lock()
	u, ok := r.m.users[r.key()]
	r.m.mu.Unlock()
	if ok {
		return u
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := r.Provider(ctx)
	if err != nil {
		return nil
	}
	u, err = p.User(ctx)
	if err != nil {
		return nil
	}
	r.m.mu.Lock()
	r.m.users[r.key()] = u
	r.m.mu.Unlock()
	return u
}

// Commit stages and commits changes. Returns a summary.
func (r *Repo) Commit(ctx context.Context, o CommitOptions) (string, error) {
	if err := r.needClone(); err != nil {
		return "", err
	}
	msg := strings.TrimSpace(o.Message)
	if msg == "" {
		return "", errors.New("a commit message is required")
	}
	if o.Session != "" {
		msg += "\n\nOpsdeck-Agent-Session: " + o.Session
	}
	name, email := r.identity(ctx, o)
	defer r.m.lock(r.key(), "commit")()
	var specs []string
	for _, p := range o.Paths {
		abs, err := r.abs(p)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(r.Dir, abs)
		specs = append(specs, filepath.ToSlash(rel))
	}
	if _, err := r.git(ctx, append([]string{"add", "-A", "--"}, specs...)...); err != nil {
		return "", err
	}
	if _, err := r.git(ctx, append([]string{"diff", "--cached", "--quiet", "--"}, specs...)...); err == nil {
		return "", errors.New("nothing to commit")
	}
	e := localEnv()
	e.vars = append(e.vars, "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email)
	args := []string{"commit", "-q", "--no-verify", "-F", "-"}
	if len(specs) > 0 {
		args = append(args, "--")
		args = append(args, specs...)
	}
	if _, _, err := run(ctx, e, r.Dir, []byte(msg+"\n"), 0, args...); err != nil {
		return "", err
	}
	stat, _ := r.git(ctx, "show", "--stat", "--format=%h %s", "HEAD")
	return "committed " + strings.TrimRight(stat, "\n"), nil
}

// PushOptions describe a push.
type PushOptions struct {
	Branch string // local branch (default: current); pushed to the same name
	Force  bool   // --force-with-lease
}

// CurrentBranch is the checked-out branch ("" when detached).
func (r *Repo) CurrentBranch(ctx context.Context) string {
	if !r.Cloned() {
		return ""
	}
	return strings.TrimSpace(must(r.git(ctx, "branch", "--show-current")))
}

// Push sends a branch to origin and sets it as upstream.
func (r *Repo) Push(ctx context.Context, o PushOptions) (string, error) {
	if err := r.needClone(); err != nil {
		return "", err
	}
	b := o.Branch
	if b == "" {
		b = r.CurrentBranch(ctx)
	}
	if b == "" {
		return "", errors.New("HEAD is detached: check out a branch to push")
	}
	if err := validBranch(b); err != nil {
		return "", err
	}
	defer r.m.lock(r.key(), "push")()
	e, err := r.netEnv(ctx)
	if err != nil {
		return "", err
	}
	defer e.close()
	args := []string{"push", "--porcelain", "-u"}
	if o.Force {
		args = append(args, "--force-with-lease")
	}
	args = append(args, "origin", "refs/heads/"+b+":refs/heads/"+b)
	out, _, err := run(ctx, e, r.Dir, nil, 0, args...)
	if err != nil {
		return "", err
	}
	res := []string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "refs/heads/") {
			f := strings.Split(line, "\t")
			if len(f) >= 3 {
				res = append(res, strings.TrimSpace(f[2]))
			}
		}
	}
	summary := "pushed " + b + " to origin"
	if len(res) > 0 {
		summary += " (" + strings.Join(res, ", ") + ")"
	}
	return summary, nil
}

// Discard throws away uncommitted changes of paths (all when empty).
func (r *Repo) Discard(ctx context.Context, paths []string) (string, error) {
	if err := r.needClone(); err != nil {
		return "", err
	}
	st, err := r.Status(ctx)
	if err != nil {
		return "", err
	}
	defer r.m.lock(r.key(), "discard")()
	if len(paths) == 0 {
		if st.Head != "" {
			if _, err := r.git(ctx, "reset", "-q", "--hard", "HEAD"); err != nil {
				return "", err
			}
		}
		if _, err := r.git(ctx, "clean", "-fdq"); err != nil {
			return "", err
		}
		return fmt.Sprintf("discarded %d changes", len(st.Changes)), nil
	}
	n := 0
	for _, p := range paths {
		abs, err := r.abs(p)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(r.Dir, abs)
		rel = filepath.ToSlash(rel)
		for _, c := range st.Changes {
			if c.Path != rel && !strings.HasPrefix(c.Path, rel+"/") {
				continue
			}
			n++
			switch {
			case c.Status == "untracked":
				os.Remove(filepath.Join(r.Dir, filepath.FromSlash(c.Path)))
			case c.Status == "added" || st.Head == "":
				r.git(ctx, "rm", "-q", "--cached", "--force", "--", c.Path)
				os.Remove(filepath.Join(r.Dir, filepath.FromSlash(c.Path)))
			default:
				specs := []string{c.Path}
				if c.Orig != "" {
					specs = append(specs, c.Orig)
				}
				if _, err := r.git(ctx, append([]string{"restore", "--source=HEAD", "--staged", "--worktree", "--"}, specs...)...); err != nil {
					return "", err
				}
				if c.Orig != "" {
					os.Remove(filepath.Join(r.Dir, filepath.FromSlash(c.Path)))
				}
			}
		}
	}
	if n == 0 {
		return "", errors.New("no uncommitted changes at these paths")
	}
	return fmt.Sprintf("discarded %d changes", n), nil
}

// Check verifies that the server answers with these credentials (without a
// working copy) and returns the remote's default branch.
func (r *Repo) Check(ctx context.Context) (string, error) {
	e, err := r.netEnv(ctx)
	if err != nil {
		return "", err
	}
	defer e.close()
	dir := r.Dir
	if !r.Cloned() {
		dir = r.W.Path
	}
	out, _, err := run(ctx, e, dir, nil, 1<<20, "ls-remote", "--symref", "--", r.Remote.URL, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			b, _, _ := strings.Cut(rest, "\t")
			return b, nil
		}
	}
	return "", nil
}

// ProtectedBranch tells whether pushing to branch needs extra care: the
// default branch, a branch listed on the resource, or one protected on the
// server.
func (r *Repo) ProtectedBranch(ctx context.Context, branch string) (bool, string) {
	if branch == r.DefaultBranch(ctx) {
		return true, "default branch"
	}
	for _, p := range r.R.List("protectedBranches") {
		if ok, _ := path.Match(p, branch); ok || p == branch {
			return true, "listed as protected on the repository"
		}
	}
	if r.HasToken() && r.Remote.Scheme != "file" {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if p, err := r.Provider(ctx); err == nil {
			if ok, err := p.Protected(ctx, branch); err == nil && ok {
				return true, "protected on " + p.Kind()
			}
		}
	}
	return false, ""
}

// Outgoing describes what a push of branch would send: its commits not on
// the remote branch (or, for a new branch, not on the default branch) and
// their diff stat.
func (r *Repo) Outgoing(ctx context.Context, branch string) (string, error) {
	if err := r.needClone(); err != nil {
		return "", err
	}
	if err := validBranch(branch); err != nil {
		return "", err
	}
	base := "refs/remotes/origin/" + branch
	note := ""
	if _, err := r.git(ctx, "rev-parse", "--verify", "--quiet", base); err != nil {
		base = "refs/remotes/origin/" + r.DefaultBranch(ctx)
		note = "new branch " + branch + " (compared with " + r.DefaultBranch(ctx) + ")\n"
		if _, err := r.git(ctx, "rev-parse", "--verify", "--quiet", base); err != nil {
			return note + "(no remote branch to compare with)", nil
		}
	}
	log, err := r.git(ctx, "log", "--format=%h %s (%an)", base+".."+"refs/heads/"+branch, "--")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(log) == "" {
		return note + "nothing to push: the remote branch already has every commit", nil
	}
	stat, _ := r.git(ctx, "diff", "--stat", base+"...refs/heads/"+branch, "--")
	diff, _, _ := run(ctx, localEnv(), r.Dir, nil, 16<<10, "diff", "--no-color", base+"...refs/heads/"+branch, "--")
	return note + "commits:\n" + log + "\n" + stat + "\n" + diff, nil
}
