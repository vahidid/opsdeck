package server

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/workspace"
)

// TestArtifactDownload: the download endpoint streams the whole artifact (the
// file API truncates at 2 MB for display) under its own file name, and refuses
// unsafe paths.
func TestArtifactDownload(t *testing.T) {
	c := newMachine(t, "alice master pw")
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME"}, nil)
	var wss []wsSummary
	c.must("GET", "/api/workspaces", nil, &wss)

	ws, err := workspace.Load(wss[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("row,values\n", 200_000) // 2.4 MB, above the display cap
	rel, err := ws.SaveArtifact("artifacts/checks/report.csv", []byte(content), workspace.ArtifactMeta{Source: "text"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	var view map[string]any
	c.must("GET", "/api/workspaces/acme/artifacts/file?path="+url.QueryEscape(rel), nil, &view)
	if view["truncated"] != true {
		t.Fatal("expected the file API to truncate a 2.4 MB artifact")
	}

	res, err := c.http.Get(c.base + "/api/workspaces/acme/artifacts/download?path=" + url.QueryEscape(rel))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("download: %d", res.StatusCode)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "report.csv") {
		t.Fatalf("content disposition: %q", cd)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != content {
		t.Fatalf("downloaded %d bytes, want %d", len(body), len(content))
	}

	if code := c.do("GET", "/api/workspaces/acme/artifacts/download?path=../OPSDECK.md", nil, nil); code < http.StatusBadRequest {
		t.Fatalf("unsafe path accepted: %d", code)
	}
}
