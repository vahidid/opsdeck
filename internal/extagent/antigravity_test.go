package extagent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func agyRun(out string) (Result, string, string, []Event) {
	var evs []Event
	st := &agyStream{msgID: "m"}
	for _, l := range strings.Split(out, "\n") {
		st.line([]byte(l), func(e Event) { evs = append(evs, e) })
	}
	res, text, fail := st.finish()
	return res, text, fail, evs
}

func TestAgyStream(t *testing.T) {
	res, text, fail, evs := agyRun(`{"event":"init","conversation_id":"c-1","init":{"cwd":"/w","permission_mode":"always-proceed"}}
{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"mcp_opsdeck_inventory"}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":3,"state":"ACTIVE","step_type":"agent_response","text_delta":"First part. "}}
{"event":"step_update","step_update":{"conversation_id":"c-1","step_index":3,"state":"DONE","step_type":"agent_response","text_delta":"Second part."}}
{"event":"result","result":{"status":"SUCCESS","response":"First part. Second part.","num_turns":2,"usage":{"input_tokens":10,"cache_read_tokens":5,"output_tokens":3}}}`)
	if res.Resume != "c-1" || text != "First part. Second part." || fail != "" || res.InputTokens != 15 || res.OutputTokens != 3 {
		t.Fatalf("%+v %q %q", res, text, fail)
	}
	if len(evs) != 2 || evs[0].Kind != "text_delta" || evs[0].Text != "First part. " || evs[1].MsgID != "m" {
		t.Fatalf("%+v", evs)
	}
	_, _, fail, _ = agyRun(`{"event":"result","result":{"status":"ERROR","error":"quota exceeded"}}`)
	if fail != "quota exceeded" {
		t.Fatalf("%q", fail)
	}
	_, _, fail, _ = agyRun(`{"event":"result","result":{"status":"ERROR"}}`)
	if !strings.Contains(fail, "ERROR") {
		t.Fatalf("%q", fail)
	}
	if _, text, _, _ = agyRun("plain answer"); text != "plain answer" {
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

// A fake agy gets the bridge in .agents/mcp_config.json and the message
// (with the instructions, the first time) on stdin as stream-json, after an
// empty -p=; agy's settings allow the opsdeck tools; the conversation id
// comes back.
func TestRunAntigravityFake(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o700)
	os.WriteFile(settings, []byte(`{"colorScheme":"dark","permissions":{"allow":["command(git)"]}}`), 0o600)
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	os.WriteFile(bin, []byte(`#!/bin/sh
[ "$1" = "-p=" ] || { echo "-p took $2 as its prompt" >&2; exit 2; }
case "$*" in *"--input-format stream-json"*) case "$*" in *"--output-format stream-json"*) ;; *) echo "Error: --input-format stream-json requires --output-format stream-json" >&2; exit 2;; esac;; esac
cfg=$(cat .agents/mcp_config.json)
in=$(cat)
case "$cfg" in *tok-1*) ;; *) echo '{"event":"result","result":{"status":"ERROR","error":"no bridge"}}'; exit 1;; esac
case "$in" in *'"event":"user"'*'<instructions>'*) ;; *) echo '{"event":"result","result":{"status":"ERROR","error":"bad input"}}'; exit 1;; esac
echo '{"event":"init","conversation_id":"conv-9","init":{"cwd":"/w"}}'
echo '{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"done"}}'
echo '{"event":"result","result":{"status":"SUCCESS","response":"done: '"$*"'"}}'
`), 0o755)
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o700)
	var got []Event
	res, err := Run(context.Background(), Request{Spec: Spec{Kind: Antigravity, Command: bin, Model: "gemini-3-pro"}, Prompt: "hi", System: "be careful",
		FirstTurn: true, MCPURL: "http://127.0.0.1:1/mcp/x", MCPToken: "tok-1", WorkDir: work}, func(e Event) { got = append(got, e) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Resume != "conv-9" || len(got) != 2 || got[0].Kind != "text_delta" || got[1].Kind != "text" || got[0].MsgID != got[1].MsgID ||
		!strings.Contains(got[1].Text, "--model gemini-3-pro") || !strings.Contains(got[1].Text, "--output-format stream-json") {
		t.Fatalf("%+v %+v", res, got)
	}
	if _, err := os.Stat(filepath.Join(work, ".agents", "mcp_config.json")); err == nil {
		t.Fatal("the token file is left behind")
	}
	b, _ := os.ReadFile(settings)
	if !strings.Contains(string(b), `"mcp(opsdeck/*)"`) || !strings.Contains(string(b), `"command(git)"`) || !strings.Contains(string(b), `"colorScheme"`) {
		t.Fatalf("settings %s", b)
	}
	// a second run does not add the rule again
	Run(context.Background(), Request{Spec: Spec{Kind: Antigravity, Command: bin}, Prompt: "again", System: "x", FirstTurn: true, MCPURL: "u", MCPToken: "tok-1", WorkDir: work}, func(Event) {})
	if b2, _ := os.ReadFile(settings); strings.Count(string(b2), "mcp(opsdeck/*)") != 1 {
		t.Fatalf("settings %s", b2)
	}
}
