package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/fakellm"
)

// a 1×1 PNG
var tinyPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

// Files attached to a message reach the model: an image as an image, a
// text file as text (redacted); the transcript keeps only references.
func TestAttachments(t *testing.T) {
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME"}, nil)

	fake := fakellm.New([]fakellm.Turn{{Text: "I see a tiny image."}})
	defer fake.Close()
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake.URL, "models": []string{"m1"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var sess map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"mode": "ask"}, &sess)
	sid := sess["id"].(string)
	base := "/api/workspaces/acme/sessions/" + sid

	upload := func(name string, body []byte) (map[string]any, int) {
		req, _ := http.NewRequest("POST", ts.URL+base+"/attachments?name="+name, bytes.NewReader(body))
		res, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return out, res.StatusCode
	}
	img, code := upload("screen.png", tinyPNG)
	if code != 200 || img["kind"] != "image" || img["mime"] != "image/png" {
		t.Fatalf("%d %v", code, img)
	}
	txt, _ := upload("app.log", []byte("ERROR db timeout\npassword=hunter2hunter2\n"))
	if txt["kind"] != "text" {
		t.Fatalf("%v", txt)
	}
	if _, code := upload("empty.txt", nil); code < 400 {
		t.Fatal("an empty file was accepted")
	}

	c.must("POST", base+"/messages", map[string]any{"text": "", "attachments": []string{img["id"].(string), txt["id"].(string)}}, nil)
	var full struct {
		Session map[string]any
		Events  []map[string]any
	}
	for i := 0; i < 200; i++ {
		c.must("GET", base, nil, &full)
		if full.Session["state"] == "idle" && len(fake.Requests) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if full.Session["title"] != "screen.png" {
		t.Fatalf("title %v", full.Session["title"])
	}
	var user map[string]any
	for _, e := range full.Events {
		if e["type"] == "user" {
			user = e
		}
	}
	if atts, _ := user["attachments"].([]any); len(atts) != 2 {
		t.Fatalf("user event %v", user)
	}

	fake.Mu.Lock()
	b, _ := json.Marshal(fake.Requests[0]["messages"])
	fake.Mu.Unlock()
	req := string(b)
	if !strings.Contains(req, `"image_url"`) || !strings.Contains(req, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(tinyPNG)) {
		t.Fatalf("no image in the request: %s", req)
	}
	if !strings.Contains(req, `attachment name=\"app.log\"`) || !strings.Contains(req, "ERROR db timeout") || strings.Contains(req, "hunter2hunter2") {
		t.Fatalf("text attachment: %s", req)
	}

	// the transcript has a reference, not the bytes
	var files []string
	filepath.WalkDir(os.Getenv("OPSDECK_HOME"), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == sid+".json" {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 1 {
		t.Fatalf("transcripts: %v", files)
	}
	for _, f := range files {
		if raw, _ := os.ReadFile(f); bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(tinyPNG))) {
			t.Fatal("the image is stored in the transcript")
		}
	}

	// the image is served for the chat
	res, err := c.http.Get(ts.URL + base + "/attachments/" + img["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.Header.Get("Content-Type") != "image/png" || !bytes.Equal(got, tinyPNG) || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("%v %d", res.Header, len(got))
	}
	if code := c.do("GET", base+"/attachments/a-0000000000", nil, nil); code != 404 {
		t.Fatalf("missing attachment: %d", code)
	}
	if code := c.do("GET", base+"/attachments/..%2f"+sid, nil, nil); code < 400 {
		t.Fatalf("path in an attachment id: %d", code)
	}
}
