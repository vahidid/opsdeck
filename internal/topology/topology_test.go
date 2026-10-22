package topology

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opsdeck/internal/workspace"
)

func ws(t *testing.T) *workspace.Workspace {
	m := workspace.NewManager(t.TempDir(), nil, nil)
	w, err := m.Create(workspace.Meta{Name: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestValidate(t *testing.T) {
	cases := map[string]Topology{
		"used by a zone and a node":  {Zones: []Zone{{ID: "x"}}, Nodes: []Node{{ID: "x", Type: "server"}}},
		`zone "nope" does not exist`: {Nodes: []Node{{ID: "a", Type: "server", Zone: "nope"}}},
		`node "b" does not exist`:    {Nodes: []Node{{ID: "a", Type: "server"}}, Links: []Link{{ID: "l", From: "a", To: "b"}}},
		"inside itself":              {Zones: []Zone{{ID: "z1", Parent: "z2"}, {ID: "z2", Parent: "z1"}}},
		"no type":                    {Nodes: []Node{{ID: "a"}}},
		"no spaces":                  {Nodes: []Node{{ID: "a b", Type: "server"}}},
	}
	for want, tp := range cases {
		tp.normalize()
		if err := tp.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
}

func TestSaveLoadRender(t *testing.T) {
	w := ws(t)
	tp, rev, err := Load(w)
	if err != nil || rev != "" || !tp.Empty() {
		t.Fatalf("empty load: %+v %q %v", tp, rev, err)
	}
	// the UI gets lists, never null
	if b, _ := json.Marshal(tp); string(b) != `{"version":1,"zones":[],"nodes":[],"links":[],"layout":{}}` {
		t.Fatalf("empty JSON: %s", b)
	}
	tp.Zones = []Zone{{ID: "dc", Label: "DC", Kind: "datacenter"}, {ID: "dmz", Label: "DMZ", Kind: "dmz", Parent: "dc", CIDR: "10.0.1.0/24"}}
	tp.Nodes = []Node{
		{ID: "cdn", Type: "cdn", Label: "Cloudflare", Vendor: "Cloudflare"},
		{ID: "lb", Type: "load-balancer", Variant: "L7 (HTTP)", Zone: "dmz", Vendor: "HAProxy", Tech: []string{"keepalived"}},
		{ID: "app", Type: "server", Zone: "dc", Deployments: []Deployment{{Name: "shop", Kind: "docker-compose", Image: "shop:1", Ports: []string{"8080"}, Repo: "git/deploy"}}},
	}
	tp.Links = []Link{{From: "cdn", To: "lb", Kind: "https", Port: "443", Encrypted: true}, {From: "lb", To: "app", Kind: "http", Port: "8080"}}
	tp.Layout = map[string]Box{"lb": {X: 1, Y: 2}, "gone": {X: 0, Y: 0}}
	rev1, err := Save(w, tp, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(w.Path, FileName))
	if !strings.HasPrefix(string(b), "# Topology of this workspace") || strings.Contains(string(b), "gone") {
		t.Fatalf("file:\n%s", b)
	}
	got, rev, _ := Load(w)
	if rev != rev1 || len(got.Links) != 2 || got.Links[0].ID != "cdn--lb" {
		t.Fatalf("reload %q %+v", rev, got.Links)
	}
	if _, err := Save(w, got, "stale"); err != ErrConflict {
		t.Fatalf("conflict: %v", err)
	}
	text := Render(got, nil)
	for _, want := range []string{
		"### Zone DC › DMZ [dmz] — DMZ, 10.0.1.0/24",
		"- [lb] lb: Load balancer, L7 (HTTP) — HAProxy; keepalived",
		"  - runs shop (Docker Compose, shop:1, ports 8080, from git/deploy)",
		"- cdn → lb: HTTPS (port 443, encrypted) [cdn--lb]",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("render misses %q:\n%s", want, text)
		}
	}
	// removing a zone keeps its contents
	got.RemoveZone("dmz")
	if got.Node("lb").Zone != "dc" || got.Validate() != nil {
		t.Fatalf("remove zone: %+v", got.Node("lb"))
	}
	got.RemoveNode("lb")
	if len(got.Links) != 0 {
		t.Fatalf("links of a removed node stay: %+v", got.Links)
	}
}

func TestImportInventory(t *testing.T) {
	w := ws(t)
	for _, r := range []*workspace.Resource{
		{Kind: "WireGuard", Name: "office", Spec: map[string]any{"config": "vault://acme/wg"}},
		{Kind: "Host", Name: "bastion", Spec: map[string]any{"address": "203.0.113.5"}},
		{Kind: "Host", Name: "app-1", Via: "host/bastion", Environment: "prod", Spec: map[string]any{"address": "10.0.0.5"}},
		{Kind: "Database", Name: "pg", Via: "network/office", Spec: map[string]any{"engine": "postgres", "address": "10.0.0.9:5432"}},
	} {
		if err := w.PutResource("", r); err != nil {
			t.Fatal(err)
		}
	}
	tp := &Topology{}
	tp.normalize()
	added := ImportInventory(tp, w)
	if len(added) != 4 {
		t.Fatalf("added %v", added)
	}
	if n := tp.Node("app-1"); n == nil || n.Type != "server" || n.Resource != "host/app-1" || n.Environment != "prod" {
		t.Fatalf("app-1: %+v", n)
	}
	if n := tp.Node("office"); n.Type != "vpn-gateway" {
		t.Fatalf("office: %+v", n)
	}
	kinds := map[string]string{}
	for _, l := range tp.Links {
		kinds[l.From+">"+l.To] = l.Kind
	}
	if kinds["bastion>app-1"] != "ssh" || kinds["office>pg"] != "wireguard" {
		t.Fatalf("links %v", kinds)
	}
	if again := ImportInventory(tp, w); len(again) != 0 {
		t.Fatalf("second import added %v", again)
	}
	if err := tp.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Render(tp, w.Resources), "inventory host/app-1") || strings.Contains(Render(tp, w.Resources), "not on the diagram") {
		t.Fatal(Render(tp, w.Resources))
	}
}
