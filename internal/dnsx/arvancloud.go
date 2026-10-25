package dnsx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// arvancloud talks to the ArvanCloud CDN API 4.0 (napi.arvancloud.ir) with
// an API key. Domains are addressed by name; a record's value is an object
// whose shape depends on the type.
type arvancloud struct{ api *api }

type arEnvelope struct {
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Meta    struct {
		CurrentPage int `json:"current_page"`
		LastPage    int `json:"last_page"`
		Total       int `json:"total"`
	} `json:"meta"`
}

func (a *arvancloud) call(ctx context.Context, method, path string, body any, out any) (*arEnvelope, error) {
	var env arEnvelope
	if _, err := a.api.do(ctx, method, path, body, &env); err != nil {
		return nil, err
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return nil, fmt.Errorf("unexpected answer from ArvanCloud: %v", err)
		}
	}
	return &env, nil
}

type arDomain struct {
	ID        string    `json:"id"`
	Domain    string    `json:"domain"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	PlanLevel int       `json:"plan_level"`
	NSKeys    []string  `json:"ns_keys"`
	CurrentNS []string  `json:"current_ns"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (d arDomain) zone() Zone {
	name := strings.ToLower(firstNonEmpty(d.Domain, d.Name))
	plan := ""
	if d.PlanLevel > 0 {
		plan = "level " + strconv.Itoa(d.PlanLevel)
	}
	return Zone{ID: firstNonEmpty(d.ID, name), Name: name, Status: firstNonEmpty(d.Status, "active"), Plan: plan,
		NameServers: nonNilStrings(d.NSKeys), CurrentNameServers: d.CurrentNS, Modified: d.UpdatedAt,
		URL: "https://panel.arvancloud.ir/cdn/" + url.PathEscape(name) + "/dns-settings"}
}

func (a *arvancloud) Verify(ctx context.Context) (string, int, error) {
	zones, err := a.Zones(ctx, "")
	if err != nil {
		return "", 0, fmt.Errorf("the API key was not accepted or cannot list domains: %w", err)
	}
	return "", len(zones), nil
}

func (a *arvancloud) Zones(ctx context.Context, query string) ([]Zone, error) {
	out := []Zone{}
	q := strings.ToLower(strings.TrimSpace(query))
	for page := 1; page <= 50; page++ {
		path := "/domains?per_page=50&page=" + strconv.Itoa(page)
		if q != "" {
			path += "&search=" + url.QueryEscape(q)
		}
		var list []arDomain
		env, err := a.call(ctx, "GET", path, nil, &list)
		if err != nil {
			return nil, err
		}
		for _, d := range list {
			z := d.zone()
			if q == "" || strings.Contains(z.Name, q) {
				out = append(out, z)
			}
		}
		if len(list) == 0 || env.Meta.LastPage == 0 || page >= env.Meta.LastPage {
			break
		}
	}
	return out, nil
}

func (a *arvancloud) Zone(ctx context.Context, name string) (*Zone, error) {
	var d arDomain
	if _, err := a.call(ctx, "GET", "/domains/"+url.PathEscape(strings.ToLower(name)), nil, &d); err != nil {
		return nil, fmt.Errorf("domain %s is not in this ArvanCloud account (or the key cannot see it): %w", name, err)
	}
	z := d.zone()
	if z.Name == "" {
		z.Name = strings.ToLower(name)
	}
	return &z, nil
}

type arRecord struct {
	ID            string          `json:"id,omitempty"`
	Type          string          `json:"type"`
	Name          string          `json:"name"`
	Value         json.RawMessage `json:"value,omitempty"`
	TTL           int             `json:"ttl,omitempty"`
	Cloud         bool            `json:"cloud"`
	UpstreamHTTPS string          `json:"upstream_https,omitempty"`
	IPFilterMode  *arIPFilter     `json:"ip_filter_mode,omitempty"`
	IsProtected   bool            `json:"is_protected,omitempty"`
	UpdatedAt     time.Time       `json:"updated_at,omitempty"`
}

type arIPFilter struct {
	Count     string `json:"count"`
	Order     string `json:"order"`
	GeoFilter string `json:"geo_filter"`
}

type arAddr struct {
	IP      string `json:"ip"`
	Port    *int   `json:"port"`
	Weight  *int   `json:"weight"`
	Country string `json:"country"`
}

// record converts an ArvanCloud record: the typed value object becomes the
// text content.
func (r arRecord) record(zone string) Record {
	typ := NormalizeType(r.Type)
	out := Record{ID: r.ID, Type: typ, Name: Relative(r.Name, zone), TTL: r.TTL, Proxied: r.Cloud, Proxiable: Proxiable(typ), Locked: r.IsProtected, Modified: r.UpdatedAt}
	out.FQDN = FQDN(out.Name, zone)
	var raw any
	json.Unmarshal(r.Value, &raw)
	switch typ {
	case "A", "AAAA":
		var addrs []arAddr
		if json.Unmarshal(r.Value, &addrs) == nil {
			var ips []string
			for _, x := range addrs {
				if x.IP != "" {
					ips = append(ips, x.IP)
				}
			}
			out.Content = strings.Join(ips, ", ")
		}
	case "CNAME", "ANAME", "NS":
		var v struct{ Host string }
		json.Unmarshal(r.Value, &v)
		out.Content = v.Host
	case "MX":
		var v struct {
			Host     string
			Priority int
		}
		json.Unmarshal(r.Value, &v)
		out.Content, out.Priority = v.Host, v.Priority
	case "TXT", "SPF", "DKIM":
		var v struct{ Text string }
		json.Unmarshal(r.Value, &v)
		out.Content = v.Text
	case "SRV":
		var v struct {
			Target                 string
			Port, Weight, Priority int
		}
		json.Unmarshal(r.Value, &v)
		out.Content, out.Priority = fmt.Sprintf("%d %d %s", v.Weight, v.Port, v.Target), v.Priority
	case "CAA":
		var v struct{ Tag, Value string }
		json.Unmarshal(r.Value, &v)
		out.Content = fmt.Sprintf("0 %s %q", v.Tag, v.Value)
	case "PTR":
		var v struct{ Domain string }
		json.Unmarshal(r.Value, &v)
		out.Content = v.Domain
	}
	if out.Content == "" && len(r.Value) > 0 {
		out.Raw = raw
		out.Content = string(r.Value)
	}
	return out
}

func (a *arvancloud) Records(ctx context.Context, z *Zone) ([]Record, error) {
	out := []Record{}
	for page := 1; page <= 100; page++ {
		var list []arRecord
		env, err := a.call(ctx, "GET", "/domains/"+url.PathEscape(z.Name)+"/dns-records?per_page=100&page="+strconv.Itoa(page), nil, &list)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			out = append(out, r.record(z.Name))
		}
		if len(list) == 0 || env.Meta.LastPage == 0 || page >= env.Meta.LastPage {
			break
		}
	}
	Sort(out)
	return out, nil
}

// body builds the create/update body of a record. Settings the input does
// not express (upstream_https, ip_filter_mode) get their defaults.
func (a *arvancloud) body(z *Zone, in RecordInput) (map[string]any, error) {
	if err := Validate(&in, z.Name); err != nil {
		return nil, err
	}
	b := map[string]any{"type": strings.ToLower(in.Type), "name": in.Name, "cloud": in.Proxied != nil && *in.Proxied}
	ttl := in.TTL
	if ttl <= 1 {
		ttl = 120
	}
	b["ttl"] = ttl
	var value any
	switch in.Type {
	case "A", "AAAA":
		var addrs []map[string]any
		for _, ip := range strings.FieldsFunc(in.Content, func(r rune) bool { return r == ',' || r == ' ' }) {
			addrs = append(addrs, map[string]any{"ip": ip, "port": nil, "weight": nil, "country": ""})
		}
		if len(addrs) == 0 {
			return nil, errors.New("an address is required")
		}
		value = addrs
		b["upstream_https"] = "default"
		b["ip_filter_mode"] = map[string]any{"count": "single", "order": "none", "geo_filter": "none"}
	case "CNAME", "ANAME":
		value = map[string]any{"host": strings.TrimSuffix(in.Content, "."), "host_header": "source", "port": nil}
		b["upstream_https"] = "default"
	case "NS":
		value = map[string]any{"host": strings.TrimSuffix(in.Content, ".")}
	case "MX":
		value = map[string]any{"host": strings.TrimSuffix(in.Content, "."), "priority": in.Priority}
	case "TXT", "SPF", "DKIM":
		value = map[string]any{"text": in.Content}
	case "SRV":
		weight, port, target, err := SRV(in.Content)
		if err != nil {
			return nil, err
		}
		value = map[string]any{"target": target, "port": port, "weight": weight, "priority": in.Priority}
	case "CAA":
		_, tag, v, err := CAA(in.Content)
		if err != nil {
			return nil, err
		}
		value = map[string]any{"tag": tag, "value": v}
	case "PTR":
		value = map[string]any{"domain": strings.TrimSuffix(in.Content, ".")}
	default:
		return nil, fmt.Errorf("record type %s is not supported for ArvanCloud here (A, AAAA, CNAME, ANAME, NS, MX, TXT, SRV, CAA, PTR)", in.Type)
	}
	b["value"] = value
	return b, nil
}

func (a *arvancloud) Create(ctx context.Context, z *Zone, in RecordInput) (*Record, error) {
	b, err := a.body(z, in)
	if err != nil {
		return nil, err
	}
	var r arRecord
	if _, err := a.call(ctx, "POST", "/domains/"+url.PathEscape(z.Name)+"/dns-records", b, &r); err != nil {
		return nil, err
	}
	rec := r.record(z.Name)
	return &rec, nil
}

func (a *arvancloud) Update(ctx context.Context, z *Zone, cur *Record, in RecordInput) (*Record, error) {
	b, err := a.body(z, in)
	if err != nil {
		return nil, err
	}
	var r arRecord
	if _, err := a.call(ctx, "PUT", "/domains/"+url.PathEscape(z.Name)+"/dns-records/"+url.PathEscape(cur.ID), b, &r); err != nil {
		return nil, err
	}
	rec := r.record(z.Name)
	if rec.ID == "" {
		rec.ID = cur.ID
	}
	return &rec, nil
}

func (a *arvancloud) Delete(ctx context.Context, z *Zone, id string) error {
	_, err := a.call(ctx, "DELETE", "/domains/"+url.PathEscape(z.Name)+"/dns-records/"+url.PathEscape(id), nil, nil)
	return err
}
