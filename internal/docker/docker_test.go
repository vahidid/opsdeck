package docker

import (
	"strings"
	"testing"
)

func TestParseLogLine(t *testing.T) {
	cases := []struct {
		kind, raw string
		want      LogLine
	}{
		{"container", "2026-10-22T17:28:42.260985106Z level=info msg=hi", LogLine{TS: "2026-10-22T17:28:42.260985106Z", Line: "level=info msg=hi"}},
		{"container", "no timestamp here", LogLine{Line: "no timestamp here"}},
		{"compose", "api-1     | 2026-10-22T17:28:43.263592661Z level=info msg=\"request 11\"", LogLine{Src: "api-1", TS: "2026-10-22T17:28:43.263592661Z", Line: "level=info msg=\"request 11\""}},
		{"compose", "worker-1  | 2026-10-22T17:28:39.514Z a | b", LogLine{Src: "worker-1", TS: "2026-10-22T17:28:39.514Z", Line: "a | b"}},
		{"service", "2026-10-22T17:28:40.575368720Z web.2.p17xh5235462xyz@vm    | tick 13", LogLine{TS: "2026-10-22T17:28:40.575368720Z", Src: "web.2@vm", Line: "tick 13"}},
	}
	for _, c := range cases {
		if got := ParseLogLine(c.kind, c.raw, false); got != c.want {
			t.Errorf("%s %q:\n got %+v\nwant %+v", c.kind, c.raw, got, c.want)
		}
	}
	if !ParseLogLine("container", "2026-10-22T17:28:42Z boom", true).Stderr {
		t.Error("stderr flag lost")
	}
}

func TestSinceAndCommands(t *testing.T) {
	for in, want := range map[string]string{"": "", "15m": " --since 15m", "2d": " --since 48h", "2026-10-22T17:28:42.26Z": " --since 2026-10-22T17:28:42.26Z"} {
		if got, err := sinceArg(in); err != nil || got != want {
			t.Errorf("since %q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"1y", "now; rm -rf /", "$(id)", "15 m"} {
		if _, err := sinceArg(bad); err == nil {
			t.Errorf("since %q accepted", bad)
		}
	}
	if !ValidRef("ghcr.io/acme/shop:1.2.3@sha256:abc") || ValidRef("x; rm -rf /") || ValidRef("../etc") || ValidID("a b") {
		t.Error("reference validation")
	}
}

func TestCleanRel(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "a/b": "a/b", "/a/../../etc/passwd": "etc/passwd", "a/./b/": "a/b"} {
		if got, err := cleanRel(in); err != nil || got != want {
			t.Errorf("cleanRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "x\x00"} {
		if validName(bad) == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

func TestParseEventAndTop(t *testing.T) {
	e, ok := ParseEvent(`{"Type":"container","Action":"start","Actor":{"ID":"5b131a8f758d1234567890","Attributes":{"name":"shop-api-1","image":"busybox"}},"scope":"local","time":1792690094,"timeNano":1792690094387943690}`)
	if !ok || e.Type != "container" || e.Action != "start" || e.Name != "shop-api-1" || e.ID != "5b131a8f758d" || !strings.HasPrefix(e.Time, "2026-") {
		t.Fatalf("event: %+v", e)
	}
	if _, ok := ParseEvent("not json"); ok {
		t.Fatal("garbage parsed")
	}
	ps := parseTopDefault("PID   USER     TIME  COMMAND\n1 root      0:00 sh -c sleep 1000\n7 root 0:00 sleep 1000\n")
	if len(ps) != 2 || ps[0].PID != "1" || ps[0].User != "root" || ps[0].Command != "sh -c sleep 1000" {
		t.Fatalf("top: %+v", ps)
	}
	if shortTask("blog_echo.1.mo73p42ja57inizm5u2jiltzu@vm") != "blog_echo.1@vm" {
		t.Fatal("shortTask")
	}
}
