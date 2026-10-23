package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"opsdeck/internal/paths"
	"opsdeck/internal/sysproc"
)

// The desktop app shows the UI in a webview, which neither opens links in
// a new browser window nor saves downloads. In desktop mode the UI hands
// both to these endpoints instead.
func (s *Server) desktopRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/desktop/open", s.desktopOpen)
	mux.HandleFunc("POST /api/desktop/save", s.desktopSave)
}

// Hand-offs to the desktop (replaced in tests).
var (
	openInBrowser = sysproc.Open
	revealFile    = sysproc.Reveal
)

// desktopOpen opens a web link in the default browser.
func (s *Server) desktopOpen(w http.ResponseWriter, r *http.Request) {
	var in struct{ URL string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, http.StatusBadRequest, "only http and https links can be opened")
		return
	}
	if err := openInBrowser(u.String()); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// desktopSave writes the request body to the downloads folder under a free
// name and shows it in the file manager.
func (s *Server) desktopSave(w http.ResponseWriter, r *http.Request) {
	dir := paths.Downloads()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(w, err)
		return
	}
	f, path, err := createFree(dir, safeFileName(r.URL.Query().Get("name")))
	if err != nil {
		fail(w, err)
		return
	}
	_, err = io.Copy(f, r.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		fail(w, err)
		return
	}
	revealFile(path)
	writeJSON(w, 200, map[string]string{"path": path})
}

// safeFileName keeps the last element of a suggested name, without
// characters that are not allowed in file names on some system.
func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "download"
	}
	return name
}

// createFree creates name in dir, or "name (2).ext", "name (3).ext"… when
// it exists.
func createFree(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		p := filepath.Join(dir, name)
		if i > 1 {
			p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, p, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("no free file name for " + name)
}
