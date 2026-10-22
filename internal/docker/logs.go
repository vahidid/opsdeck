package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/workspace"
)

// LogSource says whose logs to stream.
type LogSource struct {
	Kind    string // container | compose | service | events
	Target  string // container, compose project or swarm service
	Service string // compose: one service of the project (optional)
	Tail    int    // lines of history per container; <0 = all
	Since   string // duration (5m, 2h, 1d) or RFC3339 time
	Follow  bool
}

var (
	durRe  = regexp.MustCompile(`^([0-9]{1,6})([smhd])$`)
	timeRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+(Z|[+-][0-9]{2}:[0-9]{2})?$`)
)

// sinceArg validates --since: a duration (d is turned into hours) or a time.
func sinceArg(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if m := durRe.FindStringSubmatch(s); m != nil {
		if m[2] == "d" {
			n, _ := strconv.Atoi(m[1])
			return " --since " + strconv.Itoa(n*24) + "h", nil
		}
		return " --since " + s, nil
	}
	if timeRe.MatchString(s) {
		return " --since " + s, nil
	}
	return "", fmt.Errorf("invalid since %q (e.g. 15m, 2h, 1d or an RFC3339 time)", s)
}

// LogsCommand builds the shell command that streams logs. Container logs
// keep stdout and stderr apart (the caller reads them separately).
func (c *Client) LogsCommand(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, src LogSource) (string, error) {
	p, err := c.Prefix(ctx, w, host)
	if err != nil {
		return "", err
	}
	since, err := sinceArg(src.Since)
	if err != nil {
		return "", err
	}
	tail := " --tail all"
	if src.Tail >= 0 {
		tail = " --tail " + strconv.Itoa(src.Tail)
	}
	follow := ""
	if src.Follow {
		follow = " -f"
	}
	switch src.Kind {
	case "container", "":
		if !ValidID(src.Target) {
			return "", fmt.Errorf("invalid container")
		}
		return p + "logs --timestamps" + tail + since + follow + " " + src.Target, nil
	case "compose":
		if !ValidID(src.Target) {
			return "", fmt.Errorf("invalid compose project")
		}
		cmd := p + "compose --ansi never -p " + src.Target + " logs --timestamps --no-color" + tail + since + follow
		if src.Service != "" {
			if !ValidID(src.Service) {
				return "", fmt.Errorf("invalid service")
			}
			cmd += " " + src.Service
		}
		return cmd + " 2>&1", nil
	case "service":
		if !ValidID(src.Target) {
			return "", fmt.Errorf("invalid service")
		}
		return p + "service logs --timestamps --no-trunc" + tail + since + follow + " " + src.Target, nil
	case "events":
		cmd := p + "events --format '{{json .}}'" + since
		if !src.Follow {
			cmd += " --until " + strconv.FormatInt(time.Now().Unix(), 10)
		}
		return cmd, nil
	}
	return "", fmt.Errorf("unknown log source %q", src.Kind)
}

// LogLine is a parsed log line.
type LogLine struct {
	TS     string `json:"ts,omitempty"`     // RFC3339Nano from docker
	Src    string `json:"src,omitempty"`    // compose service / swarm task
	Line   string `json:"line"`             // the message
	Stderr bool   `json:"stderr,omitempty"` // written to standard error
}

func splitTS(s string) (ts, rest string) {
	if len(s) > 20 && s[4] == '-' && s[10] == 'T' {
		if i := strings.IndexByte(s, ' '); i > 0 {
			return s[:i], s[i+1:]
		}
		return s, ""
	}
	return "", s
}

// ParseLogLine splits docker's prefixes (time, compose service, swarm task)
// off a line of LogsCommand output.
func ParseLogLine(kind, raw string, stderr bool) LogLine {
	l := LogLine{Stderr: stderr}
	switch kind {
	case "compose":
		// "api-1  | 2026-10-22T17:28:42.26Z message"
		if i := strings.Index(raw, " | "); i > 0 {
			if pre := strings.TrimSpace(raw[:i]); pre != "" && !strings.ContainsAny(pre, " \t") {
				l.Src = pre
				l.TS, l.Line = splitTS(raw[i+3:])
				return l
			}
		}
		l.TS, l.Line = splitTS(raw)
	case "service":
		// "2026-10-22T17:28:40.57Z web.2.p17xh5235462@vm    | message"
		ts, rest := splitTS(raw)
		l.TS = ts
		if i := strings.Index(rest, " | "); i > 0 {
			l.Src = shortTask(strings.TrimSpace(rest[:i]))
			l.Line = rest[i+3:]
			return l
		}
		l.Line = rest
	default:
		l.TS, l.Line = splitTS(raw)
	}
	return l
}

// shortTask turns web.2.p17xh5235462@vm into web.2@vm.
func shortTask(s string) string {
	name, node, _ := strings.Cut(s, "@")
	parts := strings.Split(name, ".")
	if len(parts) >= 3 && len(parts[len(parts)-1]) >= 12 {
		name = strings.Join(parts[:len(parts)-1], ".")
	}
	if node != "" {
		return name + "@" + node
	}
	return name
}

// Event is a docker event.
type Event struct {
	Time   string            `json:"time"`
	Type   string            `json:"type"`
	Action string            `json:"action"`
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Scope  string            `json:"scope,omitempty"`
	Attrs  map[string]string `json:"attrs,omitempty"`
}

// ParseEvent parses a line of docker events --format '{{json .}}'.
func ParseEvent(line string) (Event, bool) {
	var raw struct {
		Type, Action, Scope string
		Actor               struct {
			ID         string
			Attributes map[string]string
		}
		TimeNano int64 `json:"timeNano"`
	}
	if json.Unmarshal([]byte(line), &raw) != nil || raw.Type == "" {
		return Event{}, false
	}
	e := Event{Type: raw.Type, Action: raw.Action, ID: raw.Actor.ID, Scope: raw.Scope, Attrs: raw.Actor.Attributes,
		Time: time.Unix(0, raw.TimeNano).UTC().Format(time.RFC3339Nano)}
	e.Name = raw.Actor.Attributes["name"]
	if len(e.ID) > 12 && raw.Type != "image" {
		e.ID = e.ID[:12]
	}
	return e, true
}
