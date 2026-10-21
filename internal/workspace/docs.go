package workspace

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	ok := rel == "OPSDECK.md" ||
		(strings.HasPrefix(rel, "docs/") && strings.HasSuffix(rel, ".md")) ||
		(strings.HasPrefix(rel, "skills/") && strings.HasSuffix(rel, ".md") && strings.Count(rel, "/") == 1)
	if !ok {
		return "", errors.New("docs must be OPSDECK.md, docs/**/*.md or skills/*.md")
	}
	abs := filepath.Join(w.Path, filepath.FromSlash(rel))
	// Refuse paths that leave the workspace through symlinks.
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
	filepath.WalkDir(filepath.Join(w.Path, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			if info, err := d.Info(); err == nil {
				add(p, info)
			}
		}
		return nil
	})
	matches, _ := filepath.Glob(filepath.Join(w.Path, "skills", "*.md"))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil {
			add(m, info)
		}
	}
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

func (w *Workspace) WriteDoc(rel, content string) error {
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
	p, err := w.docPath(rel)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// Skill is a packaged procedure the agent can load on demand.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

// Skills parses skills/*.md. Optional YAML-ish frontmatter:
//
//	---
//	name: deploy-docker-app
//	description: Deploy a git repo as a single docker container on a host
//	---
func (w *Workspace) Skills() []Skill {
	out := []Skill{}
	matches, _ := filepath.Glob(filepath.Join(w.Path, "skills", "*.md"))
	sort.Strings(matches)
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		s := Skill{Name: strings.TrimSuffix(filepath.Base(m), ".md"), Path: "skills/" + filepath.Base(m)}
		text := string(b)
		if strings.HasPrefix(text, "---") {
			if end := strings.Index(text[3:], "\n---"); end > 0 {
				for _, line := range strings.Split(text[3:3+end], "\n") {
					k, v, ok := strings.Cut(line, ":")
					if !ok {
						continue
					}
					switch strings.TrimSpace(k) {
					case "name":
						s.Name = strings.TrimSpace(v)
					case "description":
						s.Description = strings.TrimSpace(v)
					}
				}
			}
		}
		if s.Description == "" {
			s.Description = mdTitle(m)
		}
		out = append(out, s)
	}
	return out
}
