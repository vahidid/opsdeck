package knowledge

import (
	"strings"
	"testing"
)

func TestSearch(t *testing.T) {
	ix := Build([]Entry{
		{Kind: KindSkill, Ref: "rotate-certs", Title: "rotate-certs", Summary: "Renew and rotate TLS certificates on nginx hosts", Text: "Use certbot renew, then reload nginx."},
		{Kind: KindDoc, Ref: "docs/runbooks/postgres-backup.md", Title: "Postgres backups", Text: "Backups run nightly with pg_dump to the NAS. Restore with pg_restore."},
		{Kind: KindMemory, Ref: "memory", Text: "- the NAS is reachable only over the office VPN"},
		{Kind: KindScript, Ref: "harden-ssh", Title: "harden ssh", Summary: "Disable password login and root login", Text: "new-host ubuntu"},
	})
	top := func(q, kind string) string {
		h := ix.Search(q, kind, 3)
		if len(h) == 0 {
			return ""
		}
		return h[0].Ref
	}
	cases := map[string]string{
		"renew the TLS certificate of the web servers": "rotate-certs",
		"how do we restore the database backups?":      "docs/runbooks/postgres-backup.md",
		"بکاپ دیتابیس رو بازیابی کن":                   "docs/runbooks/postgres-backup.md",
		"گواهی سرورها رو تمدید کن":                     "rotate-certs",
		"secure ssh on the new host":                   "harden-ssh",
	}
	for q, want := range cases {
		if got := top(q, ""); got != want {
			t.Errorf("%q: got %q, want %q", q, got, want)
		}
	}
	if got := top("NAS", KindMemory); got != "memory" {
		t.Errorf("kind filter: %q", got)
	}
	if h := ix.Search("the and of", "", 3); len(h) != 0 {
		t.Errorf("stop words matched: %v", h)
	}
	if h := ix.Search("pg_dump", "", 1); len(h) != 1 || h[0].Snippet == "" {
		t.Errorf("snippet: %+v", h)
	}
}

func TestTokens(t *testing.T) {
	got := strings.Join(Tokens("Deploying the Services to k8s clusters, سرور\u200cها prod"), " ")
	if want := "deploy servic k8s cluster سرورها prod"; got != want {
		t.Fatalf("tokens: %q, want %q", got, want)
	}
}
