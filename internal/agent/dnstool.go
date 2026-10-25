package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"opsdeck/internal/dnsx"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// toolDNS reads and changes the DNS records of the workspace's domains
// (Domain resources) through the DNS connectors of this machine. Reads are
// free; set is a change and delete is destructive, each with the record
// before and after on the approval card.
func toolDNS() Tool {
	return Tool{
		Def: llm.Tool{Name: "dns", Description: `The DNS zones of the workspace's domains (Domain resources, managed at Cloudflare or ArvanCloud through a connected account — you never see its token). Actions: zones (the domains with their status and name servers), records (a zone's records, filtered by type and query), set (create a record, or change the one named by id or by type+name: content, ttl, priority, proxied, comment), delete (by id, or by type+name when that names one record). Names are relative to the zone (@ for the zone itself, www, _dmarc) or full. Content: an address for A/AAAA (ArvanCloud: several, comma-separated, balanced), a host for CNAME/NS/MX/PTR (MX takes priority), text for TXT, "weight port target" for SRV (plus priority), 'flags tag "value"' for CAA. proxied=true routes A/AAAA/CNAME records through the provider's CDN. ttl in seconds (0 = the provider's automatic/default). set and delete wait for the user's approval.`,
			Schema: obj(map[string]any{
				"domain":   str("Domain resource name (default: the workspace's only domain)"),
				"action":   enum("What to do", "zones", "records", "set", "delete"),
				"type":     str("Record type: A, AAAA, CNAME, MX, TXT, NS, SRV, CAA, PTR (records: filter)"),
				"name":     str("Record name, relative to the zone (@, www, _dmarc) or full (records: filter by substring)"),
				"content":  str("set: the record's content"),
				"ttl":      num("set: TTL in seconds (0 = automatic/default)"),
				"priority": num("set: priority of MX and SRV records"),
				"proxied":  map[string]any{"type": "boolean", "description": "set: route the record through the provider's CDN (A, AAAA, CNAME)"},
				"comment":  str("set: a note on the record (Cloudflare)"),
				"id":       str("set/delete: the record id (from records) when type+name is ambiguous"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Domain, Action, Type, Name, Content, Comment, ID string
				TTL, Priority                                    int
				Proxied                                          *bool
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if env.DNS == nil {
				return nil, errors.New("DNS is not available")
			}
			switch in.Action {
			case "zones":
				return &Call{Risk: RiskRead, Target: "dns/" + env.WS.Name(), Match: "zones", Run: func(ctx context.Context) (string, error) {
					return dnsZones(ctx, env), nil
				}}, nil
			case "records", "set", "delete":
			default:
				return nil, fmt.Errorf("unknown action %q (zones, records, set, delete)", in.Action)
			}
			r, err := env.domain(in.Domain)
			if err != nil {
				return nil, err
			}
			envName, protected := targetInfo(env.WS, r)
			target := "domain/" + r.Name
			zone := dnsx.ZoneName(r)
			if in.Action == "records" {
				return &Call{Risk: RiskRead, Target: target, Environment: envName, Protected: protected, Match: strings.TrimSpace("records " + in.Type + " " + in.Name),
					Credentials: env.dnsCredentials(r), Run: func(ctx context.Context) (string, error) {
						ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
						defer cancel()
						o, err := env.DNS.ForDomain(ctx, env.WS, r)
						if err != nil {
							return "", err
						}
						recs, err := o.Prov.Records(ctx, o.Zone)
						if err != nil {
							env.DNS.Forget(o.Conn, zone)
							return "", err
						}
						recs = dnsx.Filter(recs, in.Type, in.Name)
						setView(ctx, map[string]any{"kind": "dns", "zone": o.Zone.Name, "records": recs})
						return formatRecords(o.Zone, recs), nil
					}}, nil
			}
			// set and delete: find the record now, so the approval card shows
			// what changes.
			ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			o, err := env.DNS.ForDomain(ctx, env.WS, r)
			if err != nil {
				return nil, err
			}
			recs, err := o.Prov.Records(ctx, o.Zone)
			if err != nil {
				env.DNS.Forget(o.Conn, zone)
				return nil, err
			}
			cur, err := pickRecord(recs, zone, in.ID, in.Type, in.Name, in.Content, in.Action == "delete")
			if err != nil {
				return nil, err
			}
			if in.Action == "delete" {
				if cur == nil {
					return nil, errors.New("delete needs the record's id, or a type and name that match exactly one record")
				}
				if cur.Locked {
					return nil, fmt.Errorf("the record %s is managed by the provider and cannot be deleted", cur.FQDN)
				}
				return &Call{Risk: RiskDestructive, Target: target, Environment: envName, Protected: protected, Match: "delete " + cur.Type + " " + cur.FQDN,
					Preview: "delete from " + zone + ":\n- " + dnsx.Format(*cur), Credentials: env.dnsCredentials(r), Run: func(ctx context.Context) (string, error) {
						ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
						defer cancel()
						if err := o.Prov.Delete(ctx, o.Zone, cur.ID); err != nil {
							return "", err
						}
						return "Deleted from " + zone + ":\n" + dnsx.Format(*cur) + "\n\nResolvers keep the old answer for up to its TTL.", nil
					}}, nil
			}
			// set
			ri := dnsx.RecordInput{Type: in.Type, Name: in.Name, Content: in.Content, TTL: in.TTL, Priority: in.Priority, Proxied: in.Proxied, Comment: in.Comment}
			if cur != nil {
				// fill what the call left out from the current record
				ri.Type = firstNonEmpty(ri.Type, cur.Type)
				ri.Name = firstNonEmpty(ri.Name, cur.Name)
				ri.Content = firstNonEmpty(ri.Content, cur.Content)
				if ri.TTL == 0 && !hasKey(input, "ttl") {
					ri.TTL = cur.TTL
				}
				if ri.Priority == 0 && !hasKey(input, "priority") {
					ri.Priority = cur.Priority
				}
				if ri.Proxied == nil {
					p := cur.Proxied
					ri.Proxied = &p
				}
				if ri.Comment == "" && !hasKey(input, "comment") {
					ri.Comment = cur.Comment
				}
				if cur.Locked {
					return nil, fmt.Errorf("the record %s is managed by the provider and cannot be changed", cur.FQDN)
				}
			}
			if err := dnsx.Validate(&ri, zone); err != nil {
				return nil, err
			}
			after := dnsx.Record{Type: ri.Type, Name: ri.Name, FQDN: dnsx.FQDN(ri.Name, zone), Content: ri.Content, TTL: ri.TTL, Priority: ri.Priority, Comment: ri.Comment}
			if ri.Proxied != nil {
				after.Proxied = *ri.Proxied
			}
			var preview, match string
			if cur != nil {
				if dnsx.Format(*cur) == dnsx.Format(after) && cur.TTL == after.TTL {
					return nil, fmt.Errorf("the record %s %s already has these values", cur.Type, cur.FQDN)
				}
				preview = "change in " + zone + ":\n- " + dnsx.Format(*cur) + "\n+ " + dnsx.Format(after)
				match = "update " + after.Type + " " + after.FQDN
			} else {
				preview = "add to " + zone + ":\n+ " + dnsx.Format(after)
				match = "create " + after.Type + " " + after.FQDN
			}
			return &Call{Risk: RiskChange, Target: target, Environment: envName, Protected: protected, Match: match, Preview: preview,
				Credentials: env.dnsCredentials(r), Run: func(ctx context.Context) (string, error) {
					ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
					defer cancel()
					var rec *dnsx.Record
					var err error
					if cur != nil {
						rec, err = o.Prov.Update(ctx, o.Zone, cur, ri)
					} else {
						rec, err = o.Prov.Create(ctx, o.Zone, ri)
					}
					if err != nil {
						return "", err
					}
					verb := "Added to"
					if cur != nil {
						verb = "Changed in"
					}
					setView(ctx, map[string]any{"kind": "dns", "zone": o.Zone.Name, "records": []dnsx.Record{*rec}})
					return fmt.Sprintf("%s %s (id %s):\n%s\n\nResolvers pick the change up within its TTL.", verb, zone, rec.ID, dnsx.Format(*rec)), nil
				}}, nil
		},
	}
}

// domain finds a Domain resource by name (or the only one).
func (e *Env) domain(name string) (*workspace.Resource, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "domain/")
	var all []*workspace.Resource
	for _, r := range e.WS.Resources {
		if r.Kind == "Domain" {
			all = append(all, r)
		}
	}
	if len(all) == 0 {
		return nil, errors.New("this workspace has no domains: add one (kind Domain, with its zone) with workspace_resource or on the Domains page")
	}
	if name == "" && len(all) == 1 {
		return all[0], nil
	}
	var names []string
	for _, r := range all {
		if r.Name == name || dnsx.ZoneName(r) == strings.ToLower(name) {
			return r, nil
		}
		names = append(names, r.Name+" ("+dnsx.ZoneName(r)+")")
	}
	if name == "" {
		return nil, fmt.Errorf("name one of the domains: %s", strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("domain %q not found; domains: %s", name, strings.Join(names, ", "))
}

// dnsCredentials is the vault reference of the connector a domain uses
// (for confirm-on-use), when it is known without asking the provider.
func (e *Env) dnsCredentials(r *workspace.Resource) []string {
	if e.DNS == nil {
		return nil
	}
	if id := r.Str("connection"); id != "" {
		if c, ok := e.DNS.Find(id); ok {
			return []string{c.Credential}
		}
		return nil
	}
	var refs []string
	if e.DNS.Connections != nil {
		for _, c := range e.DNS.Connections() {
			refs = append(refs, c.Credential)
		}
	}
	return refs
}

// pickRecord finds the record a set/delete call means: by id, else by
// type+name (and content when several share them). nil when none matches
// and the call does not need one (set creates).
func pickRecord(recs []dnsx.Record, zone, id, typ, name, content string, must bool) (*dnsx.Record, error) {
	if id != "" {
		for i := range recs {
			if recs[i].ID == id {
				return &recs[i], nil
			}
		}
		return nil, fmt.Errorf("no record with id %s in %s (list them with records)", id, zone)
	}
	typ = dnsx.NormalizeType(typ)
	if typ == "" || strings.TrimSpace(name) == "" {
		if must {
			return nil, errors.New("give the record's id, or its type and name")
		}
		return nil, nil
	}
	rel := dnsx.Relative(name, zone)
	var found []*dnsx.Record
	for i := range recs {
		if recs[i].Type == typ && recs[i].Name == rel {
			found = append(found, &recs[i])
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	}
	for _, f := range found {
		if content != "" && strings.EqualFold(strings.TrimSpace(f.Content), strings.TrimSpace(content)) {
			return f, nil
		}
	}
	if !must && content != "" {
		return nil, nil // a new record next to the existing ones
	}
	var lines []string
	for _, f := range found {
		lines = append(lines, f.ID+": "+dnsx.Format(*f))
	}
	return nil, fmt.Errorf("%d %s records are named %s; give the id of the one you mean:\n%s", len(found), typ, dnsx.FQDN(rel, zone), strings.Join(lines, "\n"))
}

// hasKey reports whether the call set a key at all (to tell 0 from unset).
func hasKey(input json.RawMessage, key string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

func formatRecords(z *dnsx.Zone, recs []dnsx.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d records", z.Name, len(recs))
	if z.Status != "" {
		fmt.Fprintf(&b, " (zone %s", z.Status)
		if z.Paused {
			b.WriteString(", paused")
		}
		b.WriteString(")")
	}
	b.WriteString("\n")
	if len(recs) == 0 {
		return b.String()
	}
	b.WriteString("id | name ttl type content\n")
	for _, r := range recs {
		fmt.Fprintf(&b, "%s | %s\n", r.ID, dnsx.Format(r))
	}
	return b.String()
}

// dnsZones lists the workspace's domains with their zones.
func dnsZones(ctx context.Context, env *Env) string {
	var b strings.Builder
	n := 0
	for _, r := range env.WS.Resources {
		if r.Kind != "Domain" {
			continue
		}
		n++
		e := env.WS.EnvironmentOf(r)
		fmt.Fprintf(&b, "- %s: zone %s", r.Name, dnsx.ZoneName(r))
		if r.Environment != "" {
			fmt.Fprintf(&b, " env=%s", r.Environment)
			if e.Protected {
				b.WriteString(" (PROTECTED)")
			}
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		o, err := env.DNS.ForDomain(cctx, env.WS, r)
		cancel()
		if err != nil {
			fmt.Fprintf(&b, " — not reachable: %v\n", err)
			continue
		}
		p, _ := dnsx.ProviderByID(o.Conn.Provider)
		fmt.Fprintf(&b, " at %s (connector %s): status %s", p.Label, o.Conn.ID, o.Zone.Status)
		if len(o.Zone.NameServers) > 0 {
			fmt.Fprintf(&b, ", name servers %s", strings.Join(o.Zone.NameServers, " "))
		}
		if len(o.Zone.CurrentNameServers) > 0 && !sameSet(o.Zone.NameServers, o.Zone.CurrentNameServers) {
			fmt.Fprintf(&b, " (registrar points at %s)", strings.Join(o.Zone.CurrentNameServers, " "))
		}
		if r.Str("notes") != "" {
			fmt.Fprintf(&b, "\n  notes: %s", strings.ReplaceAll(strings.TrimSpace(r.Str("notes")), "\n", "\n  "))
		}
		b.WriteString("\n")
	}
	if n == 0 {
		return "No domains in this workspace (add a Domain resource with its zone)."
	}
	return b.String()
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[strings.ToLower(strings.TrimSuffix(x, "."))] = true
	}
	for _, x := range b {
		if !m[strings.ToLower(strings.TrimSuffix(x, "."))] {
			return false
		}
	}
	return true
}
