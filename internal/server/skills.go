package server

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"opsdeck/internal/skills"
)

func (s *Server) skillLibrary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"skills": skills.Library(), "attribution": skills.Attribution()})
}

func (s *Server) skillLibraryFile(w http.ResponseWriter, r *http.Request) {
	body, err := skills.Read(r.PathValue("name"), r.URL.Query().Get("file"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"content": body})
}

// skillCopy copies a built-in skill into the workspace (skills/<name>/) so
// it can be adapted; the copy then takes precedence over the built-in one.
func (s *Server) skillCopy(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	src, err := skills.FS(name)
	if err != nil {
		fail(w, err)
		return
	}
	dst := filepath.Join(ws.Path, "skills", name)
	if _, err := os.Stat(dst); err == nil {
		fail(w, errors.New("skills/"+name+" already exists"))
		return
	}
	err = fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	s.record(r, ws.Name(), "skill.copy", "skill/"+name, nil, err)
	if err != nil {
		os.RemoveAll(dst)
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"path": "skills/" + name + "/SKILL.md"})
}
