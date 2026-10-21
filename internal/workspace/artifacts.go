package workspace

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxArtifactSize bounds a single artifact.
const MaxArtifactSize = 5 << 20

// Artifact is a file kept as evidence: a config snapshot from a server, the
// full output of a command, or text the agent produced.
type Artifact struct {
	Path    string    `json:"path"` // workspace-relative, artifacts/...
	Size    int64     `json:"size"`
	Updated time.Time `json:"updated"`
}

// ArtifactMeta is recorded in artifacts/INDEX.md.
type ArtifactMeta struct {
	Source  string // e.g. "file app-1:/etc/nginx/nginx.conf", "command app-1: df -h", "text"
	Session string
	Note    string
}

var unsafeSeg = regexp.MustCompile(`[^A-Za-z0-9._@+-]+`)

func cleanSegment(s string) string {
	s = strings.Trim(unsafeSeg.ReplaceAllString(s, "_"), "._")
	if s == "" {
		s = "file"
	}
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}

// ArtifactPath builds artifacts/<date>/<group>/<rel...> from untrusted parts:
// every segment is sanitised so the result always stays inside artifacts/.
func ArtifactPath(now time.Time, group string, rel string) string {
	parts := []string{"artifacts", now.Format("2006-01-02")}
	if group != "" {
		parts = append(parts, cleanSegment(group))
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		parts = append(parts, cleanSegment(seg))
	}
	if len(parts) == 2 || (group != "" && len(parts) == 3) {
		parts = append(parts, "artifact.txt")
	}
	return strings.Join(parts, "/")
}

// SaveArtifact writes data under rel (from ArtifactPath). If the file exists a
// time suffix is added, so artifacts are never overwritten. Only text is
// accepted. It returns the final workspace-relative path.
func (w *Workspace) SaveArtifact(rel string, data []byte, meta ArtifactMeta, now time.Time) (string, error) {
	if !strings.HasPrefix(rel, "artifacts/") || strings.Contains(rel, "..") {
		return "", errors.New("invalid artifact path")
	}
	if len(data) > MaxArtifactSize {
		return "", fmt.Errorf("artifact too large (%d bytes, max %d)", len(data), MaxArtifactSize)
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", errors.New("only text artifacts are supported (binary content refused)")
	}
	abs, err := w.contained(filepath.Join(w.Path, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err == nil {
		ext := filepath.Ext(abs)
		abs = strings.TrimSuffix(abs, ext) + "." + now.Format("150405") + ext
		for i := 2; ; i++ {
			if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
				break
			}
			abs = strings.TrimSuffix(abs, ext) + fmt.Sprintf("-%d", i) + ext
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, data, 0o600); err != nil {
		return "", err
	}
	final, _ := filepath.Rel(w.Path, abs)
	final = filepath.ToSlash(final)
	w.appendArtifactIndex(final, int64(len(data)), meta, now)
	return final, nil
}

func (w *Workspace) appendArtifactIndex(path string, size int64, meta ArtifactMeta, now time.Time) {
	index := filepath.Join(w.Path, "artifacts", "INDEX.md")
	if _, err := os.Stat(index); errors.Is(err, os.ErrNotExist) {
		os.WriteFile(index, []byte("# Artifacts\n\nFiles saved as evidence (config snapshots, command output, generated files).\n\n"+
			"| time | file | source | session | note |\n|---|---|---|---|---|\n"), 0o600)
	}
	f, err := os.OpenFile(index, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	cell := func(s string) string { return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s) }
	rel := strings.TrimPrefix(path, "artifacts/")
	fmt.Fprintf(f, "| %s | [%s](%s) | %s | %s | %s |\n", now.Format("2006-01-02 15:04"), cell(rel), rel,
		cell(meta.Source), cell(meta.Session), cell(meta.Note))
}

// Artifacts lists artifact files, newest first.
func (w *Workspace) Artifacts() []Artifact {
	out := []Artifact{}
	filepath.WalkDir(filepath.Join(w.Path, "artifacts"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(w.Path, p)
		out = append(out, Artifact{Path: filepath.ToSlash(rel), Size: info.Size(), Updated: info.ModTime()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == "artifacts/INDEX.md" || out[j].Path == "artifacts/INDEX.md" {
			return out[i].Path == "artifacts/INDEX.md"
		}
		return out[i].Updated.After(out[j].Updated)
	})
	return out
}

func (w *Workspace) artifactAbs(rel string) (string, error) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if !strings.HasPrefix(rel, "artifacts/") || strings.Contains(rel, "..") {
		return "", errors.New("invalid artifact path")
	}
	return w.contained(filepath.Join(w.Path, filepath.FromSlash(rel)))
}

// ReadArtifact returns up to max bytes of an artifact.
func (w *Workspace) ReadArtifact(rel string, max int64) (string, bool, error) {
	abs, err := w.artifactAbs(rel)
	if err != nil {
		return "", false, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return "", false, err
	}
	if int64(len(b)) > max {
		return string(b[:max]), true, nil
	}
	return string(b), false, nil
}

// DeleteArtifact removes one artifact file.
func (w *Workspace) DeleteArtifact(rel string) error {
	abs, err := w.artifactAbs(rel)
	if err != nil {
		return err
	}
	return os.Remove(abs)
}
