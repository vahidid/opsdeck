package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

func TestWaitForTCPAndHTTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens yet
	go func() {
		time.Sleep(1200 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err == nil {
			defer l.Close()
			time.Sleep(3 * time.Second)
		}
	}()
	env := &Env{WS: &workspace.Workspace{}}
	c := prepare(t, toolWaitFor(), env, map[string]any{"condition": "tcp", "target": addr, "interval_sec": 1, "timeout_sec": 10})
	var progress []string
	sc := &callScope{progress: func(s string) { progress = append(progress, s) }}
	out, err := c.Run(withCallScope(context.Background(), sc))
	if err != nil || !strings.Contains(out, "accepts connections") || len(progress) == 0 {
		t.Fatalf("tcp: %q %v %v", out, err, progress)
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			http.Error(w, "starting", 503)
			return
		}
		fmt.Fprint(w, `{"status":"UP"}`)
	}))
	defer srv.Close()
	c = prepare(t, toolWaitFor(), env, map[string]any{"condition": "http", "target": srv.URL + "/health", "match": `"UP"`, "interval_sec": 1, "timeout_sec": 10})
	if c.Risk != RiskRead {
		t.Fatalf("risk %s", c.Risk)
	}
	out, err = c.Run(context.Background())
	if err != nil || !strings.Contains(out, "(3 checks)") || !strings.Contains(out, "200 OK") {
		t.Fatalf("http: %q %v", out, err)
	}
	c = prepare(t, toolWaitFor(), env, map[string]any{"condition": "http", "target": srv.URL, "status": 418, "interval_sec": 1, "timeout_sec": 2})
	if _, err := c.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "not met after") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestRolloutDone(t *testing.T) {
	obj := func(gen, observed, replicas, updated, available, total float64) map[string]any {
		return map[string]any{"metadata": map[string]any{"generation": gen}, "spec": map[string]any{"replicas": replicas},
			"status": map[string]any{"observedGeneration": observed, "updatedReplicas": updated, "availableReplicas": available, "replicas": total}}
	}
	for _, tc := range []struct {
		o    map[string]any
		done bool
	}{
		{obj(2, 1, 3, 3, 3, 3), false}, // controller has not seen it
		{obj(2, 2, 3, 1, 3, 4), false}, // rolling
		{obj(2, 2, 3, 3, 3, 3), true},
	} {
		if done, got := rolloutDone("deployment", tc.o); done != tc.done {
			t.Errorf("%v: done=%v (%s)", tc.o["status"], done, got)
		}
	}
}

func TestAskUserAndTodo(t *testing.T) {
	r, s := testRunner(t, ModeAsk, toolAskUser(), toolTodo())
	ch, unsub := s.Subscribe()
	defer unsub()
	go func() {
		for ev := range ch {
			if ev["type"] != "question" {
				continue
			}
			id := ev["questionId"].(string)
			if err := s.Answer(id, Answer{Choices: []string{"nope"}}); err == nil {
				t.Error("an answer outside the options was accepted")
			}
			if err := s.Answer(id, Answer{}); err == nil {
				t.Error("an empty answer was accepted")
			}
			if s.Info().State != "waiting_input" {
				t.Errorf("state %s", s.Info().State)
			}
			s.Answer(id, Answer{Choices: []string{"app-2 first"}, Text: "after 18:00"})
		}
	}()
	in, _ := json.Marshal(map[string]any{"question": "Which host first?", "options": []map[string]string{{"label": "app-1 first"}, {"label": "app-2 first"}}})
	res := r.runCalls(context.Background(), []llm.Block{{Type: "tool_use", ID: "q1", Name: "ask_user", Input: in}}, false)
	if res[0].IsError || res[0].Content != "The user chose: app-2 first\nand added: after 18:00" {
		t.Fatalf("answer: %q", res[0].Content)
	}

	in, _ = json.Marshal(map[string]any{"items": []map[string]string{{"text": "drain app-1", "status": "done"}, {"text": "upgrade", "status": "in_progress"}, {"text": "verify", "status": "pending"}}})
	events := s.Events()
	res = r.runCalls(context.Background(), []llm.Block{{Type: "tool_use", ID: "t1", Name: "todo", Input: in}}, false)
	if !strings.Contains(res[0].Content, "1 of 3 finished") || !strings.Contains(res[0].Content, "[>] upgrade") {
		t.Fatalf("todo: %q", res[0].Content)
	}
	found := false
	for _, ev := range s.Events()[len(events):] {
		if ev["type"] == "todo" {
			items := ev["items"].([]TodoItem)
			found = len(items) == 3 && items[1].Status == "in_progress"
		}
	}
	if !found {
		t.Fatal("no todo event")
	}

	s.info.Headless = true
	in, _ = json.Marshal(map[string]any{"question": "Restart now?"})
	res = r.runCalls(context.Background(), []llm.Block{{Type: "tool_use", ID: "q2", Name: "ask_user", Input: in}}, false)
	if !res[0].IsError || !strings.Contains(res[0].Content, "headless") {
		t.Fatalf("headless question: %q", res[0].Content)
	}
}
