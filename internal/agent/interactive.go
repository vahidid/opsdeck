package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"opsdeck/internal/kube"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// ---------------------------------------------------------------------------
// wait_for

func toolWaitFor() Tool {
	return Tool{
		Def: llm.Tool{Name: "wait_for", Description: "Wait until something is true instead of sleeping and retrying by hand: condition=tcp (a port accepts connections), http (a URL answers with an expected status and, optionally, a body matching a pattern), command (a command on a host exits with the expected code and, optionally, prints a match — e.g. a log line appears), rollout (a Kubernetes deployment, statefulset or daemonset finished rolling out). It checks every interval_sec until timeout_sec and shows progress in the chat.",
			Schema: obj(map[string]any{
				"condition":    enum("What to wait for", "tcp", "http", "command", "rollout"),
				"target":       str("tcp: host:port or a workspace resource (host/app-1); http: a URL or a panel"),
				"from":         str("tcp/http: optional host to check from"),
				"host":         str("command: the host to run it on"),
				"command":      str("command: shell command to run each time, e.g. systemctl is-active nginx or journalctl -u app -n 50 --no-pager"),
				"match":        str("Optional regular expression the HTTP body or command output must contain"),
				"status":       num("http: expected status code (default: any below 400)"),
				"exit_code":    num("command: expected exit code (default 0)"),
				"cluster":      str("rollout: KubernetesCluster resource name"),
				"namespace":    str("rollout: namespace (default the cluster's)"),
				"kind":         enum("rollout: workload kind (default deployment)", "deployment", "statefulset", "daemonset"),
				"name":         str("rollout: workload name"),
				"interval_sec": num("Seconds between checks (default 5)"),
				"timeout_sec":  num("Give up after this many seconds (default 300, max 1800)"),
			}, "condition")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Condition, Target, From, Host, Command, Match, Cluster, Namespace, Kind, Name string
				Status                                                                        int
				ExitCode                                                                      *int `json:"exit_code"`
				IntervalSec                                                                   int  `json:"interval_sec"`
				TimeoutSec                                                                    int  `json:"timeout_sec"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			interval := time.Duration(max(1, min(firstPositive(in.IntervalSec, 5), 300))) * time.Second
			timeout := time.Duration(max(1, min(firstPositive(in.TimeoutSec, 300), 1800))) * time.Second
			var re *regexp.Regexp
			if in.Match != "" {
				var err error
				if re, err = regexp.Compile(in.Match); err != nil {
					return nil, fmt.Errorf("invalid match pattern: %v", err)
				}
			}
			var check func(ctx context.Context) (bool, string)
			c := &Call{Risk: RiskRead}
			switch in.Condition {
			case "tcp", "http":
				var from *workspace.Resource
				if in.From != "" {
					h, err := env.host(in.From)
					if err != nil {
						return nil, err
					}
					from = h
				}
				res := env.WS.FindTarget(in.Target)
				if res != nil && !strings.Contains(in.Target, "/") && res.Kind != "Host" {
					res = nil
				}
				rs := []*workspace.Resource{}
				if from != nil {
					rs = append(rs, from)
				}
				if res != nil {
					rs = append(rs, res)
				}
				c.Environment, c.Protected = targetInfo(env.WS, rs...)
				c.Target, c.Match, c.Credentials = "net/"+in.Target, in.Condition+" "+in.Target, env.creds(rs...)
				if in.Condition == "tcp" {
					addr := in.Target
					if res != nil {
						switch res.Kind {
						case "Host":
							addr = net.JoinHostPort(res.Str("address"), strconv.Itoa(res.Int("port", 22)))
						case "Database":
							addr = res.Str("address")
						}
					}
					check = func(ctx context.Context) (bool, string) {
						dial, err := checkDialer(ctx, env, from, res)
						if err != nil {
							return false, err.Error()
						}
						dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
						defer cancel()
						conn, err := dial(dctx, "tcp", addr)
						if err != nil {
							return false, "tcp " + addr + ": " + err.Error()
						}
						conn.Close()
						return true, "tcp " + addr + " accepts connections"
					}
				} else {
					url := in.Target
					if res != nil && res.Str("url") != "" {
						url = res.Str("url")
					}
					if !strings.Contains(url, "://") {
						url = "http://" + url
					}
					check = func(ctx context.Context) (bool, string) {
						dial, err := checkDialer(ctx, env, from, res)
						if err != nil {
							return false, err.Error()
						}
						hc := &http.Client{Timeout: 15 * time.Second,
							Transport: &http.Transport{DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { return dial(ctx, n, a) },
								TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
							CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
						req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
						if err != nil {
							return false, err.Error()
						}
						resp, err := hc.Do(req)
						if err != nil {
							return false, "GET " + url + ": " + err.Error()
						}
						defer resp.Body.Close()
						body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
						got := fmt.Sprintf("GET %s → %s", url, resp.Status)
						ok := resp.StatusCode < 400
						if in.Status > 0 {
							ok = resp.StatusCode == in.Status
						}
						if ok && re != nil && !re.Match(body) {
							return false, got + ", body does not match " + in.Match
						}
						return ok, got
					}
				}
			case "command":
				h, err := env.host(in.Host)
				if err != nil {
					return nil, err
				}
				if strings.TrimSpace(in.Command) == "" {
					return nil, errors.New("command is empty")
				}
				an := AnalyzeCommand(in.Command)
				c.Risk, c.Reasons, c.Shell, c.Commands = an.Risk, an.Reasons, true, an.Parts
				c.Environment, c.Protected = targetInfo(env.WS, h)
				c.Target, c.Match, c.Credentials = "host/"+h.Name, in.Command, env.creds(h)
				c.Preview = "$ " + in.Command + "\n\non: " + h.Name + fmt.Sprintf("\nevery %s, for up to %s", interval, timeout)
				want := 0
				if in.ExitCode != nil {
					want = *in.ExitCode
				}
				check = func(ctx context.Context) (bool, string) {
					cctx, cancel := context.WithTimeout(ctx, max(30*time.Second, interval))
					defer cancel()
					res, err := env.Hub.Exec(cctx, env.WS, h, in.Command, nil, 64<<10)
					if err != nil && res.DurationMs == 0 {
						return false, err.Error()
					}
					out := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
					last := out
					if i := strings.LastIndex(out, "\n"); i >= 0 {
						last = out[i+1:]
					}
					got := fmt.Sprintf("exit %d: %s", res.ExitCode, oneLine(last, 160))
					if res.ExitCode != want {
						return false, got
					}
					if re != nil {
						if m := re.FindString(out); m != "" {
							return true, fmt.Sprintf("exit %d, matched %q", res.ExitCode, oneLine(m, 200))
						}
						return false, got + " (no match yet)"
					}
					return true, got
				}
			case "rollout":
				cl := env.WS.Find("KubernetesCluster", strings.TrimPrefix(in.Cluster, "cluster/"))
				if cl == nil {
					return nil, fmt.Errorf("cluster %q not found", in.Cluster)
				}
				kind := firstNonEmpty(in.Kind, "deployment")
				if in.Name == "" {
					return nil, errors.New("rollout needs the workload name")
				}
				c.Environment, c.Protected = targetInfo(env.WS, cl)
				c.Target, c.Match = "cluster/"+cl.Name, "rollout "+kind+"/"+in.Name
				check = func(ctx context.Context) (bool, string) {
					conn, err := env.Kube.Conn(ctx, env.WS, cl)
					if err != nil {
						return false, err.Error()
					}
					rt, err := conn.Resolve(ctx, kind)
					if err != nil {
						return false, err.Error()
					}
					o, err := conn.Get(ctx, rt, firstNonEmpty(in.Namespace, conn.NS), in.Name)
					if err != nil {
						return false, err.Error()
					}
					return rolloutDone(kind, o)
				}
			default:
				return nil, fmt.Errorf("unknown condition %q (tcp, http, command or rollout)", in.Condition)
			}
			c.Run = func(ctx context.Context) (string, error) {
				return waitLoop(ctx, check, interval, timeout)
			}
			return c, nil
		},
	}
}

func firstPositive(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// waitLoop checks until the condition holds or the time is up.
func waitLoop(ctx context.Context, check func(context.Context) (bool, string), interval, timeout time.Duration) (string, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	for n := 1; ; n++ {
		ok, got := check(ctx)
		took := time.Since(start).Round(time.Second)
		if ok {
			return fmt.Sprintf("condition met after %s (%d checks): %s", took, n, got), nil
		}
		reportProgress(ctx, "check %d (%s): %s", n, took, got)
		if time.Now().Add(interval).After(deadline) {
			return "", fmt.Errorf("not met after %s (%d checks); last: %s", took, n, got)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("interrupted after %s (%d checks); last: %s", took, n, got)
		case <-time.After(interval):
		}
	}
}

// rolloutDone reads a workload's status like kubectl rollout status.
func rolloutDone(kind string, o kube.Object) (bool, string) {
	num := func(m map[string]any, k string) int64 {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		}
		return 0
	}
	md, _ := o["metadata"].(map[string]any)
	spec, _ := o["spec"].(map[string]any)
	st, _ := o["status"].(map[string]any)
	if md == nil || st == nil {
		return false, "no status yet"
	}
	if num(st, "observedGeneration") < num(md, "generation") {
		return false, "waiting for the controller to see the new generation"
	}
	switch kind {
	case "daemonset":
		want, upd, avail := num(st, "desiredNumberScheduled"), num(st, "updatedNumberScheduled"), num(st, "numberAvailable")
		got := fmt.Sprintf("%d of %d updated, %d available", upd, want, avail)
		return upd >= want && avail >= want, got
	case "statefulset":
		want := int64(1)
		if _, ok := spec["replicas"]; ok {
			want = num(spec, "replicas")
		}
		ready, upd := num(st, "readyReplicas"), num(st, "updatedReplicas")
		got := fmt.Sprintf("%d of %d updated, %d ready", upd, want, ready)
		if cur, up := st["currentRevision"], st["updateRevision"]; cur != nil && up != nil && cur != up {
			return false, got + ", revision " + fmt.Sprint(up) + " rolling out"
		}
		return ready >= want && upd >= want, got
	}
	want := int64(1)
	if _, ok := spec["replicas"]; ok {
		want = num(spec, "replicas")
	}
	upd, avail, total := num(st, "updatedReplicas"), num(st, "availableReplicas"), num(st, "replicas")
	got := fmt.Sprintf("%d of %d updated, %d available", upd, want, avail)
	if conds, ok := st["conditions"].([]any); ok {
		for _, c := range conds {
			if cm, ok := c.(map[string]any); ok && cm["type"] == "Progressing" && cm["reason"] == "ProgressDeadlineExceeded" {
				return false, got + ", progress deadline exceeded: " + fmt.Sprint(cm["message"])
			}
		}
	}
	return upd >= want && avail >= want && total <= want, got
}

// ---------------------------------------------------------------------------
// ask_user

// Question is something the agent asks the user, with options to pick.
type Question struct {
	Text      string           `json:"question"`
	Options   []QuestionOption `json:"options"`
	Multiple  bool             `json:"multiple"`
	AllowText bool             `json:"allowText"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type askKey struct{}

// askFunc asks the user a question and waits for the answer.
type askFunc func(ctx context.Context, q Question) (Answer, error)

func withAsk(ctx context.Context, f askFunc) context.Context { return context.WithValue(ctx, askKey{}, f) }

func toolAskUser() Tool {
	return Tool{
		Def: llm.Tool{Name: "ask_user", Description: "Ask the user a question when the decision is theirs (which of several options, a maintenance window, whether to restart now, which host to start with) and wait for the answer. Offer 2–6 short options; the user can also type their own answer. Do not use it to ask for approval of a change (changes ask on their own) or for passwords (never ask for those).",
			Schema: obj(map[string]any{
				"question": str("The question, in the user's language"),
				"options": map[string]any{"type": "array", "description": "Options to pick from", "items": obj(map[string]any{
					"label":       str("Short option text"),
					"description": str("Optional: what choosing it means"),
				}, "label")},
				"multiple":   map[string]any{"type": "boolean", "description": "The user may pick several options"},
				"allow_text": map[string]any{"type": "boolean", "description": "Let the user type an answer of their own (default true)"},
			}, "question")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Question  string
				Options   []QuestionOption
				Multiple  bool
				AllowText *bool `json:"allow_text"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Question) == "" {
				return nil, errors.New("question is empty")
			}
			if len(in.Options) > 8 {
				in.Options = in.Options[:8]
			}
			q := Question{Text: strings.TrimSpace(in.Question), Options: in.Options, Multiple: in.Multiple, AllowText: in.AllowText == nil || *in.AllowText || len(in.Options) == 0}
			return &Call{Risk: RiskRead, Target: "user", Scope: ScopeLocal, Serial: true, Match: q.Text, Run: func(ctx context.Context) (string, error) {
				ask, _ := ctx.Value(askKey{}).(askFunc)
				if ask == nil {
					return "", errors.New("nobody can answer questions here; decide yourself and say what you assumed")
				}
				a, err := ask(ctx, q)
				if err != nil {
					return "", err
				}
				switch {
				case a.Skipped:
					return "The user skipped the question: decide yourself, or stop if it matters, and say what you assumed.", nil
				case len(a.Choices) > 0 && a.Text != "":
					return "The user chose: " + strings.Join(a.Choices, "; ") + "\nand added: " + a.Text, nil
				case len(a.Choices) > 0:
					return "The user chose: " + strings.Join(a.Choices, "; "), nil
				}
				return "The user answered: " + a.Text, nil
			}}, nil
		},
	}
}

// ask shows a question card and waits for the user's answer.
func (r *runner) ask(ctx context.Context, callID string, q Question) (Answer, error) {
	s := r.s
	if s.Info().Headless {
		return Answer{}, errors.New("headless run: nobody can answer; decide yourself and say what you assumed")
	}
	id := uuid.NewString()[:8]
	pq := &pendingQuestion{ch: make(chan Answer, 1)}
	for _, o := range q.Options {
		pq.options = append(pq.options, o.Label)
	}
	s.mu.Lock()
	s.questions[id] = pq
	s.mu.Unlock()
	s.refreshState()
	r.emit("question", map[string]any{"questionId": id, "callId": callID, "question": q.Text, "options": q.Options, "multiple": q.Multiple, "allowText": q.AllowText}, true)
	s.save()
	var a Answer
	var answered bool
	select {
	case a, answered = <-pq.ch:
	case <-ctx.Done():
	}
	s.mu.Lock()
	delete(s.questions, id)
	s.mu.Unlock()
	if !answered {
		return Answer{}, errors.New("interrupted before the user answered")
	}
	r.emit("question_answered", map[string]any{"questionId": id, "choices": a.Choices, "text": r.m.redactor.Redact(a.Text), "skipped": a.Skipped}, true)
	s.refreshState()
	return a, nil
}

// Answer answers a question of the agent.
func (s *Session) Answer(questionID string, a Answer) error {
	s.mu.Lock()
	q := s.questions[questionID]
	if q != nil {
		delete(s.questions, questionID)
	}
	s.mu.Unlock()
	if q == nil {
		return errors.New("no such question")
	}
	a.Text = strings.TrimSpace(a.Text)
	if !a.Skipped && len(a.Choices) == 0 && a.Text == "" {
		s.mu.Lock()
		s.questions[questionID] = q
		s.mu.Unlock()
		return errors.New("pick an option, type an answer, or skip")
	}
	for _, c := range a.Choices {
		found := false
		for _, o := range q.options {
			found = found || o == c
		}
		if !found {
			s.mu.Lock()
			s.questions[questionID] = q
			s.mu.Unlock()
			return fmt.Errorf("%q is not one of the options", c)
		}
	}
	q.ch <- a
	return nil
}

// ---------------------------------------------------------------------------
// todo

// TodoItem is one step of the agent's plan.
type TodoItem struct {
	Text   string `json:"text"`
	Status string `json:"status"` // pending | in_progress | done | skipped
}

type todoKey struct{}

func withTodo(ctx context.Context, f func([]TodoItem)) context.Context {
	return context.WithValue(ctx, todoKey{}, f)
}

func toolTodo() Tool {
	return Tool{
		Def: llm.Tool{Name: "todo", Description: "Keep a visible checklist for work with several steps (a deploy, a migration, an audit of many hosts): send the whole list each time with each item's status — pending, in_progress (one at a time), done or skipped. The user sees it live above the chat. Update it as you go: mark a step in_progress when you start it and done when it is verified.",
			Schema: obj(map[string]any{
				"items": map[string]any{"type": "array", "description": "The whole list, in order", "items": obj(map[string]any{
					"text":   str("The step"),
					"status": enum("Its status", "pending", "in_progress", "done", "skipped"),
				}, "text", "status")},
			}, "items")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Items []TodoItem }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if len(in.Items) > 50 {
				return nil, errors.New("keep the list to 50 items or fewer")
			}
			active := 0
			for i := range in.Items {
				it := &in.Items[i]
				it.Text = strings.TrimSpace(it.Text)
				switch it.Status {
				case "pending", "in_progress", "done", "skipped":
				case "completed":
					it.Status = "done"
				default:
					return nil, fmt.Errorf("item %d: status must be pending, in_progress, done or skipped", i+1)
				}
				if it.Status == "in_progress" {
					active++
				}
				if it.Text == "" {
					return nil, fmt.Errorf("item %d has no text", i+1)
				}
			}
			return &Call{Risk: RiskRead, Target: "session", Scope: ScopeLocal, Run: func(ctx context.Context) (string, error) {
				if set, _ := ctx.Value(todoKey{}).(func([]TodoItem)); set != nil {
					set(in.Items)
				}
				var b strings.Builder
				done := 0
				for _, it := range in.Items {
					mark := map[string]string{"pending": "[ ]", "in_progress": "[>]", "done": "[x]", "skipped": "[-]"}[it.Status]
					if it.Status == "done" || it.Status == "skipped" {
						done++
					}
					fmt.Fprintf(&b, "%s %s\n", mark, it.Text)
				}
				head := fmt.Sprintf("Checklist updated (%d of %d finished)", done, len(in.Items))
				if active > 1 {
					head += "; keep one step in_progress at a time"
				}
				return head + ":\n" + b.String(), nil
			}}, nil
		},
	}
}
