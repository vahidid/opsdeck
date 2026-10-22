package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RepoDir is where the working copy of a GitRemote lives (git-ignored).
func (w *Workspace) RepoDir(name string) string { return filepath.Join(w.Path, "repos", name) }

// RepoDocPaths are the files and folders of a documentation repository
// that count as docs.
func RepoDocPaths(r *Resource) []string {
	if p := r.List("docsPaths"); len(p) > 0 {
		return p
	}
	return []string{"README.md", "docs"}
}

func isMarkdown(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".md" || ext == ".markdown"
}

// docRepos are the cloned GitRemotes marked as documentation.
func (w *Workspace) docRepos() []*Resource {
	var out []*Resource
	for _, r := range w.Resources {
		if r.Kind == "GitRemote" && r.Bool("docs", false) {
			if _, err := os.Stat(filepath.Join(w.RepoDir(r.Name), ".git")); err == nil {
				out = append(out, r)
			}
		}
	}
	return out
}

// repoDocs lists Markdown files of documentation repositories as
// repo/<name>/<path>.
func (w *Workspace) repoDocs() []Doc {
	out := []Doc{}
	for _, r := range w.docRepos() {
		dir := w.RepoDir(r.Name)
		n := 0
		seen := map[string]bool{}
		for _, p := range RepoDocPaths(r) {
			p = strings.Trim(filepath.ToSlash(filepath.Clean(p)), "/")
			if p == "" || p == "." {
				p = "."
			}
			if strings.HasPrefix(p, "..") {
				continue
			}
			filepath.WalkDir(filepath.Join(dir, filepath.FromSlash(p)), func(abs string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if d.Name() == ".git" || d.Name() == "node_modules" {
						return filepath.SkipDir
					}
					return nil
				}
				if !isMarkdown(abs) || n >= 1000 || d.Type()&fs.ModeSymlink != 0 {
					return nil
				}
				rel, _ := filepath.Rel(dir, abs)
				key := "repo/" + r.Name + "/" + filepath.ToSlash(rel)
				if seen[key] {
					return nil
				}
				seen[key] = true
				if info, err := d.Info(); err == nil {
					out = append(out, Doc{Path: key, Title: mdTitle(abs), Size: info.Size(), Updated: info.ModTime()})
					n++
				}
				return nil
			})
		}
	}
	return out
}

// repoDocPath resolves repo/<name>/<path> to a Markdown file inside the
// repository's documentation paths.
func (w *Workspace) repoDocPath(rel string) (string, error) {
	rest := strings.TrimPrefix(rel, "repo/")
	name, p, ok := strings.Cut(rest, "/")
	if !ok || p == "" {
		return "", errors.New("invalid path")
	}
	r := w.Find("GitRemote", name)
	if r == nil || !r.Bool("docs", false) {
		return "", errors.New("repository " + name + " is not a documentation repository")
	}
	if !isMarkdown(p) || strings.HasPrefix(p, ".git/") || strings.Contains(p, "/.git/") {
		return "", errors.New("not a Markdown file")
	}
	inDocs := false
	for _, d := range RepoDocPaths(r) {
		d = strings.Trim(filepath.ToSlash(filepath.Clean(d)), "/")
		if d == "." || d == "" || p == d || strings.HasPrefix(p, d+"/") {
			inDocs = true
		}
	}
	if !inDocs {
		return "", errors.New("not in the repository's documentation paths")
	}
	return w.contained(filepath.Join(w.RepoDir(name), filepath.FromSlash(p)))
}

// IgnoreRepos makes sure working copies are git-ignored when the workspace
// folder is itself kept in Git (folders made by older versions).
func (w *Workspace) IgnoreRepos() {
	p := filepath.Join(w.Path, ".gitignore")
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		if l := strings.TrimSpace(line); l == "repos/" || l == "/repos/" || l == "repos" || l == "/repos" {
			return
		}
	}
	s := string(b)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	os.WriteFile(p, []byte(s+"# Git working copies (opsdeck)\nrepos/\n"), 0o600)
}
