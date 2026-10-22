package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/sshd"
)

// TestDockerLive drives the Docker API against the real docker engine of
// this machine (through the test SSH server). It is skipped where there is
// no running daemon or no busybox image can be had.
func TestDockerLive(t *testing.T) {
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("no docker daemon: %s", bytes.TrimSpace(out))
	}
	if exec.Command("docker", "image", "inspect", "busybox:1.37").Run() != nil {
		if out, err := exec.Command("docker", "pull", "busybox:1.37").CombinedOutput(); err != nil {
			t.Skipf("cannot pull busybox: %s", out)
		}
	}
	home := t.TempDir()
	t.Setenv("OPSDECK_HOME", home)
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 3 * time.Minute}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)
	var key map[string]any
	c.must("POST", "/api/vault/ssh/generate", map[string]any{"scope": "acme", "name": "k", "algorithm": "ed25519"}, &key)
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(key["publicKey"].(string)))
	host, err := sshd.Start("127.0.0.1:0", sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	_, port, _ := net.SplitHostPort(host.Addr)
	p, _ := strconv.Atoi(port)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "d1",
		"spec": map[string]any{"address": "127.0.0.1", "port": p, "auth": "vault://acme/k"}}, nil)
	D := "/api/workspaces/acme/hosts/d1/docker"

	var info map[string]any
	c.must("GET", D+"/info", nil, &info)
	if info["serverVersion"] == "" || info["compose"] == "" {
		t.Fatalf("info: %v", info)
	}

	// --- a compose project created from the page
	project := "opsdeckt" + strconv.FormatInt(time.Now().UnixNano()%1e6, 10)
	dir := t.TempDir()
	defer exec.Command("docker", "compose", "-p", project, "down", "-v", "--remove-orphans").Run()
	compose := `services:
  web:
    image: busybox:1.37
    command: sh -c 'echo "level=info started"; echo "ERROR on stderr" >&2; sleep 1000'
    volumes: [data:/data]
volumes:
  data:
`
	if code := c.do("POST", D+"/compose", map[string]any{"name": project, "dir": dir, "content": "services: [", "start": true}, nil); code != 400 {
		t.Fatalf("invalid compose accepted: %d", code)
	}
	c.must("POST", D+"/compose", map[string]any{"name": project, "dir": dir, "content": compose, "start": true}, nil)
	var cts []map[string]any
	c.must("GET", D+"/compose/"+project, nil, &cts)
	if len(cts) != 1 || cts[0]["service"] != "web" || cts[0]["state"] != "running" {
		t.Fatalf("compose ps: %v", cts)
	}
	var files []map[string]any
	c.must("GET", D+"/compose/"+project+"/files", nil, &files)
	if len(files) != 2 || files[0]["path"] != filepath.Join(dir, "compose.yaml") || files[1]["kind"] != "env" || files[1]["exists"] != false {
		t.Fatalf("files: %v", files)
	}
	// an invalid edit is refused and leaves the file alone
	if code := c.do("PUT", D+"/compose/"+project+"/file", map[string]any{"path": files[0]["path"], "content": "services:\n  web:\n    image: [\n"}, nil); code != 400 {
		t.Fatal("invalid edit accepted")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); string(b) != compose {
		t.Fatal("compose file changed by a refused edit")
	}
	c.must("PUT", D+"/compose/"+project+"/file", map[string]any{"path": filepath.Join(dir, ".env"), "content": "GREETING=hi\n"}, nil)
	if b, _ := os.ReadFile(filepath.Join(dir, ".env")); string(b) != "GREETING=hi\n" {
		t.Fatal(".env not written")
	}

	// --- logs: compose prefixes, timestamps, stderr kept apart
	time.Sleep(time.Second)
	lines := sse(t, c, D[:len(D)-len("/hosts/d1/docker")]+"/logs/stream?host=d1&source=compose&project="+project+"&lines=50")
	if !hasLine(lines, func(l map[string]any) bool {
		return l["src"] == "web-1" && l["ts"] != nil && l["line"] == "level=info started"
	}) {
		t.Fatalf("compose logs: %v", lines)
	}
	ctName := cts[0]["name"].(string)
	lines = sse(t, c, "/api/workspaces/acme/logs/stream?host=d1&source=docker&container="+ctName+"&lines=50")
	if !hasLine(lines, func(l map[string]any) bool { return l["line"] == "ERROR on stderr" && l["stderr"] == true }) ||
		!hasLine(lines, func(l map[string]any) bool { return l["line"] == "level=info started" && l["stderr"] == nil }) {
		t.Fatalf("container logs: %v", lines)
	}
	events := sse(t, c, "/api/workspaces/acme/logs/stream?host=d1&source=events&since=10m")
	if !hasLine(events, func(l map[string]any) bool {
		e, _ := l["event"].(map[string]any)
		return e["type"] == "container" && e["action"] == "start"
	}) {
		t.Fatalf("events: %v", events)
	}

	// --- volume files
	vol := project + "_data"
	var vols []map[string]any
	c.must("GET", D+"/volumes", nil, &vols)
	found := false
	for _, v := range vols {
		if v["name"] == vol {
			found = v["project"] == project && len(v["containers"].([]any)) == 1
		}
	}
	if !found {
		t.Fatalf("volume %s not listed with its container: %v", vol, vols)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "hello.txt")
	fw.Write([]byte("hello volume\n"))
	mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+D+"/volumes/"+vol+"/upload?path=/", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if res, err := c.http.Do(req); err != nil || res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload: %v %s", err, b)
	}
	c.must("POST", D+"/volumes/"+vol+"/mkdir", map[string]any{"path": "/", "name": "conf"}, nil)
	c.must("PUT", D+"/volumes/"+vol+"/file", map[string]any{"path": "/conf/app.ini", "content": "x=1\n"}, nil)
	var listing struct {
		Access  string
		Entries []map[string]any
	}
	c.must("GET", D+"/volumes/"+vol+"/files?path=/", nil, &listing)
	names := []string{}
	for _, e := range listing.Entries {
		names = append(names, e["name"].(string)+":"+e["type"].(string))
	}
	if listing.Access == "" || strings.Join(names, ",") != "conf:dir,hello.txt:file" {
		t.Fatalf("listing (%s): %v", listing.Access, names)
	}
	// the container sees what was uploaded
	if out, _ := exec.Command("docker", "exec", ctName, "cat", "/data/hello.txt", "/data/conf/app.ini").CombinedOutput(); string(out) != "hello volume\nx=1\n" {
		t.Fatalf("container sees %q", out)
	}
	var file map[string]any
	c.must("GET", D+"/volumes/"+vol+"/file?path=/hello.txt", nil, &file)
	if file["content"] != "hello volume\n" || file["binary"] != false {
		t.Fatalf("read: %v", file)
	}
	res, _ := c.http.Get(ts.URL + D + "/volumes/" + vol + "/file?path=/conf/app.ini&download=1")
	b, _ := io.ReadAll(res.Body)
	if string(b) != "x=1\n" || !strings.Contains(res.Header.Get("Content-Disposition"), "app.ini") {
		t.Fatalf("download: %q %v", b, res.Header)
	}
	// nothing outside the volume is reachable, also through a symlink
	exec.Command("docker", "exec", ctName, "ln", "-s", "/etc", "/data/escape").Run()
	if code := c.do("GET", D+"/volumes/"+vol+"/files?path=/escape", nil, nil); code < 400 {
		t.Fatal("listed through a symlink out of the volume")
	}
	if code := c.do("GET", D+"/volumes/"+vol+"/file?path=/../../../../etc/hostname", nil, nil); code < 400 {
		t.Fatal("read outside the volume")
	}
	c.must("DELETE", D+"/volumes/"+vol+"/file?path=/escape", nil, nil)
	if _, err := os.Stat("/etc/hostname"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	c.must("DELETE", D+"/volumes/"+vol+"/file?path=/conf", nil, nil)
	if code := c.do("DELETE", D+"/volumes/"+vol+"/file?path=/", nil, nil); code < 400 {
		t.Fatal("deleted the volume root")
	}

	// --- exec into the container
	u, _ := http.NewRequest("GET", ts.URL, nil)
	hdr := http.Header{}
	for _, ck := range jar.Cookies(u.URL) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	wsc, _, err := websocket.DefaultDialer.Dial(strings.Replace(ts.URL, "http", "ws", 1)+D+"/containers/"+ctName+"/exec?cols=80&rows=24", hdr)
	if err != nil {
		t.Fatal(err)
	}
	wsc.WriteJSON(map[string]any{"t": "d", "d": "echo in-$((20+22)) && cat /data/hello.txt\n"})
	var term strings.Builder
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(term.String(), "hello volume") && time.Now().Before(deadline) {
		wsc.SetReadDeadline(deadline)
		var m map[string]any
		if err := wsc.ReadJSON(&m); err != nil {
			t.Fatalf("exec read: %v (%q)", err, term.String())
		}
		if d, ok := m["d"].(string); ok {
			term.WriteString(d)
		}
	}
	if !strings.Contains(term.String(), "in-42") {
		t.Fatalf("exec output: %q", term.String())
	}
	wsc.Close()

	// --- container details and actions
	var top []map[string]any
	c.must("GET", D+"/containers/"+ctName+"/top", nil, &top)
	if len(top) == 0 {
		t.Fatal("top: no processes")
	}
	var out map[string]string
	c.must("POST", D+"/compose/"+project+"/restart", map[string]any{"services": []string{"web"}}, &out)
	c.must("POST", D+"/compose/"+project+"/down-volumes", nil, &out)
	c.must("GET", D+"/volumes", nil, &vols)
	for _, v := range vols {
		if v["name"] == vol {
			t.Fatal("down-volumes kept the volume")
		}
	}

	// --- swarm (only where this engine is already a manager)
	var sw map[string]any
	c.must("GET", D+"/swarm", nil, &sw)
	if sw["state"] == "active" && sw["manager"] == true {
		stack := "ot" + strconv.FormatInt(time.Now().UnixNano()%1e6, 10)
		defer exec.Command("docker", "stack", "rm", stack).Run()
		c.must("POST", D+"/stacks", map[string]any{"name": stack, "content": "version: \"3.8\"\nservices:\n  echo:\n    image: busybox:1.37\n    command: sh -c 'while true; do echo tick; sleep 1; done'\n"}, nil)
		var svcs []map[string]any
		c.must("GET", D+"/services", nil, &svcs)
		var svc map[string]any
		for _, s := range svcs {
			if s["stack"] == stack {
				svc = s
			}
		}
		if svc == nil {
			t.Fatalf("stack service not listed: %v", svcs)
		}
		name := svc["name"].(string)
		c.must("POST", D+"/services/"+name+"/scale", map[string]any{"arg": "2"}, nil)
		var tasks []map[string]any
		for i := 0; i < 60; i++ {
			c.must("GET", D+"/services/"+name+"/tasks", nil, &tasks)
			running := 0
			for _, tk := range tasks {
				if strings.HasPrefix(tk["currentState"].(string), "Running") {
					running++
				}
			}
			if running == 2 {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		time.Sleep(1500 * time.Millisecond)
		lines = sse(t, c, "/api/workspaces/acme/logs/stream?host=d1&source=service&service="+name+"&lines=20")
		if !hasLine(lines, func(l map[string]any) bool {
			return l["line"] == "tick" && strings.HasPrefix(l["src"].(string), name+".")
		}) {
			t.Fatalf("service logs: %v", lines)
		}
		c.must("DELETE", D+"/stacks/"+stack, nil, nil)
	}
}

// sse reads a finite (non-follow) log stream.
func sse(t *testing.T, c *client, path string) []map[string]any {
	t.Helper()
	res, err := c.http.Get(c.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("GET %s: %d %s", path, res.StatusCode, b)
	}
	var out []map[string]any
	sc := bufio.NewScanner(res.Body)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if e, ok := strings.CutPrefix(line, "event: "); ok {
			event = e
			continue
		}
		if d, ok := strings.CutPrefix(line, "data: "); ok {
			if event == "error" {
				t.Fatalf("stream error: %s", d)
			}
			if event == "" {
				var m map[string]any
				json.Unmarshal([]byte(d), &m)
				out = append(out, m)
			}
			event = ""
		}
	}
	return out
}

func hasLine(lines []map[string]any, pred func(map[string]any) bool) bool {
	for _, l := range lines {
		if pred(l) {
			return true
		}
	}
	return false
}
