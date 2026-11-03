package extagent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseAntigravity(t *testing.T) {
	res, text, fail := parseAntigravity(`{"conversation_id":"c-1","status":"success","response":"all good","usage":{"input_tokens":10,"output_tokens":3}}`)
	if res.Resume != "c-1" || text != "all good" || fail != "" || res.InputTokens != 10 || res.OutputTokens != 3 {
		t.Fatalf("%+v %q %q", res, text, fail)
	}
	_, _, fail = parseAntigravity(`{"conversation_id":"c-1","status":"error","error":{"message":"not signed in"}}`)
	if fail != "not signed in" {
		t.Fatalf("%q", fail)
	}
	// NDJSON events
	res, text, _ = parseAntigravity("{\"type\":\"init\",\"session_id\":\"s-2\"}\n{\"role\":\"assistant\",\"content\":\"he\",\"delta\":true}\n{\"role\":\"assistant\",\"content\":\"llo\",\"delta\":true}\n")
	if res.Resume != "s-2" || text != "hello" {
		t.Fatalf("%+v %q", res, text)
	}
	if _, text, _ = parseAntigravity("plain answer\n"); text != "plain answer" {
		t.Fatalf("%q", text)
	}
}

func TestParseAgyModels(t *testing.T) {
	m := parseAgyModels("Available models:\n* gemini-3-pro (default)\n  gemini-3-flash  fast\n  claude-sonnet-4.5\n")
	var ids []string
	for _, x := range m {
		ids = append(ids, x.ID)
	}
	if strings.Join(ids, ",") != "default,gemini-3-pro,gemini-3-flash,claude-sonnet-4.5" {
		t.Fatal(ids)
	}
}

// A fake agy gets the bridge in .agents/mcp_config.json and the
// instructions with the first message; the conversation id comes back.
func TestRunAntigravityFake(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	os.WriteFile(bin, []byte(`#!/bin/sh
cfg=$(cat .agents/mcp_config.json)
in=$(cat)
case "$cfg" in *tok-1*) ;; *) echo '{"status":"error","error":"no bridge"}'; exit 1;; esac
case "$in" in *"<instructions>"*) ;; *) echo '{"status":"error","error":"no instructions"}'; exit 1;; esac
echo '{"conversation_id":"conv-9","status":"success","response":"done: '"$*"'"}'
`), 0o755)
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o700)
	var got []Event
	res, err := Run(context.Background(), Request{Spec: Spec{Kind: Antigravity, Command: bin, Model: "gemini-3-pro"}, Prompt: "hi", System: "be careful",
		FirstTurn: true, MCPURL: "http://127.0.0.1:1/mcp/x", MCPToken: "tok-1", WorkDir: work}, func(e Event) { got = append(got, e) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Resume != "conv-9" || len(got) != 1 || !strings.Contains(got[0].Text, "--model gemini-3-pro") || !strings.Contains(got[0].Text, "--sandbox") {
		t.Fatalf("%+v %+v", res, got)
	}
	if _, err := os.Stat(filepath.Join(work, ".agents", "mcp_config.json")); err == nil {
		t.Fatal("the token file is left behind")
	}
}
