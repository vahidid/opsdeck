package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/wgpeer"
)

// TestVPNAgent: a WireGuard profile in userspace mode with a local SOCKS5
// proxy. The agent checks the VPNs, connects one on its own (no approval:
// userspace only affects opsdeck), reads its log, reaches a panel behind
// it, and disconnecting needs approval. Other apps reach the office network
// through the proxy while it is up.
func TestVPNAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)

	peer, err := wgpeer.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	proxyPort := l.Addr().(*net.TCPAddr).Port
	l.Close()
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "vpn", "name": "office-wg",
		"fields": []map[string]any{{"name": "config", "value": peer.ClientConfig}}, "policy": map[string]any{"agentUse": true}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "WireGuard", "name": "office",
		"description": "Office network",
		"spec":        map[string]any{"config": "vault://acme/office-wg", "mode": "userspace", "proxyPort": proxyPort}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Panel", "name": "intranet", "via": "network/office",
		"spec": map[string]any{"url": "http://10.9.0.2:8080/"}}, nil)

	var drivers map[string]any
	c.must("GET", "/api/vpn/drivers", nil, &drivers)
	if len(drivers["drivers"].([]any)) != 6 {
		t.Fatalf("drivers: %v", drivers)
	}

	fake := fakellm.New([]fakellm.Turn{
		{Tools: []fakellm.ToolCall{{Name: "vpn", Args: map[string]any{"action": "status"}}}},
		{Tools: []fakellm.ToolCall{{Name: "vpn", Args: map[string]any{"action": "connect", "name": "office"}}}},
		{Tools: []fakellm.ToolCall{{Name: "vpn", Args: map[string]any{"action": "log", "name": "office"}}}},
		{Tools: []fakellm.ToolCall{{Name: "net_check", Args: map[string]any{"type": "http", "target": "panel/intranet"}}}},
		{Tools: []fakellm.ToolCall{{Name: "vpn", Args: map[string]any{"action": "disconnect", "name": "office"}}}},
		{Text: "Done."},
	})
	defer fake.Close()
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake.URL, "models": []string{"m1"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var sess map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"mode": "ask"}, &sess)
	sid := sess["id"].(string)
	stream, err := c.http.Get(ts.URL + "/api/workspaces/acme/sessions/" + sid + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "check the intranet"}, nil)

	var asked []string
	results := map[string]string{}
	started := false
	sc := bufio.NewScanner(stream.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	proxyOK := false
	for sc.Scan() {
		d, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev map[string]any
		json.Unmarshal([]byte(d), &ev)
		switch ev["type"] {
		case "approval_request":
			asked = append(asked, ev["tool"].(string))
			// before approving the disconnect, use the VPN from "another app"
			proxyOK = viaSOCKS(t, proxyPort)
			var nets []map[string]any
			c.must("GET", "/api/workspaces/acme/networks", nil, &nets)
			if nets[0]["status"] != "up" || nets[0]["proxy"] == "" || nets[0]["address"] != "10.9.0.1" {
				t.Errorf("network status while up: %v", nets[0])
			}
			c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/approvals/"+ev["approvalId"].(string), map[string]string{"decision": "approve"}, nil)
		case "tool_result":
			results[ev["callId"].(string)], _ = ev["output"].(string)
		case "status":
			if ev["state"] == "running" {
				started = true
			}
		}
		if started && ev["type"] == "status" && ev["state"] == "idle" {
			break
		}
	}
	if strings.Join(asked, ",") != "vpn" {
		t.Fatalf("only the disconnect should ask, asked: %v", asked)
	}
	check := func(id, sub string) {
		t.Helper()
		if !strings.Contains(results[id], sub) {
			t.Errorf("%s: want %q in %q", id, sub, results[id])
		}
	}
	check("call_0_0", "office (WireGuard, userspace mode): down")
	check("call_0_0", "used by Panel/intranet")
	check("call_1_0", "connected")
	check("call_2_0", "connecting office (userspace)")
	check("call_3_0", "200")
	check("call_4_0", "disconnected office")
	if !proxyOK {
		t.Error("the local SOCKS5 proxy did not reach the office network")
	}
	if !strings.Contains(fake.LastSystem(), "vpn tool") {
		t.Error("system prompt should explain when to connect VPNs")
	}
	var nets []map[string]any
	c.must("GET", "/api/workspaces/acme/networks", nil, &nets)
	if nets[0]["status"] != "down" {
		t.Fatalf("after disconnect: %v", nets[0])
	}
	var logLines []map[string]any
	c.must("GET", "/api/workspaces/acme/networks/office/log", nil, &logLines)
	if len(logLines) < 3 {
		t.Fatalf("log: %v", logLines)
	}
}

func viaSOCKS(t *testing.T, port int) bool {
	d, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, proxy.Direct)
	if err != nil {
		return false
	}
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.(proxy.ContextDialer).DialContext(ctx, network, addr)
		}}}
	res, err := hc.Get("http://10.9.0.2:8080/")
	if err != nil {
		t.Logf("socks: %v", err)
		return false
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b) == wgpeer.Body
}
