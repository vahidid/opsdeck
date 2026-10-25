// Command e2e runs opsdeck for the browser tests in e2e/: a real server with a
// fresh data folder, a workspace whose hosts are local SSH servers, and a
// scripted model. The vault is left locked. It prints one JSON line with the
// login URL and the master password, then serves until interrupted.
//
//	go run ./tools/e2e -port 7799
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"opsdeck/internal/paths"
	"opsdeck/internal/server"
	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/sshd"
)

const password = "correct horse battery"

func main() {
	port := flag.Int("port", 7799, "port to serve on")
	flag.Parse()
	home, err := os.MkdirTemp("", "opsdeck-e2e-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(home)
	os.Setenv("OPSDECK_HOME", home)

	srv, err := server.New(paths.Default(), server.Options{Port: *port, Version: "e2e"})
	if err != nil {
		log.Fatal(err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(*port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	go (&http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}).Serve(ln)

	login := srv.URL()
	jar, _ := cookiejar.New(nil)
	c := &client{base: "http://" + addr, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	c.do("POST", "/api/auth/login", map[string]string{"token": login[strings.Index(login, "#token=")+7:]}, nil)
	c.do("POST", "/api/vault/init", map[string]string{"password": password}, nil)
	c.do("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME Shop", "color": "#10b981"}, nil)
	c.do("PUT", "/api/workspaces/acme", map[string]any{"title": "ACME Shop",
		"environments": []map[string]any{{"name": "staging"}, {"name": "prod", "protected": true}}}, nil)

	var key struct{ PublicKey string }
	c.do("POST", "/api/vault/ssh/generate", map[string]any{"scope": "acme", "name": "deploy-key", "algorithm": "ed25519"}, &key)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
	if err != nil {
		log.Fatalf("ssh key: %v", err)
	}
	work, _ := os.MkdirTemp(home, "host-")
	opts := sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: work}
	bastion, err := sshd.Start("127.0.0.1:0", opts)
	if err != nil {
		log.Fatal(err)
	}
	app, err := sshd.Start("127.0.0.1:0", opts)
	if err != nil {
		log.Fatal(err)
	}
	c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "bastion", "environment": "prod",
		"spec": map[string]any{"address": "127.0.0.1", "port": portOf(bastion.Addr), "auth": "vault://acme/deploy-key", "roles": []string{"bastion"}}}, nil)
	c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "app-1", "environment": "prod", "via": "host/bastion",
		"spec": map[string]any{"address": "127.0.0.1", "port": portOf(app.Addr), "auth": "vault://acme/deploy-key", "roles": []string{"app"}}}, nil)

	// every new conversation follows this script: look, change (asks on the
	// protected prod), report
	llm := fakellm.New([]fakellm.Turn{
		{Text: "I'll look at the inventory and the app host first.", Tools: []fakellm.ToolCall{
			{Name: "inventory", Args: map[string]any{}},
			{Name: "exec", Args: map[string]any{"target": "app-1", "command": "uname -s"}},
		}},
		{Text: "The host is up. Now I'll write the marker file the deploy expects.", Tools: []fakellm.ToolCall{
			{Name: "exec", Args: map[string]any{"target": "app-1", "command": "echo deployed > marker.txt"}},
		}},
		{Text: "## Report\n\n- `app-1` is up.\n- Wrote `marker.txt`.\n\nE2E-DONE"},
	})
	var settings map[string]any
	c.do("GET", "/api/settings", nil, &settings)
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Scripted", "baseUrl": llm.URL, "models": []string{"m1"}}}
	c.do("PUT", "/api/settings", settings, nil)
	c.do("POST", "/api/vault/lock", nil, nil)
	if c.err != nil {
		log.Fatal(c.err)
	}

	out, _ := json.Marshal(map[string]string{"url": login, "base": c.base, "password": password, "hostDir": work})
	fmt.Println(string(out))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	srv.Close()
	bastion.Close()
	app.Close()
	llm.Close()
}

func portOf(addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	return n
}

type client struct {
	base string
	http *http.Client
	err  error
}

// do sends a JSON request; the first failure is kept in c.err.
func (c *client) do(method, path string, body, out any) {
	if c.err != nil {
		return
	}
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		c.err = err
		return
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		c.err = fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, b)
		return
	}
	if out != nil {
		json.Unmarshal(b, out)
	}
}
