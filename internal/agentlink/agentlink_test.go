package agentlink

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setHome(t *testing.T) string {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData"))
	return h
}

func target(secret string) Target {
	return Target{Name: "opsdeck-acme", Endpoint: "http://127.0.0.1:7788/mcp/ext", Secret: secret, Command: "/usr/bin/opsdeck"}
}

func TestJSONClientsKeepOtherServers(t *testing.T) {
	h := setHome(t)
	ctx := context.Background()
	path := filepath.Join(h, ".cursor", "mcp.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"mcpServers":{"other":{"url":"http://x"}},"keep":1}`), 0o644)

	if Installed("cursor", "opsdeck-acme") {
		t.Fatal("installed before install")
	}
	if _, err := Install(ctx, "cursor", target("odk_one")); err != nil {
		t.Fatal(err)
	}
	if !Installed("cursor", "opsdeck-acme") {
		t.Fatal("not installed")
	}
	// rotate: same name, new key
	if _, err := Install(ctx, "cursor", target("odk_two")); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &doc)
	servers := doc["mcpServers"].(map[string]any)
	if servers["other"] == nil || doc["keep"] == nil {
		t.Fatalf("other settings lost: %s", b)
	}
	got := servers["opsdeck-acme"].(map[string]any)["headers"].(map[string]any)["Authorization"]
	if got != "Bearer odk_two" {
		t.Fatalf("header %v", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if bak, _ := os.ReadFile(path + ".opsdeck-backup"); !strings.Contains(string(bak), `"other"`) || strings.Contains(string(bak), "opsdeck-acme") {
		t.Fatalf("backup %s", bak)
	}
	if err := Uninstall(ctx, "cursor", "opsdeck-acme"); err != nil {
		t.Fatal(err)
	}
	if Installed("cursor", "opsdeck-acme") {
		t.Fatal("still installed")
	}
}

func TestFormats(t *testing.T) {
	h := setHome(t)
	ctx := context.Background()
	for id, want := range map[string]string{
		"gemini-cli":      `"httpUrl"`,
		"antigravity-cli": `"serverUrl"`,
		"antigravity":     `"serverUrl"`,
		"windsurf":        `"serverUrl"`,
		"vscode":          `"servers"`,
		"claude-desktop":  `"OPSDECK_TOKEN"`,
	} {
		path, err := Install(ctx, id, target("odk_x"))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if !strings.HasPrefix(path, h) {
			t.Fatalf("%s: %s outside home", id, path)
		}
		b, _ := os.ReadFile(path)
		if !strings.Contains(string(b), want) || !strings.Contains(string(b), "odk_x") {
			t.Fatalf("%s: %s", id, b)
		}
		if !Installed(id, "opsdeck-acme") {
			t.Fatalf("%s: not installed", id)
		}
	}
}

func TestJSONWithComments(t *testing.T) {
	h := setHome(t)
	path := filepath.Join(h, ".gemini", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("{\n // mine\n \"theme\": \"x\"\n}"), 0o644)
	if _, err := Install(context.Background(), "gemini-cli", target("odk_x")); err == nil {
		t.Fatal("a file with comments must be left alone")
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "// mine") {
		t.Fatal("file changed")
	}
}

func TestCodexTOML(t *testing.T) {
	h := setHome(t)
	ctx := context.Background()
	path := filepath.Join(h, ".codex", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("model = \"o3\"\n\n[mcp_servers.opsdeck-acme]\ncommand = \"old\"\n\n[mcp_servers.opsdeck-acme.env]\nX = \"1\"\n\n[mcp_servers.other]\ncommand = \"o\"\n"), 0o644)
	if _, err := Install(ctx, "codex", target("odk_new")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if strings.Contains(s, `"old"`) || strings.Contains(s, "X = ") || !strings.Contains(s, "[mcp_servers.other]") || !strings.Contains(s, `model = "o3"`) ||
		strings.Count(s, "[mcp_servers.opsdeck-acme]") != 1 || !strings.Contains(s, `OPSDECK_TOKEN = "odk_new"`) {
		t.Fatalf("config:\n%s", s)
	}
	if err := Uninstall(ctx, "codex", "opsdeck-acme"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "opsdeck-acme") || !strings.Contains(string(b), "[mcp_servers.other]") {
		t.Fatalf("after uninstall:\n%s", b)
	}
}

func TestServerName(t *testing.T) {
	if n := ServerName("my ws/1"); n != "opsdeck-my-ws-1" {
		t.Fatal(n)
	}
	if _, err := Install(context.Background(), "cursor", Target{Name: "bad name"}); err == nil {
		t.Fatal("bad name accepted")
	}
	if Known("nope") || !Known("antigravity") {
		t.Fatal("Known")
	}
}
