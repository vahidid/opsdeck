package acmex

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"opsdeck/internal/dnsx"
	"opsdeck/internal/workspace"
)

// solver puts dns-01 challenge records into the zones through the DNS
// connectors and removes them afterwards.
type solver struct {
	m       *Manager
	ws      *workspace.Workspace
	log     func(string)
	zones   map[string]*dnsx.Opened // zone name → opened
	created []presented
}

type presented struct {
	zone *dnsx.Opened
	rec  *dnsx.Record
}

func newSolver(m *Manager, ws *workspace.Workspace, log func(string)) *solver {
	return &solver{m: m, ws: ws, log: log, zones: map[string]*dnsx.Opened{}}
}

// zoneFor finds the zone of a name (cached per zone).
func (s *solver) zoneFor(ctx context.Context, name string) (*dnsx.Opened, string, error) {
	for z, o := range s.zones {
		if name == z || strings.HasSuffix(name, "."+z) {
			return o, dnsx.Relative(name, z), nil
		}
	}
	o, rel, err := s.m.DNS.FindZone(ctx, s.ws, name)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", name, err)
	}
	s.zones[o.Zone.Name] = o
	p, _ := dnsx.ProviderByID(o.Conn.Provider)
	s.log(fmt.Sprintf("%s: zone %s at %s (connector %s)", name, o.Zone.Name, p.Label, o.Conn.ID))
	return o, rel, nil
}

// present creates the TXT record for a challenge and waits until the zone's
// name servers answer with it.
func (s *solver) present(ctx context.Context, name, value string) error {
	o, rel, err := s.zoneFor(ctx, name)
	if err != nil {
		return err
	}
	recName := "_acme-challenge"
	if rel != "@" {
		recName += "." + rel
	}
	fqdn := dnsx.FQDN(recName, o.Zone.Name)
	// a record left over from an earlier attempt with the same value
	if recs, err := o.Prov.Records(ctx, o.Zone); err == nil {
		for i := range recs {
			if recs[i].Type == "TXT" && recs[i].Name == recName && strings.Trim(recs[i].Content, `"`) == value {
				s.log(fmt.Sprintf("%s: TXT %s already present", name, fqdn))
				s.created = append(s.created, presented{o, &recs[i]})
				return s.wait(ctx, fqdn, value, o.Zone.NameServers)
			}
		}
	}
	rec, err := o.Prov.Create(ctx, o.Zone, dnsx.RecordInput{Type: "TXT", Name: recName, Content: value, TTL: 120})
	if err != nil {
		return fmt.Errorf("%s: could not create TXT %s: %w", name, fqdn, err)
	}
	s.created = append(s.created, presented{o, rec})
	s.log(fmt.Sprintf("%s: created TXT %s (id %s)", name, fqdn, rec.ID))
	return s.wait(ctx, fqdn, value, o.Zone.NameServers)
}

func (s *solver) wait(ctx context.Context, fqdn, value string, ns []string) error {
	if s.m.Propagation != nil {
		return s.m.Propagation(ctx, fqdn, value, ns, s.log)
	}
	return waitPropagation(ctx, fqdn, value, ns, s.log)
}

// cleanup deletes the challenge records (with its own deadline: the call's
// context may be done).
func (s *solver) cleanup(log func(string)) {
	if len(s.created) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, p := range s.created {
		if err := p.zone.Prov.Delete(ctx, p.zone.Zone, p.rec.ID); err != nil {
			log(fmt.Sprintf("could not remove TXT %s (id %s): %v — remove it by hand", p.rec.FQDN, p.rec.ID, err))
		} else {
			log(fmt.Sprintf("removed TXT %s", p.rec.FQDN))
		}
	}
	s.created = nil
}

// waitPropagation polls the zone's authoritative name servers until every
// one answers the TXT record with value, for up to a few minutes; when the
// servers cannot be asked, it waits a little and lets the CA check.
func waitPropagation(ctx context.Context, fqdn, value string, nameServers []string, log func(string)) error {
	deadline := time.Now().Add(4 * time.Minute)
	addrs := nsAddresses(ctx, nameServers)
	if len(addrs) == 0 {
		log(fmt.Sprintf("%s: cannot query the zone's name servers from here; waiting 20s for propagation", fqdn))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Second):
		}
		return nil
	}
	log(fmt.Sprintf("%s: waiting until %d name servers answer the record…", fqdn, len(addrs)))
	attempt := 0
	for {
		attempt++
		missing := 0
		for _, a := range addrs {
			if !hasTXT(ctx, a, fqdn, value) {
				missing++
			}
		}
		if missing == 0 {
			log(fmt.Sprintf("%s: visible on all name servers", fqdn))
			return nil
		}
		if time.Now().After(deadline) {
			log(fmt.Sprintf("%s: still missing on %d of %d name servers after %d checks; letting the CA try anyway", fqdn, missing, len(addrs), attempt))
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// nsAddresses resolves the name servers (one address each).
func nsAddresses(ctx context.Context, nameServers []string) []string {
	var out []string
	for _, ns := range nameServers {
		ns = strings.TrimSuffix(strings.TrimSpace(ns), ".")
		if ns == "" {
			continue
		}
		if ip := net.ParseIP(ns); ip != nil {
			out = append(out, ip.String())
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ips, err := net.DefaultResolver.LookupIPAddr(cctx, ns)
		cancel()
		if err == nil && len(ips) > 0 {
			out = append(out, ips[0].IP.String())
		}
	}
	return out
}

// hasTXT asks one name server directly.
func hasTXT(ctx context.Context, server, fqdn, value string) bool {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(server, "53"))
	}}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	txts, err := r.LookupTXT(cctx, fqdn)
	if err != nil {
		return false
	}
	for _, t := range txts {
		if strings.Trim(t, `"`) == value {
			return true
		}
	}
	return false
}

var errNoZone = errors.New("no zone")
