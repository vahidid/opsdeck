package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/testutil/fakellm"
)

// requestText is everything the model got in request i.
func requestText(fake *fakellm.Server, i int) string {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	if i >= len(fake.Requests) {
		return ""
	}
	b, _ := json.Marshal(fake.Requests[i]["messages"])
	return string(b)
}

func nRequests(fake *fakellm.Server) int {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	return len(fake.Requests)
}

func approvalReq(ev map[string]any) bool { return ev["type"] == "approval_request" }

// A turn held by an approval: messages sent meanwhile wait. Queued ones
// start the next turn together, in order; a cancelled one is not sent.
func TestQueuedMessagesWaitForTheTurn(t *testing.T) {
	h := newHarness(t)
	c := h.c
	marker := filepath.Join(h.dirs[0], "queued")
	fake := h.model([]fakellm.Turn{
		{Text: "Changing.", Tools: []fakellm.ToolCall{{Name: "exec", Args: map[string]any{"target": "app-1", "command": "touch " + marker}}}},
		{Text: "First turn done."},
		{Text: "Second turn done."},
	})
	sid, st := h.session(map[string]any{"mode": "ask"})
	base := "/api/workspaces/acme/sessions/" + sid
	c.must("POST", base+"/messages", map[string]string{"text": "make the change"}, nil)
	appr := st.wait(approvalReq, "approval")

	// without a mode a message during a turn is refused, as before
	if code := c.do("POST", base+"/messages", map[string]string{"text": "hello?"}, nil); code != 409 && code < 400 {
		t.Fatalf("a message without a mode was accepted during a turn: %d", code)
	}
	var sent struct{ Pending map[string]any }
	c.must("POST", base+"/messages", map[string]string{"text": "then check disk", "mode": "queue"}, &sent)
	first := sent.Pending["id"].(string)
	c.must("POST", base+"/messages", map[string]string{"text": "drop this one", "mode": "queue"}, &sent)
	dropped := sent.Pending["id"].(string)
	c.must("POST", base+"/messages", map[string]string{"text": "and memory", "mode": "queue"}, &sent)
	third := sent.Pending["id"].(string)
	c.must("DELETE", base+"/pending/"+dropped, nil, nil)
	if code := c.do("DELETE", base+"/pending/"+dropped, nil, nil); code != 404 {
		t.Fatalf("cancelling twice: %d", code)
	}
	var got struct{ Pending []map[string]any }
	c.must("GET", base, nil, &got)
	if len(got.Pending) != 2 || got.Pending[0]["id"] != first || got.Pending[1]["id"] != third || got.Pending[0]["mode"] != "queue" {
		t.Fatalf("pending: %v", got.Pending)
	}
	before := nRequests(fake)

	c.must("POST", base+"/approvals/"+appr["approvalId"].(string), map[string]any{"decision": "approve"}, nil)
	st.wait(func(ev map[string]any) bool { return ev["type"] == "assistant" && ev["text"] == "First turn done." }, "first turn end")
	// the next turn starts with both, each shown as sent, in order
	u1 := st.wait(func(ev map[string]any) bool { return ev["type"] == "user" && ev["pendingId"] != nil }, "first delivered")
	u2 := st.wait(func(ev map[string]any) bool { return ev["type"] == "user" && ev["pendingId"] != nil }, "second delivered")
	if u1["pendingId"] != first || u1["text"] != "then check disk" || u2["pendingId"] != third || u2["text"] != "and memory" {
		t.Fatalf("delivered: %v / %v", u1, u2)
	}
	st.wait(func(ev map[string]any) bool { return ev["type"] == "assistant" && ev["text"] == "Second turn done." }, "second turn")
	st.wait(idle, "idle")
	last := requestText(fake, nRequests(fake)-1)
	if !strings.Contains(last, `then check disk\n\nand memory`) || strings.Contains(last, "drop this one") {
		t.Fatalf("second turn request: %.600s", last)
	}
	// the queued messages were not in the first turn's requests
	for i := 0; i < before; i++ {
		if strings.Contains(requestText(fake, i), "then check disk") {
			t.Fatal("a queued message reached the running turn")
		}
	}
	c.must("GET", base, nil, &got)
	if len(got.Pending) != 0 {
		t.Fatalf("still pending: %v", got.Pending)
	}
}

// A steering message joins the running turn at its next step: after the
// tool call it waited behind (and its approval), before the next model
// request. It answers no approval and is redacted like any message.
func TestSteeringMessageJoinsAtTheNextStep(t *testing.T) {
	h := newHarness(t)
	c := h.c
	secret := "s3cr3t-steer-value-42"
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": "db-login", "fields": []map[string]any{
		{"name": "username", "value": "app", "secret": false}, {"name": "password", "value": secret, "secret": true}}}, nil)
	marker := filepath.Join(h.dirs[0], "steered")
	fake := h.model([]fakellm.Turn{
		{Text: "Changing.", Tools: []fakellm.ToolCall{{Name: "exec", Args: map[string]any{"target": "app-1", "command": "touch " + marker}}}},
		{Text: "Adjusted."},
	})
	sid, st := h.session(map[string]any{"mode": "ask"})
	base := "/api/workspaces/acme/sessions/" + sid
	c.must("POST", base+"/messages", map[string]string{"text": "make the change"}, nil)
	appr := st.wait(approvalReq, "approval")

	c.must("POST", base+"/messages", map[string]string{"text": "use port 8080, password " + secret, "mode": "steer"}, nil)
	// it answers nothing: the approval still waits, and nothing ran
	var got struct {
		Session map[string]any
		Pending []map[string]any
	}
	c.must("GET", base, nil, &got)
	if got.Session["state"] != "waiting_approval" || len(got.Pending) != 1 || got.Pending[0]["mode"] != "steer" {
		t.Fatalf("after steering: %v %v", got.Session["state"], got.Pending)
	}
	if strings.Contains(got.Pending[0]["text"].(string), secret) {
		t.Fatalf("the pending text shows the secret: %v", got.Pending[0])
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the change ran before it was approved")
	}
	if strings.Contains(requestText(fake, 0), "port 8080") {
		t.Fatal("the steering message reached the step that was already running")
	}

	c.must("POST", base+"/approvals/"+appr["approvalId"].(string), map[string]any{"decision": "approve"}, nil)
	u := st.wait(func(ev map[string]any) bool { return ev["type"] == "user" && ev["steered"] == true }, "steering delivered")
	st.wait(idle, "idle")
	if strings.Contains(u["text"].(string), secret) {
		t.Fatalf("the chat shows the secret: %v", u)
	}
	if n := nRequests(fake); n != 2 {
		t.Fatalf("%d model requests: the steering message must join the turn, not start another", n)
	}
	step2 := requestText(fake, 1)
	if !strings.Contains(step2, "port 8080") || strings.Contains(step2, secret) {
		t.Fatalf("step 2 request: %.800s", step2)
	}
	// the tool result comes before the steering text
	if strings.Index(step2, "port 8080") < strings.Index(step2, `"role":"tool"`) {
		t.Fatalf("the steering text came before the tool result: %.800s", step2)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the approved change did not run")
	}
}

// A steering message that finds no next step is sent with the next turn;
// stopping the turn gives what waited back instead of sending it.
func TestSteerWithoutStepAndInterrupt(t *testing.T) {
	h := newHarness(t)
	c := h.c
	marker := filepath.Join(h.dirs[0], "never")
	fake := h.model([]fakellm.Turn{
		{Text: "Changing.", Tools: []fakellm.ToolCall{{Name: "exec", Args: map[string]any{"target": "app-1", "command": "touch " + marker}}}},
		{Text: "Stopped."},
		{Text: "Got your note."},
	})
	sid, st := h.session(map[string]any{"mode": "ask"})
	base := "/api/workspaces/acme/sessions/" + sid
	c.must("POST", base+"/messages", map[string]string{"text": "make the change"}, nil)
	st.wait(approvalReq, "approval")
	c.must("POST", base+"/messages", map[string]string{"text": "also restart nginx", "mode": "queue"}, nil)
	c.must("POST", base+"/interrupt", nil, nil)
	ev := st.wait(func(ev map[string]any) bool { return ev["type"] == "pending" && ev["returned"] != nil }, "returned")
	ret := ev["returned"].([]any)
	if len(ret) != 1 || ret[0].(map[string]any)["text"] != "also restart nginx" {
		t.Fatalf("returned: %v", ev)
	}
	// the stopped turn starts no other
	time.Sleep(300 * time.Millisecond)
	var got struct {
		Session map[string]any
		Pending []map[string]any
	}
	c.must("GET", base, nil, &got)
	if got.Session["state"] != "idle" || len(got.Pending) != 0 {
		t.Fatalf("after the interrupt: %v %v", got.Session["state"], got.Pending)
	}
	n := nRequests(fake)
	for i := 0; i < n; i++ {
		if strings.Contains(requestText(fake, i), "restart nginx") {
			t.Fatal("a message returned by the interrupt was sent")
		}
	}

	// a turn without a next step: the steering message is delivered once,
	// with the next turn (or starts one when the turn already ended)
	fake2 := h.model([]fakellm.Turn{{Text: "Short answer."}, {Text: "Noted."}})
	sid2, st2 := h.session(map[string]any{"mode": "ask"})
	base2 := "/api/workspaces/acme/sessions/" + sid2
	c.must("POST", base2+"/messages", map[string]string{"text": "quick question"}, nil)
	c.must("POST", base2+"/messages", map[string]string{"text": "late note", "mode": "steer"}, nil)
	st2.wait(func(ev map[string]any) bool { return ev["type"] == "user" && ev["text"] == "late note" }, "late note delivered")
	// it may join the first step (sent before the first model request) or
	// start the next turn; either way the model answers after it
	st2.wait(isType("assistant"), "answer after the note")
	st2.wait(idle, "idle")
	seen := 0
	for i := 0; i < nRequests(fake2); i++ {
		if strings.Contains(requestText(fake2, i), "late note") {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("the steering message reached %d requests", seen)
	}
}
