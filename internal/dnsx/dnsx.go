// Package dnsx manages the DNS zones of a workspace's domains through DNS /
// CDN provider accounts connected once on this machine (config.DNSConnection,
// token in the vault): Cloudflare and ArvanCloud. Zones and records come in
// one provider-neutral shape, so the Domains page and the agent's dns tool
// work the same way whatever the provider.
package dnsx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/config"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// ProviderInfo describes a supported provider for the UI.
type ProviderInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	APIURL    string `json:"apiUrl"`
	TokenPage string `json:"tokenPage"` // where the user creates a token
	Dashboard string `json:"dashboard"`
	// TokenHelp says which permissions the token needs.
	TokenHelp        string `json:"tokenHelp"`
	TokenPlaceholder string `json:"tokenPlaceholder"`
	// Proxy is what the provider calls routing a record through its CDN.
	Proxy string `json:"proxy"`
	// TTLs the provider accepts (empty = any number of seconds).
	TTLs []int `json:"ttls,omitempty"`
	// Comments: records can carry a note.
	Comments bool `json:"comments"`
}

// Providers lists the supported providers.
var Providers = []ProviderInfo{
	{ID: "cloudflare", Label: "Cloudflare", APIURL: "https://api.cloudflare.com/client/v4",
		TokenPage: "https://dash.cloudflare.com/profile/api-tokens", Dashboard: "https://dash.cloudflare.com",
		TokenHelp:        "Create an API token with the permissions Zone → Zone: Read and Zone → DNS: Edit for the zones to manage (the “Edit zone DNS” template).",
		TokenPlaceholder: "Cloudflare API token", Proxy: "Proxied through Cloudflare", Comments: true},
	{ID: "arvancloud", Label: "ArvanCloud", APIURL: "https://napi.arvancloud.ir/cdn/4.0",
		TokenPage: "https://panel.arvancloud.ir/profile/api-keys", Dashboard: "https://panel.arvancloud.ir/cdn",
		TokenHelp:        "Create an API key (a machine user) with access to the CDN product: domains and DNS records.",
		TokenPlaceholder: "ArvanCloud API key", Proxy: "Through the ArvanCloud CDN (cloud)",
		TTLs: []int{120, 180, 300, 600, 900, 1800, 3600, 7200, 18000, 43200, 86400}},
}

// ProviderByID returns a provider's description.
func ProviderByID(id string) (ProviderInfo, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

// Zone is a DNS zone (a domain) of a connected account.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // active | pending | …
	Paused bool   `json:"paused,omitempty"`
	Plan   string `json:"plan,omitempty"`
	// NameServers are the provider's name servers for the zone;
	// CurrentNameServers those seen at the registrar, when the provider
	// reports them.
	NameServers        []string  `json:"nameServers"`
	CurrentNameServers []string  `json:"currentNameServers,omitempty"`
	Account            string    `json:"account,omitempty"`
	Modified           time.Time `json:"modified,omitempty"`
	URL                string    `json:"url,omitempty"` // the zone in the provider's dashboard
}

// Record is a DNS record in a provider-neutral shape.
type Record struct {
	ID   string `json:"id"`
	Type string `json:"type"` // A, AAAA, CNAME, MX, TXT, NS, SRV, CAA, PTR, …
	// Name is relative to the zone ("@" for the zone itself, "www",
	// "_dmarc"); FQDN the full name.
	Name string `json:"name"`
	FQDN string `json:"fqdn"`
	// Content is the value as text: an address (several, comma-separated,
	// for a provider that balances one record over addresses), a host
	// name, a text, "weight port target" for SRV, `flags tag "value"` for
	// CAA.
	Content  string `json:"content"`
	TTL      int    `json:"ttl"` // seconds; 1 (Cloudflare) = automatic
	Priority int    `json:"priority,omitempty"`
	// Proxied: the record points at the provider's CDN instead of the
	// origin (Cloudflare proxied, ArvanCloud cloud).
	Proxied   bool      `json:"proxied"`
	Proxiable bool      `json:"proxiable"`
	Comment   string    `json:"comment,omitempty"`
	Locked    bool      `json:"locked,omitempty"` // managed by the provider, cannot be changed
	Modified  time.Time `json:"modified,omitempty"`
	// Raw is the provider's own value for types without a text form.
	Raw any `json:"raw,omitempty"`
}

// RecordInput is a record to create, or the new state of one.
type RecordInput struct {
	Type     string `json:"type"`
	Name     string `json:"name"` // relative or full
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
	Proxied  *bool  `json:"proxied"`
	Comment  string `json:"comment"`
}

// Provider talks to one account's API.
type Provider interface {
	// Verify checks the token: who it belongs to (as the provider
	// reports it, may be empty) and how many zones it sees.
	Verify(ctx context.Context) (account string, zones int, err error)
	// Zones lists the account's zones, filtered by a substring of the name.
	Zones(ctx context.Context, query string) ([]Zone, error)
	// Zone finds one zone by name.
	Zone(ctx context.Context, name string) (*Zone, error)
	Records(ctx context.Context, z *Zone) ([]Record, error)
	Create(ctx context.Context, z *Zone, in RecordInput) (*Record, error)
	Update(ctx context.Context, z *Zone, cur *Record, in RecordInput) (*Record, error)
	Delete(ctx context.Context, z *Zone, id string) error
}

// ErrNoConnector: no connected account manages a domain.
var ErrNoConnector = errors.New("no DNS connector")

// Manager opens providers for connectors and finds the connector and zone
// of a workspace Domain resource.
type Manager struct {
	Vault *vault.Vault
	// Connections returns the connected DNS accounts.
	Connections func() []config.DNSConnection

	mu    sync.Mutex
	zones map[string]*Zone // connector/name → zone (ids do not change)
}

// New returns a manager without connectors.
func New(v *vault.Vault) *Manager {
	return &Manager{Vault: v, zones: map[string]*Zone{}}
}

// Find returns a connector by ID.
func (m *Manager) Find(id string) (config.DNSConnection, bool) {
	if m.Connections == nil {
		return config.DNSConnection{}, false
	}
	for _, c := range m.Connections() {
		if c.ID == id {
			return c, true
		}
	}
	return config.DNSConnection{}, false
}

// Token returns a connector's token from the vault. ws scopes the lookup
// when the token is used from a workspace (the agent policy of the item
// applies then); nil for the connector page itself.
func (m *Manager) Token(ctx context.Context, c config.DNSConnection, ws *workspace.Workspace) (string, error) {
	if m.Vault == nil {
		return "", errors.New("no vault")
	}
	var it vault.Item
	var err error
	if ws != nil {
		it, _, err = m.Vault.ResolveFor(ws.Name(), c.Credential)
	} else {
		it, _, err = m.Vault.Resolve(c.Credential)
	}
	if err != nil {
		return "", fmt.Errorf("connector %s: %w", c.ID, err)
	}
	tok := it.Get("token")
	if tok == "" {
		tok = it.Get("password")
	}
	if tok == "" {
		return "", fmt.Errorf("connector %s: the vault item has no token", c.ID)
	}
	return tok, nil
}

// Open builds the provider client of a connector with token.
func Open(c config.DNSConnection, token string) (Provider, error) {
	info, ok := ProviderByID(c.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown DNS provider %q", c.Provider)
	}
	base := strings.TrimSuffix(strings.TrimSpace(c.APIURL), "/")
	if base == "" {
		base = info.APIURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("invalid API URL %q", base)
	}
	hc, err := httpClient(c.Proxy)
	if err != nil {
		return nil, err
	}
	api := &api{base: base, hc: hc}
	switch c.Provider {
	case "cloudflare":
		api.auth = func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+token) }
		return &cloudflare{api: api}, nil
	case "arvancloud":
		api.auth = func(req *http.Request) {
			if strings.Contains(token, " ") {
				req.Header.Set("Authorization", token) // "Apikey …" pasted whole
			} else {
				req.Header.Set("Authorization", "Apikey "+token)
			}
		}
		return &arvancloud{api: api}, nil
	}
	return nil, fmt.Errorf("unknown DNS provider %q", c.Provider)
}

// httpClient reaches a provider's API: through proxy when set (socks5://
// or http://), else as the environment says (HTTPS_PROXY). Redirects are
// not followed, so the token never goes to another server.
func httpClient(proxy string) (*http.Client, error) {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 20 * time.Second, ResponseHeaderTimeout: 60 * time.Second,
		ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	if p := strings.TrimSpace(proxy); p != "" {
		pu, err := url.Parse(p)
		if err != nil || pu.Host == "" || (pu.Scheme != "socks5" && pu.Scheme != "socks5h" && pu.Scheme != "http" && pu.Scheme != "https") {
			return nil, fmt.Errorf("invalid proxy %q (socks5://host:port or http://host:port)", proxy)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	return &http.Client{Transport: tr, Timeout: 90 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// Opened is a Domain resource resolved to its connector, provider and zone.
type Opened struct {
	Conn config.DNSConnection
	Prov Provider
	Zone *Zone
}

// ZoneName is the zone of a Domain resource (spec.zone, else its name).
func ZoneName(r *workspace.Resource) string {
	z := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Str("zone")), "."))
	if z == "" {
		z = r.Name
	}
	return z
}

// ForDomain finds the connector that manages a Domain resource's zone (the
// one it names, else the connected account that has the zone), opens the
// provider and looks the zone up (cached: zone ids do not change).
func (m *Manager) ForDomain(ctx context.Context, ws *workspace.Workspace, r *workspace.Resource) (*Opened, error) {
	if r.Kind != "Domain" {
		return nil, fmt.Errorf("%s is not a Domain", r.ID())
	}
	name := ZoneName(r)
	var conns []config.DNSConnection
	if m.Connections != nil {
		conns = m.Connections()
	}
	if id := strings.TrimSpace(r.Str("connection")); id != "" {
		c, ok := m.Find(id)
		if !ok {
			return nil, fmt.Errorf("DNS connector %q is not set up on this computer (add it under Connectors)", id)
		}
		conns = []config.DNSConnection{c}
	}
	if len(conns) == 0 {
		return nil, fmt.Errorf("%w: connect Cloudflare or ArvanCloud under Connectors", ErrNoConnector)
	}
	var firstErr error
	for _, c := range conns {
		o, err := m.open(ctx, ws, c, name)
		if err == nil {
			return o, nil
		}
		if firstErr == nil || len(conns) == 1 {
			firstErr = err
		}
	}
	if len(conns) > 1 {
		return nil, fmt.Errorf("no connected DNS account has the zone %s (%v)", name, firstErr)
	}
	return nil, firstErr
}

func (m *Manager) open(ctx context.Context, ws *workspace.Workspace, c config.DNSConnection, name string) (*Opened, error) {
	tok, err := m.Token(ctx, c, ws)
	if err != nil {
		return nil, err
	}
	p, err := Open(c, tok)
	if err != nil {
		return nil, err
	}
	key := c.ID + "/" + name
	m.mu.Lock()
	z := m.zones[key]
	m.mu.Unlock()
	if z == nil {
		if z, err = p.Zone(ctx, name); err != nil {
			return nil, fmt.Errorf("%s: %w", c.ID, err)
		}
		m.mu.Lock()
		m.zones[key] = z
		m.mu.Unlock()
	}
	return &Opened{Conn: c, Prov: p, Zone: z}, nil
}

// Forget drops a cached zone (after an error that may mean it changed).
func (m *Manager) Forget(c config.DNSConnection, name string) {
	m.mu.Lock()
	delete(m.zones, c.ID+"/"+name)
	m.mu.Unlock()
}

// TokenItem is the vault item that stores a connector's token.
func TokenItem(name string, c config.DNSConnection, token string) vault.Item {
	info, _ := ProviderByID(c.Provider)
	return vault.Item{Scope: vault.GlobalScope, Type: "token", Name: name,
		Notes:  "DNS connector " + c.ID + " (" + info.Label + "). Used by opsdeck to read and change DNS records of the workspaces' domains.",
		Tags:   []string{"dns", c.Provider},
		Policy: vault.Policy{AgentUse: true},
		Fields: []vault.Field{{Name: "token", Value: token, Secret: true}, {Name: "url", Value: firstNonEmpty(c.APIURL, info.APIURL)}}}
}

// ----------------------------------------------------------------------------
// Names and values

// Relative returns a record name relative to its zone: "@" for the zone
// itself.
func Relative(fqdn, zone string) string {
	n := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(fqdn), "."))
	zone = strings.ToLower(strings.TrimSuffix(zone, "."))
	switch {
	case n == "" || n == "@" || n == zone:
		return "@"
	case strings.HasSuffix(n, "."+zone):
		return strings.TrimSuffix(n, "."+zone)
	}
	return n
}

// FQDN returns the full name of a record given relative or full.
func FQDN(name, zone string) string {
	rel := Relative(name, zone)
	zone = strings.ToLower(strings.TrimSuffix(zone, "."))
	if rel == "@" {
		return zone
	}
	return rel + "." + zone
}

// NormalizeType upper-cases a record type.
func NormalizeType(t string) string { return strings.ToUpper(strings.TrimSpace(t)) }

// Proxiable reports whether a type can point at a CDN.
func Proxiable(t string) bool {
	switch NormalizeType(t) {
	case "A", "AAAA", "CNAME", "ANAME":
		return true
	}
	return false
}

// SRV splits "weight port target".
func SRV(content string) (weight, port int, target string, err error) {
	f := strings.Fields(content)
	if len(f) != 3 {
		return 0, 0, "", fmt.Errorf("an SRV record's content is \"weight port target\" (got %q)", content)
	}
	if _, err := fmt.Sscan(f[0], &weight); err != nil {
		return 0, 0, "", fmt.Errorf("SRV weight %q is not a number", f[0])
	}
	if _, err := fmt.Sscan(f[1], &port); err != nil {
		return 0, 0, "", fmt.Errorf("SRV port %q is not a number", f[1])
	}
	return weight, port, strings.TrimSuffix(f[2], "."), nil
}

// CAA splits `flags tag "value"` (flags optional, defaults to 0).
func CAA(content string) (flags int, tag, value string, err error) {
	f := strings.Fields(content)
	if len(f) >= 3 {
		if _, err := fmt.Sscan(f[0], &flags); err != nil {
			return 0, "", "", fmt.Errorf("CAA flags %q is not a number", f[0])
		}
		f = f[1:]
	}
	if len(f) < 2 {
		return 0, "", "", fmt.Errorf("a CAA record's content is `flags tag \"value\"`, e.g. 0 issue \"letsencrypt.org\" (got %q)", content)
	}
	tag = strings.ToLower(f[0])
	value = strings.Trim(strings.Join(f[1:], " "), `"`)
	if tag != "issue" && tag != "issuewild" && tag != "iodef" {
		return 0, "", "", fmt.Errorf("CAA tag must be issue, issuewild or iodef (got %q)", tag)
	}
	return flags, tag, value, nil
}

// Format renders a record as a zone-file-like line (for previews and the
// agent).
func Format(r Record) string {
	ttl := "auto"
	if r.TTL > 1 {
		ttl = fmt.Sprint(r.TTL)
	}
	v := r.Content
	if r.Priority > 0 && (r.Type == "MX" || r.Type == "SRV") {
		v = fmt.Sprintf("%d %s", r.Priority, v)
	}
	line := fmt.Sprintf("%-28s %-6s %-6s %s", r.FQDN, ttl, r.Type, v)
	if r.Proxied {
		line += "  (proxied)"
	}
	if r.Comment != "" {
		line += "  ; " + r.Comment
	}
	return line
}

// Sort orders records by name (zone itself first), then type.
func Sort(rs []Record) {
	rank := func(r Record) string {
		if r.Name == "@" {
			return "0"
		}
		return "1" + r.Name
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if a, b := rank(rs[i]), rank(rs[j]); a != b {
			return a < b
		}
		if rs[i].Type != rs[j].Type {
			return rs[i].Type < rs[j].Type
		}
		return rs[i].Content < rs[j].Content
	})
}

// Filter keeps records of a type and/or whose name or content contains q.
func Filter(rs []Record, typ, q string) []Record {
	typ = NormalizeType(typ)
	q = strings.ToLower(strings.TrimSpace(q))
	out := []Record{}
	for _, r := range rs {
		if typ != "" && r.Type != typ {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.FQDN), q) && !strings.Contains(strings.ToLower(r.Content), q) && !strings.Contains(strings.ToLower(r.Comment), q) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Validate checks a record input before it is sent.
func Validate(in *RecordInput, zone string) error {
	in.Type = NormalizeType(in.Type)
	if in.Type == "" {
		return errors.New("type is required (A, AAAA, CNAME, MX, TXT, NS, SRV, CAA, PTR)")
	}
	in.Name = Relative(in.Name, zone)
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" {
		return errors.New("content is required")
	}
	if in.TTL < 0 {
		return errors.New("ttl cannot be negative")
	}
	switch in.Type {
	case "SRV":
		if _, _, _, err := SRV(in.Content); err != nil {
			return err
		}
	case "CAA":
		if _, _, _, err := CAA(in.Content); err != nil {
			return err
		}
	case "MX":
		if in.Priority < 0 {
			return errors.New("priority cannot be negative")
		}
	}
	if in.Proxied != nil && *in.Proxied && !Proxiable(in.Type) {
		return fmt.Errorf("a %s record cannot be proxied (only A, AAAA and CNAME)", in.Type)
	}
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
