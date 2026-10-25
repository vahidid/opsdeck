package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"opsdeck/internal/config"
	"opsdeck/internal/dnsx"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// DNS connectors (Cloudflare, ArvanCloud accounts connected once on this
// machine) and the Domains page: the zones of a workspace's Domain
// resources and their records. Reads are free; every record change is
// audited.

func (s *Server) dnsRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/dns/providers", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, dnsx.Providers) })
	mux.HandleFunc("GET /api/dns/connections", u(s.dnsConnList))
	mux.HandleFunc("POST /api/dns/connections", u(s.dnsConnCreate))
	mux.HandleFunc("POST /api/dns/connections/{id}/test", u(s.dnsConnTest))
	mux.HandleFunc("DELETE /api/dns/connections/{id}", u(s.dnsConnDelete))
	mux.HandleFunc("GET /api/dns/connections/{id}/zones", u(s.dnsConnZones))

	D := "/api/workspaces/{ws}/domains"
	mux.HandleFunc("GET "+D, u(s.domainsList))
	mux.HandleFunc("GET "+D+"/{name}", u(s.domainGet))
	mux.HandleFunc("GET "+D+"/{name}/records", u(s.domainRecords))
	mux.HandleFunc("POST "+D+"/{name}/records", u(s.domainRecordCreate))
	mux.HandleFunc("PUT "+D+"/{name}/records/{id}", u(s.domainRecordUpdate))
	mux.HandleFunc("DELETE "+D+"/{name}/records/{id}", u(s.domainRecordDelete))
}

// DNSConnInfo is a DNS connector as the UI sees it (never the token).
type DNSConnInfo struct {
	config.DNSConnection
	Label     string `json:"label"`
	TokenPage string `json:"tokenPage"`
	Dashboard string `json:"dashboard"`
}

func dnsConnInfo(c config.DNSConnection) DNSConnInfo {
	p, _ := dnsx.ProviderByID(c.Provider)
	return DNSConnInfo{DNSConnection: c, Label: p.Label, TokenPage: p.TokenPage, Dashboard: p.Dashboard}
}

func (s *Server) dnsConnList(w http.ResponseWriter, r *http.Request) {
	out := []DNSConnInfo{}
	for _, c := range s.cfg.DNSConnections() {
		out = append(out, dnsConnInfo(c))
	}
	writeJSON(w, 200, out)
}

var dnsIDClean = regexp.MustCompile(`[^a-z0-9]+`)

// dnsConnID keeps the ID of the connector being reconnected (or of one for
// the same account), else derives a free one from the provider.
func (s *Server) dnsConnID(c config.DNSConnection, reconnect string) string {
	existing := s.cfg.DNSConnections()
	for _, x := range existing {
		if x.ID == reconnect && x.Provider == c.Provider {
			return x.ID
		}
	}
	for _, x := range existing {
		if x.Provider == c.Provider && x.APIURL == c.APIURL && (x.Account == c.Account && c.Account != "" || c.Account == "" && x.Account == "") {
			return x.ID
		}
	}
	base := strings.Trim(dnsIDClean.ReplaceAllString(strings.ToLower(c.Provider), "-"), "-")
	id := base
	for n := 2; ; n++ {
		taken := false
		for _, x := range existing {
			taken = taken || x.ID == id
		}
		if !taken {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

func (s *Server) dnsConnCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string `json:"id"` // reconnecting this connector
		Provider string `json:"provider"`
		APIURL   string `json:"apiUrl"`
		Proxy    string `json:"proxy"`
		Token    string `json:"token"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if _, ok := dnsx.ProviderByID(in.Provider); !ok {
		fail(w, errors.New("provider must be cloudflare or arvancloud"))
		return
	}
	token := strings.TrimSpace(in.Token)
	if token == "" {
		fail(w, errors.New("a token is required"))
		return
	}
	c := config.DNSConnection{Provider: in.Provider, APIURL: strings.TrimSuffix(strings.TrimSpace(in.APIURL), "/"), Proxy: strings.TrimSpace(in.Proxy), Method: "token"}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	c, err := s.saveDNSConn(ctx, c, token, in.ID)
	s.record(r, "", "dns.connect", c.Provider, map[string]any{"account": c.Account, "zones": c.Zones}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, dnsConnInfo(c))
}

// saveDNSConn checks the token against the provider and stores the
// connector with the token in the vault.
func (s *Server) saveDNSConn(ctx context.Context, c config.DNSConnection, token, reconnect string) (config.DNSConnection, error) {
	p, err := dnsx.Open(c, token)
	if err != nil {
		return c, err
	}
	acct, zones, err := p.Verify(ctx)
	if err != nil {
		return c, err
	}
	c.Account, c.Zones = acct, zones
	c.ID = s.dnsConnID(c, reconnect)
	if old, ok := s.dns.Find(c.ID); ok && !old.Created.IsZero() {
		c.Created = old.Created
	}
	if c.Created.IsZero() {
		c.Created = time.Now().UTC()
	}
	name := "dns-" + c.ID
	item := dnsx.TokenItem(name, c, token)
	items, err := s.vault.List(vault.GlobalScope)
	if err != nil {
		return c, err
	}
	var saved vault.Item
	for _, it := range items {
		if it.Name == name {
			item.Policy = it.Policy // keep what the user chose
			saved, err = s.vault.Update(it.ID, item, nil)
			break
		}
	}
	if saved.ID == "" && err == nil {
		saved, err = s.vault.Create(item)
	}
	if err != nil {
		return c, err
	}
	c.Credential = saved.Ref()
	if err := s.cfg.PutDNSConnection(c); err != nil {
		return c, err
	}
	return c, nil
}

func (s *Server) dnsConnTest(w http.ResponseWriter, r *http.Request) {
	c, ok := s.dns.Find(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "connector not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res := map[string]any{"ok": false}
	token, err := s.dns.Token(ctx, c, nil)
	if err == nil {
		var p dnsx.Provider
		if p, err = dnsx.Open(c, token); err == nil {
			var acct string
			var n int
			if acct, n, err = p.Verify(ctx); err == nil {
				res["ok"], res["account"], res["zones"] = true, acct, n
				if acct != c.Account || n != c.Zones {
					c.Account, c.Zones = acct, n
					s.cfg.PutDNSConnection(c)
				}
			}
		}
	}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, 200, res)
}

func (s *Server) dnsConnDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := s.dns.Find(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "connector not found")
		return
	}
	if it, _, err := s.vault.Resolve(c.Credential); err == nil && it.Name == "dns-"+c.ID {
		s.vault.Delete(it.ID)
	}
	err := s.cfg.DeleteDNSConnection(c.ID)
	s.record(r, "", "dns.disconnect", c.Provider, map[string]any{"id": c.ID}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// dnsConnZones lists the zones of a connected account (to add one to a
// workspace).
func (s *Server) dnsConnZones(w http.ResponseWriter, r *http.Request) {
	c, ok := s.dns.Find(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "connector not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	token, err := s.dns.Token(ctx, c, nil)
	if err != nil {
		fail(w, err)
		return
	}
	p, err := dnsx.Open(c, token)
	if err != nil {
		fail(w, err)
		return
	}
	zones, err := p.Zones(ctx, r.URL.Query().Get("q"))
	if err != nil {
		fail(w, err)
		return
	}
	if zones == nil {
		zones = []dnsx.Zone{}
	}
	writeJSON(w, 200, zones)
}

// ----------------------------------------------------------------------------
// Domains of a workspace

// DomainInfo is a Domain resource with its zone as the provider sees it.
type DomainInfo struct {
	Name        string              `json:"name"`
	Zone        string              `json:"zone"`
	Environment string              `json:"environment,omitempty"`
	Protected   bool                `json:"protected,omitempty"`
	Description string              `json:"description,omitempty"`
	Connection  string              `json:"connection,omitempty"`
	Provider    string              `json:"provider,omitempty"`
	Account     string              `json:"account,omitempty"`
	Info        *dnsx.Zone          `json:"info,omitempty"`
	Error       string              `json:"error,omitempty"`
	Proxy       string              `json:"proxyLabel,omitempty"`
	TTLs        []int               `json:"ttls,omitempty"`
	Comments    bool                `json:"comments"`
	Resource    *workspace.Resource `json:"-"`
}

func (s *Server) domainInfo(ctx context.Context, ws *workspace.Workspace, r *workspace.Resource) (DomainInfo, *dnsx.Opened) {
	e := ws.EnvironmentOf(r)
	d := DomainInfo{Name: r.Name, Zone: dnsx.ZoneName(r), Environment: r.Environment, Protected: e.Protected, Description: r.Description, Connection: r.Str("connection"), Resource: r}
	o, err := s.dns.ForDomain(ctx, ws, r)
	if err != nil {
		d.Error = err.Error()
		return d, nil
	}
	p, _ := dnsx.ProviderByID(o.Conn.Provider)
	d.Connection, d.Provider, d.Account, d.Info, d.Proxy, d.TTLs, d.Comments = o.Conn.ID, o.Conn.Provider, o.Conn.Account, o.Zone, p.Proxy, p.TTLs, p.Comments
	return d, o
}

func (s *Server) domainsList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out := []DomainInfo{}
	for _, res := range ws.Resources {
		if res.Kind != "Domain" {
			continue
		}
		d, _ := s.domainInfo(ctx, ws, res)
		out = append(out, d)
	}
	writeJSON(w, 200, out)
}

// domain resolves the Domain resource of the request and opens its zone.
func (s *Server) domain(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, DomainInfo, *dnsx.Opened, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, DomainInfo{}, nil, false
	}
	res := ws.Find("Domain", r.PathValue("name"))
	if res == nil {
		writeErr(w, 404, "domain not found")
		return nil, DomainInfo{}, nil, false
	}
	d, o := s.domainInfo(r.Context(), ws, res)
	if o == nil {
		writeErr(w, 502, d.Error)
		return nil, DomainInfo{}, nil, false
	}
	return ws, d, o, true
}

func (s *Server) domainGet(w http.ResponseWriter, r *http.Request) {
	_, d, _, ok := s.domain(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) domainRecords(w http.ResponseWriter, r *http.Request) {
	_, d, o, ok := s.domain(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	recs, err := o.Prov.Records(ctx, o.Zone)
	if err != nil {
		s.dns.Forget(o.Conn, d.Zone)
		fail(w, err)
		return
	}
	writeJSON(w, 200, dnsx.Filter(recs, r.URL.Query().Get("type"), r.URL.Query().Get("q")))
}

func (s *Server) domainRecordCreate(w http.ResponseWriter, r *http.Request) {
	ws, d, o, ok := s.domain(w, r)
	if !ok {
		return
	}
	var in dnsx.RecordInput
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := dnsx.Validate(&in, d.Zone); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	rec, err := o.Prov.Create(ctx, o.Zone, in)
	detail := map[string]any{"type": in.Type, "name": dnsx.FQDN(in.Name, d.Zone), "content": in.Content, "connector": o.Conn.ID}
	if rec != nil {
		detail["id"] = rec.ID
	}
	s.record(r, ws.Name(), "dns.record.create", "domain/"+d.Name, detail, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, rec)
}

func (s *Server) domainRecordUpdate(w http.ResponseWriter, r *http.Request) {
	ws, d, o, ok := s.domain(w, r)
	if !ok {
		return
	}
	var in dnsx.RecordInput
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := dnsx.Validate(&in, d.Zone); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cur, err := findRecord(ctx, o, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	rec, err := o.Prov.Update(ctx, o.Zone, cur, in)
	s.record(r, ws.Name(), "dns.record.update", "domain/"+d.Name, map[string]any{"id": cur.ID, "type": in.Type, "name": dnsx.FQDN(in.Name, d.Zone),
		"before": dnsx.Format(*cur), "content": in.Content, "connector": o.Conn.ID}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, rec)
}

func (s *Server) domainRecordDelete(w http.ResponseWriter, r *http.Request) {
	ws, d, o, ok := s.domain(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cur, err := findRecord(ctx, o, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	err = o.Prov.Delete(ctx, o.Zone, cur.ID)
	s.record(r, ws.Name(), "dns.record.delete", "domain/"+d.Name, map[string]any{"id": cur.ID, "type": cur.Type, "name": cur.FQDN, "record": dnsx.Format(*cur), "connector": o.Conn.ID}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// findRecord fetches the zone's records and returns the one with id.
func findRecord(ctx context.Context, o *dnsx.Opened, id string) (*dnsx.Record, error) {
	recs, err := o.Prov.Records(ctx, o.Zone)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		if recs[i].ID == id {
			return &recs[i], nil
		}
	}
	return nil, fmt.Errorf("record %s not found in %s (it may have been changed elsewhere; reload)", id, o.Zone.Name)
}
