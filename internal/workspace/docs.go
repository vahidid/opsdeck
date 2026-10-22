package workspace

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"opsdeck/internal/skills"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Doc describes a markdown file of the workspace.
type Doc struct {
	Path    string    `json:"path"`
	Title   string    `json:"title"`
	Size    int64     `json:"size"`
	Updated time.Time `json:"updated"`
}

// docPath validates a relative doc path: OPSDECK.md, docs/**.md or skills/*.md.
func (w *Workspace) docPath(rel string) (string, error) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") || strings.Contains(rel, "/../") {
		return "", errors.New("invalid path")
	}
	if strings.HasPrefix(rel, "repo/") {
		return w.repoDocPath(rel)
	}
	ok := rel == "OPSDECK.md" ||
		(strings.HasPrefix(rel, "docs/") && strings.HasSuffix(rel, ".md")) ||
		(strings.HasPrefix(rel, "reports/") && strings.HasSuffix(rel, ".md")) ||
		(strings.HasPrefix(rel, "skills/") && strings.HasSuffix(rel, ".md") && strings.Count(rel, "/") <= 3)
	if !ok {
		return "", errors.New("docs must be OPSDECK.md, docs/**/*.md, reports/**/*.md, skills/*.md or repo/<repository>/<path>.md")
	}
	return w.contained(filepath.Join(w.Path, filepath.FromSlash(rel)))
}

// contained refuses paths that leave the workspace, including through symlinks.
func (w *Workspace) contained(abs string) (string, error) {
	root, err := filepath.EvalSymlinks(w.Path)
	if err != nil {
		return "", err
	}
	check := abs
	for {
		if resolved, err := filepath.EvalSymlinks(check); err == nil {
			if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
				return "", errors.New("path leaves the workspace")
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

// Docs lists all documentation files.
func (w *Workspace) Docs() []Doc {
	out := []Doc{}
	add := func(abs string, info fs.FileInfo) {
		rel, _ := filepath.Rel(w.Path, abs)
		out = append(out, Doc{Path: filepath.ToSlash(rel), Title: mdTitle(abs), Size: info.Size(), Updated: info.ModTime()})
	}
	if info, err := os.Stat(filepath.Join(w.Path, "OPSDECK.md")); err == nil {
		add(filepath.Join(w.Path, "OPSDECK.md"), info)
	}
	for _, dir := range []string{"docs", "reports"} {
		filepath.WalkDir(filepath.Join(w.Path, dir), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
				if info, err := d.Info(); err == nil {
					add(p, info)
				}
			}
			return nil
		})
	}
	matches, _ := filepath.Glob(filepath.Join(w.Path, "skills", "*.md"))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil {
			add(m, info)
		}
	}
	out = append(out, w.repoDocs()...)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Path == "OPSDECK.md") != (out[j].Path == "OPSDECK.md") {
			return out[i].Path == "OPSDECK.md"
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func mdTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return filepath.Base(path)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan() && i < 30; i++ {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
		if v, ok := strings.CutPrefix(line, "title:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
		if strings.HasPrefix(line, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
	}
	return filepath.Base(path)
}

func (w *Workspace) ReadDoc(rel string) (string, error) {
	p, err := w.docPath(rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

// errRepoDoc refuses edits of repository docs outside Git.
var errRepoDoc = errors.New("documentation from a Git repository is read-only here: change it in the repository and commit")

func (w *Workspace) WriteDoc(rel, content string) error {
	if strings.HasPrefix(filepath.ToSlash(rel), "repo/") {
		return errRepoDoc
	}
	p, err := w.docPath(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return atomicWrite(p, []byte(content))
}

func (w *Workspace) DeleteDoc(rel string) error {
	if strings.HasPrefix(filepath.ToSlash(rel), "repo/") {
		return errRepoDoc
	}
	p, err := w.docPath(rel)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// Skill is a packaged procedure the agent can load on demand.
type Skill = skills.Skill

// Skills lists the workspace's own skills: skills/<name>.md, or a folder
// skills/<name>/SKILL.md with supporting files (Agent Skills format).
// Frontmatter:
//
//	---
//	name: deploy-docker-app
//	description: Deploy a git repo as a single docker container on a host
//	---
func (w *Workspace) Skills() []Skill {
	out := []Skill{}
	add := func(abs, rel, dir string) {
		b, err := os.ReadFile(abs)
		if err != nil {
			return
		}
		name, desc := skills.Frontmatter(string(b))
		if name == "" {
			if dir != "" {
				name = filepath.Base(dir)
			} else {
				name = strings.TrimSuffix(filepath.Base(abs), ".md")
			}
		}
		if desc == "" {
			desc = mdTitle(abs)
		}
		s := Skill{Name: name, Description: desc, Path: rel}
		if dir != "" {
			filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() && p != abs {
					r, _ := filepath.Rel(dir, p)
					s.Files = append(s.Files, filepath.ToSlash(r))
				}
				return nil
			})
		}
		out = append(out, s)
	}
	matches, _ := filepath.Glob(filepath.Join(w.Path, "skills", "*.md"))
	for _, m := range matches {
		add(m, "skills/"+filepath.Base(m), "")
	}
	dirs, _ := filepath.Glob(filepath.Join(w.Path, "skills", "*", "SKILL.md"))
	for _, m := range dirs {
		add(m, "skills/"+filepath.Base(filepath.Dir(m))+"/SKILL.md", filepath.Dir(m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SkillFile reads a supporting file of a folder skill.
func (w *Workspace) SkillFile(s Skill, file string) (string, error) {
	if !strings.HasSuffix(s.Path, "/SKILL.md") {
		return "", errors.New("skill " + s.Name + " has no supporting files")
	}
	dir := filepath.Join(w.Path, filepath.FromSlash(strings.TrimSuffix(s.Path, "/SKILL.md")))
	clean := filepath.Clean(filepath.FromSlash(file))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid file")
	}
	p, err := w.contained(filepath.Join(dir, clean))
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if len(b) > 2<<20 {
		b = b[:2<<20]
	}
	return string(b), err
}

// ReportMeta describes who/what produced a report.
type ReportMeta struct {
	Title   string
	Session string
	Model   string
	Tags    []string
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// WriteReport stores a new report as reports/<date>-<slug>.md and returns
// its workspace-relative path. Reports are never overwritten.
func (w *Workspace) WriteReport(meta ReportMeta, body string, now time.Time) (string, error) {
	title := strings.TrimSpace(meta.Title)
	if title == "" {
		return "", errors.New("title is required")
	}
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	if slug == "" {
		slug = "report"
	}
	dir := filepath.Join(w.Path, "reports", now.Format("2006"), now.Format("01"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	base := now.Format("2006-01-02-1504") + "-" + slug
	path := filepath.Join(dir, base+".md")
	for i := 2; ; i++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.md", base, i))
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %q\n", title)
	fmt.Fprintf(&b, "created: %s\n", now.UTC().Format(time.RFC3339))
	if meta.Session != "" {
		fmt.Fprintf(&b, "session: %s\n", meta.Session)
	}
	if meta.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", meta.Model)
	}
	if len(meta.Tags) > 0 {
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(meta.Tags, ", "))
	}
	b.WriteString("---\n\n")
	if !strings.HasPrefix(strings.TrimSpace(body), "# ") {
		b.WriteString("# " + title + "\n\n")
	}
	b.WriteString(strings.TrimSpace(body) + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(w.Path, path)
	return filepath.ToSlash(rel), nil
}

// RecentReports returns up to n reports, newest first.
func (w *Workspace) RecentReports(n int) []Doc {
	var out []Doc
	for _, d := range w.Docs() {
		if strings.HasPrefix(d.Path, "reports/") {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path > out[j].Path })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// AdoptLegacyState moves state kept outside the folder by older versions
// (sessions, known_hosts, memory) into the workspace folder.
func (w *Workspace) AdoptLegacyState(oldDir string) error {
	if _, err := os.Stat(oldDir); err != nil {
		return nil
	}
	if err := os.MkdirAll(w.StateDir(), 0o700); err != nil {
		return err
	}
	moves := map[string]string{
		filepath.Join(oldDir, "sessions"):    filepath.Join(w.StateDir(), "sessions"),
		filepath.Join(oldDir, "known_hosts"): filepath.Join(w.StateDir(), "known_hosts"),
		filepath.Join(oldDir, "memory.md"):   w.MemoryPath(),
	}
	for from, to := range moves {
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if _, err := os.Stat(to); err == nil && !strings.HasSuffix(to, "MEMORY.md") {
			continue // never overwrite
		}
		if strings.HasSuffix(to, "MEMORY.md") {
			old, _ := os.ReadFile(from)
			cur, _ := os.ReadFile(to)
			merged := strings.TrimRight(string(cur), "\n") + "\n\n" + strings.TrimSpace(string(old)) + "\n"
			if err := os.WriteFile(to, []byte(strings.TrimLeft(merged, "\n")), 0o600); err != nil {
				return err
			}
			os.Remove(from)
			continue
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	os.Remove(oldDir) // only succeeds when empty
	return nil
}
