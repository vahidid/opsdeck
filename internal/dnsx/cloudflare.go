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

// cloudflare talks to the Cloudflare API v4 with an API token (Zone: Read,
// DNS: Edit).
type cloudflare struct{ api *api }

type cfEnvelope struct {
	Success    bool            `json:"success"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		PerPage    int `json:"per_page"`
		TotalPages int `json:"total_pages"`
		TotalCount int `json:"total_count"`
	} `json:"result_info"`
	Errors []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *cloudflare) call(ctx context.Context, method, path string, body any, out any) (*cfEnvelope, error) {
	var env cfEnvelope
	if _, err := c.api.do(ctx, method, path, body, &env); err != nil {
		return nil, err
	}
	if !env.Success {
		var msgs []string
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%s (code %d)", e.Message, e.Code))
		}
		if len(msgs) == 0 {
			msgs = []string{"the API reported a failure"}
		}
		return nil, errors.New(strings.Join(msgs, "; "))
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("unexpected answer from Cloudflare: %v", err)
		}
	}
	return &env, nil
}

type cfZone struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Paused      bool      `json:"paused"`
	NameServers []string  `json:"name_servers"`
	OriginalNS  []string  `json:"original_name_servers"`
	ModifiedOn  time.Time `json:"modified_on"`
	Plan        struct {
		Name string `json:"name"`
	} `json:"plan"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
}

func (z cfZone) zone() Zone {
	return Zone{ID: z.ID, Name: z.Name, Status: z.Status, Paused: z.Paused, Plan: z.Plan.Name, NameServers: nonNilStrings(z.NameServers),
		CurrentNameServers: z.OriginalNS, Account: z.Account.Name, Modified: z.ModifiedOn,
		URL: "https://dash.cloudflare.com/?to=/:account/" + url.PathEscape(z.Name) + "/dns/records"}
}

func (c *cloudflare) Verify(ctx context.Context) (string, int, error) {
	var tok struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if _, err := c.call(ctx, "GET", "/user/tokens/verify", nil, &tok); err != nil {
		return "", 0, fmt.Errorf("the token was not accepted: %w", err)
	}
	if tok.Status != "" && tok.Status != "active" {
		return "", 0, fmt.Errorf("the token is %s", tok.Status)
	}
	zones, err := c.Zones(ctx, "")
	if err != nil {
		return "", 0, fmt.Errorf("the token cannot list zones (it needs Zone: Read): %w", err)
	}
	seen := map[string]bool{}
	var accounts []string
	for _, z := range zones {
		if z.Account != "" && !seen[z.Account] {
			seen[z.Account] = true
			accounts = append(accounts, z.Account)
		}
	}
	return strings.Join(accounts, ", "), len(zones), nil
}

func (c *cloudflare) Zones(ctx context.Context, query string) ([]Zone, error) {
	out := []Zone{}
	q := strings.ToLower(strings.TrimSpace(query))
	for page := 1; page <= 20; page++ {
		var list []cfZone
		env, err := c.call(ctx, "GET", "/zones?per_page=50&order=name&page="+strconv.Itoa(page), nil, &list)
		if err != nil {
			return nil, err
		}
		for _, z := range list {
			if q == "" || strings.Contains(strings.ToLower(z.Name), q) {
				out = append(out, z.zone())
			}
		}
		if page >= env.ResultInfo.TotalPages || len(list) == 0 {
			break
		}
	}
	return out, nil
}

func (c *cloudflare) Zone(ctx context.Context, name string) (*Zone, error) {
	var list []cfZone
	if _, err := c.call(ctx, "GET", "/zones?name="+url.QueryEscape(strings.ToLower(name)), nil, &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("zone %s is not in this Cloudflare account (or the token cannot see it)", name)
	}
	z := list[0].zone()
	return &z, nil
}

type cfRecord struct {
	ID         string          `json:"id,omitempty"`
	Type       string          `json:"type"`
	Name       string          `json:"name"`
	Content    string          `json:"content,omitempty"`
	Proxiable  bool            `json:"proxiable,omitempty"`
	Proxied    *bool           `json:"proxied,omitempty"`
	TTL        int             `json:"ttl,omitempty"`
	Priority   *int            `json:"priority,omitempty"`
	Comment    string          `json:"comment,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	ModifiedOn time.Time       `json:"modified_on,omitempty"`
}

func (r cfRecord) record(zone string) Record {
	out := Record{ID: r.ID, Type: NormalizeType(r.Type), Name: Relative(r.Name, zone), FQDN: strings.ToLower(r.Name), Content: r.Content,
		TTL: r.TTL, Proxiable: r.Proxiable, Comment: r.Comment, Modified: r.ModifiedOn}
	if r.Proxied != nil {
		out.Proxied = *r.Proxied
	}
	if r.Priority != nil {
		out.Priority = *r.Priority
	}
	switch out.Type {
	case "SRV":
		var d struct {
			Priority, Weight, Port int
			Target                 string
		}
		if json.Unmarshal(r.Data, &d) == nil && d.Target != "" {
			out.Priority = d.Priority
			out.Content = fmt.Sprintf("%d %d %s", d.Weight, d.Port, d.Target)
		}
	case "CAA":
		var d struct {
			Flags int
			Tag   string
			Value string
		}
		if json.Unmarshal(r.Data, &d) == nil && d.Tag != "" {
			out.Content = fmt.Sprintf("%d %s %q", d.Flags, d.Tag, d.Value)
		}
	}
	if out.Content == "" && len(r.Data) > 0 {
		var raw any
		json.Unmarshal(r.Data, &raw)
		out.Raw = raw
		out.Content = string(r.Data)
	}
	return out
}

func (c *cloudflare) Records(ctx context.Context, z *Zone) ([]Record, error) {
	out := []Record{}
	for page := 1; page <= 100; page++ {
		var list []cfRecord
		env, err := c.call(ctx, "GET", "/zones/"+url.PathEscape(z.ID)+"/dns_records?per_page=100&page="+strconv.Itoa(page), nil, &list)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			out = append(out, r.record(z.Name))
		}
		if page >= env.ResultInfo.TotalPages || len(list) == 0 {
			break
		}
	}
	Sort(out)
	return out, nil
}

// body builds the create/update body of a record.
func (c *cloudflare) body(z *Zone, in RecordInput) (map[string]any, error) {
	if err := Validate(&in, z.Name); err != nil {
		return nil, err
	}
	b := map[string]any{"type": in.Type, "name": FQDN(in.Name, z.Name), "ttl": 1}
	if in.TTL > 1 {
		b["ttl"] = in.TTL
	}
	if in.Comment != "" {
		b["comment"] = in.Comment
	}
	if Proxiable(in.Type) {
		b["proxied"] = in.Proxied != nil && *in.Proxied
	}
	switch in.Type {
	case "SRV":
		weight, port, target, err := SRV(in.Content)
		if err != nil {
			return nil, err
		}
		b["data"] = map[string]any{"priority": in.Priority, "weight": weight, "port": port, "target": target}
	case "CAA":
		flags, tag, value, err := CAA(in.Content)
		if err != nil {
			return nil, err
		}
		b["data"] = map[string]any{"flags": flags, "tag": tag, "value": value}
	case "MX":
		b["content"] = strings.TrimSuffix(in.Content, ".")
		b["priority"] = in.Priority
	case "A", "AAAA":
		ips := strings.FieldsFunc(in.Content, func(r rune) bool { return r == ',' || r == ' ' })
		if len(ips) != 1 {
			return nil, errors.New("Cloudflare takes one address per record: add one record per address")
		}
		b["content"] = ips[0]
	default:
		b["content"] = in.Content
	}
	return b, nil
}

func (c *cloudflare) Create(ctx context.Context, z *Zone, in RecordInput) (*Record, error) {
	b, err := c.body(z, in)
	if err != nil {
		return nil, err
	}
	var r cfRecord
	if _, err := c.call(ctx, "POST", "/zones/"+url.PathEscape(z.ID)+"/dns_records", b, &r); err != nil {
		return nil, err
	}
	rec := r.record(z.Name)
	return &rec, nil
}

func (c *cloudflare) Update(ctx context.Context, z *Zone, cur *Record, in RecordInput) (*Record, error) {
	b, err := c.body(z, in)
	if err != nil {
		return nil, err
	}
	var r cfRecord
	if _, err := c.call(ctx, "PUT", "/zones/"+url.PathEscape(z.ID)+"/dns_records/"+url.PathEscape(cur.ID), b, &r); err != nil {
		return nil, err
	}
	rec := r.record(z.Name)
	return &rec, nil
}

func (c *cloudflare) Delete(ctx context.Context, z *Zone, id string) error {
	_, err := c.call(ctx, "DELETE", "/zones/"+url.PathEscape(z.ID)+"/dns_records/"+url.PathEscape(id), nil, nil)
	return err
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
