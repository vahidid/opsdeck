package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// apiKinds are resources the agent calls over HTTP.
var apiKinds = []string{"Panel", "Prometheus", "Loki", "Elasticsearch"}

// apiResource finds an HTTP API resource by name (or kind/name); with an
// empty name it takes the only one of the given kinds.
func (e *Env) apiResource(name string, kinds ...string) (*workspace.Resource, error) {
	if len(kinds) == 0 {
		kinds = apiKinds
	}
	name = strings.TrimSpace(name)
	var all []*workspace.Resource
	for _, r := range e.WS.Resources {
		for _, k := range kinds {
			if r.Kind == k {
				all = append(all, r)
			}
		}
	}
	if name == "" {
		if len(all) == 1 {
			return all[0], nil
		}
	}
	if kind, n, ok := strings.Cut(name, "/"); ok {
		for _, r := range all {
			if strings.EqualFold(r.Kind, kind) && r.Name == n {
				return r, nil
			}
		}
		name = n
	}
	for _, r := range all {
		if r.Name == name {
			return r, nil
		}
	}
	var names []string
	for _, r := range all {
		names = append(names, r.Kind+"/"+r.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("the workspace has no %s", strings.Join(kinds, " or "))
	}
	if name == "" {
		return nil, fmt.Errorf("name one of: %s", strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("%q not found; available: %s", name, strings.Join(names, ", "))
}

// apiCredentials are the vault references a call to r uses.
func (e *Env) apiCredentials(r *workspace.Resource) []string {
	refs := e.creds(r)
	for _, k := range []string{"token", "login"} {
		if v := r.Str(k); v != "" {
			refs = append(refs, v)
		}
	}
	return refs
}

// apiClient is an HTTP client that reaches r the way r is reached (its via
// chain) and a function that adds r's credentials to a request. Redirects
// are not followed, so credentials never go to another server.
func (e *Env) apiClient(ctx context.Context, r *workspace.Resource) (*http.Client, func(*http.Request) error, error) {
	dial, err := e.Hub.Dialer(ctx, e.WS, r)
	if err != nil {
		return nil, nil, err
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: r.Bool("insecure", false)}
	if ca := strings.TrimSpace(r.Str("caCert")); ca != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, nil, fmt.Errorf("%s: the CA certificate is not valid PEM", r.ID())
		}
		tlsCfg.RootCAs = pool
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { return dial(ctx, n, a) },
		TLSClientConfig: tlsCfg, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second, ForceAttemptHTTP2: true}
	hc := &http.Client{Transport: tr, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	auth := func(req *http.Request) error {
		if ref := r.Str("token"); ref != "" {
			tok, err := e.secretField(ref, "token")
			if err != nil {
				return err
			}
			switch h := strings.TrimSpace(r.Str("authHeader")); {
			case h != "" && !strings.EqualFold(h, "Authorization"):
				req.Header.Set(h, tok)
			case r.Kind == "Elasticsearch" && !strings.Contains(tok, " "):
				req.Header.Set("Authorization", "ApiKey "+tok)
			case strings.Contains(tok, " "):
				req.Header.Set("Authorization", tok) // a full "Scheme value"
			default:
				req.Header.Set("Authorization", "Bearer "+tok)
			}
		} else if ref := r.Str("login"); ref != "" {
			it, _, err := e.Vault.ResolveFor(e.WS.Name(), ref)
			if err != nil {
				return err
			}
			if !it.Policy.AgentUse {
				return fmt.Errorf("credential %s is not allowed for agent use", ref)
			}
			req.SetBasicAuth(it.Get("username"), it.Get("password"))
		}
		if t := r.Str("tenant"); t != "" {
			req.Header.Set("X-Scope-OrgID", t)
		}
		return nil
	}
	return hc, auth, nil
}

// secretField reads a field of a vault item the agent may use.
func (e *Env) secretField(ref, def string) (string, error) {
	it, field, err := e.Vault.ResolveFor(e.WS.Name(), ref)
	if err != nil {
		return "", err
	}
	if !it.Policy.AgentUse {
		return "", fmt.Errorf("credential %s is not allowed for agent use", ref)
	}
	if field == "" {
		field = def
	}
	v := it.Get(field)
	if v == "" && field == "token" {
		v = it.Get("password")
	}
	if v == "" {
		return "", fmt.Errorf("credential %s has no %s", ref, field)
	}
	return v, nil
}

// apiURL joins a path (with its query) to the resource's base URL. The path
// stays under the base: it cannot name another server.
func apiURL(base, path string, query map[string]any) (*url.URL, error) {
	b, err := url.Parse(strings.TrimSpace(base))
	if err != nil || b.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q", base)
	}
	p, err := url.Parse(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("invalid path %q", path)
	}
	if p.Scheme != "" || p.Host != "" || strings.HasPrefix(path, "//") {
		return nil, errors.New("path must be relative to the resource's URL (e.g. /api/health), not a full URL")
	}
	u := *b
	if p.Path != "" {
		u.Path = strings.TrimRight(b.Path, "/") + "/" + strings.TrimLeft(p.Path, "/")
		u.RawPath = ""
	}
	q := b.Query()
	for k, v := range p.Query() {
		q[k] = v
	}
	for k, v := range query {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				q.Add(k, fmt.Sprint(item))
			}
		default:
			q.Set(k, fmt.Sprint(x))
		}
	}
	u.RawQuery = q.Encode()
	return &u, nil
}

// methodRisk classifies an HTTP request: reads are safe methods; DELETE is
// destructive; everything else changes something.
func methodRisk(method string) string {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return RiskRead
	case "DELETE":
		return RiskDestructive
	}
	return RiskChange
}

var safeHeader = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func toolHTTPRequest() Tool {
	return Tool{
		Def: llm.Tool{Name: "http_request", Description: "Call the HTTP API of a workspace web panel or service (Panel, Prometheus, Loki, Elasticsearch resources: Grafana, Alertmanager, Consul, Harbor, Proxmox, Jenkins, Traefik, an app's admin API…) through its VPN or jump host, logged in with its vault login or token — you never see the credentials. path is relative to the resource's URL. GET, HEAD and OPTIONS are reads; POST, PUT and PATCH are changes and DELETE is destructive, so they wait for approval. Redirects are returned, not followed.",
			Schema: obj(map[string]any{
				"resource":  str("Resource name (or kind/name), e.g. grafana"),
				"method":    enum("HTTP method (default GET)", "GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"),
				"path":      str("Path and query relative to the resource's URL, e.g. /api/health or /api/search?query=nginx"),
				"query":     map[string]any{"type": "object", "description": "Optional query parameters"},
				"headers":   map[string]any{"type": "object", "description": "Optional extra headers (not Authorization or Cookie: opsdeck adds the credentials)"},
				"json":      map[string]any{"description": "JSON body (sent with Content-Type: application/json)"},
				"body":      str("Raw body, instead of json"),
				"max_bytes": num("Maximum response bytes (default 200000, max 2000000)"),
			}, "resource")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Resource, Method, Path, Body string
				Query                        map[string]any
				Headers                      map[string]any
				JSON                         json.RawMessage
				MaxBytes                     int `json:"max_bytes"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			r, err := env.apiResource(in.Resource)
			if err != nil {
				return nil, err
			}
			method := strings.ToUpper(firstNonEmpty(strings.TrimSpace(in.Method), "GET"))
			if !regexp.MustCompile(`^[A-Z]+$`).MatchString(method) {
				return nil, fmt.Errorf("invalid method %q", in.Method)
			}
			u, err := apiURL(r.Str("url"), in.Path, in.Query)
			if err != nil {
				return nil, err
			}
			headers := http.Header{}
			for k, v := range in.Headers {
				if !safeHeader.MatchString(k) {
					return nil, fmt.Errorf("invalid header name %q", k)
				}
				switch strings.ToLower(k) {
				case "authorization", "cookie", "proxy-authorization", "x-scope-orgid", "host":
					return nil, fmt.Errorf("header %s is set by opsdeck", k)
				}
				headers.Set(k, fmt.Sprint(v))
			}
			var body []byte
			switch {
			case len(in.JSON) > 0 && string(in.JSON) != "null":
				body = in.JSON
				if headers.Get("Content-Type") == "" {
					headers.Set("Content-Type", "application/json")
				}
			case in.Body != "":
				body = []byte(in.Body)
			}
			max := in.MaxBytes
			if max <= 0 {
				max = 200000
			}
			max = min(max, 2000000)
			envName, protected := targetInfo(env.WS, r)
			match := method + " " + u.EscapedPath()
			if u.RawQuery != "" {
				match += "?" + u.RawQuery
			}
			preview := method + " " + u.String()
			if len(body) > 0 {
				var pretty bytes.Buffer
				if json.Indent(&pretty, body, "", "  ") == nil {
					body := pretty.String()
					preview += "\n\n" + truncate(body, 6000)
				} else {
					preview += "\n\n" + truncate(string(body), 6000)
				}
			}
			risk := methodRisk(method)
			return &Call{Risk: risk, Target: strings.ToLower(r.Kind) + "/" + r.Name, Environment: envName, Protected: protected, Match: match,
				Preview: preview, Credentials: env.apiCredentials(r),
				Run: func(ctx context.Context) (string, error) {
					hc, auth, err := env.apiClient(ctx, r)
					if err != nil {
						return "", err
					}
					req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
					if err != nil {
						return "", err
					}
					req.Header = headers.Clone()
					if req.Header.Get("Accept") == "" {
						req.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.5")
					}
					req.Header.Set("User-Agent", "opsdeck-agent")
					if err := auth(req); err != nil {
						return "", err
					}
					start := time.Now()
					res, err := hc.Do(req)
					if err != nil {
						return "", err
					}
					defer res.Body.Close()
					raw, _ := io.ReadAll(io.LimitReader(res.Body, int64(max)+1))
					cut := len(raw) > max
					if cut {
						raw = raw[:max]
					}
					ms := time.Since(start).Milliseconds()
					ct, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
					setView(ctx, map[string]any{"kind": "http", "method": method, "url": u.String(), "status": res.StatusCode, "durationMs": ms, "contentType": ct, "bytes": len(raw)})
					var b strings.Builder
					fmt.Fprintf(&b, "HTTP %s (%dms)\n", res.Status, ms)
					for _, h := range []string{"Content-Type", "Location", "Content-Length", "Retry-After", "WWW-Authenticate"} {
						if v := res.Header.Get(h); v != "" {
							fmt.Fprintf(&b, "%s: %s\n", h, v)
						}
					}
					b.WriteString("\n")
					b.WriteString(formatBody(raw, ct))
					if cut {
						fmt.Fprintf(&b, "\n... [response cut at %d bytes]\n", max)
					}
					if res.StatusCode >= 400 {
						return b.String(), fmt.Errorf("HTTP %d", res.StatusCode)
					}
					return b.String(), nil
				}}, nil
		},
	}
}

// formatBody shows a response body: JSON indented, text as it is.
func formatBody(raw []byte, ct string) string {
	if len(raw) == 0 {
		return "(empty body)"
	}
	if t := bytes.TrimSpace(raw); strings.Contains(ct, "json") || (len(t) > 0 && (t[0] == '{' || t[0] == '[')) {
		var buf bytes.Buffer
		if json.Indent(&buf, raw, "", "  ") == nil {
			return buf.String()
		}
	}
	if strings.Contains(ct, "html") {
		_, md := htmlToMarkdown(string(raw), nil)
		if md != "" {
			return md
		}
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return fmt.Sprintf("(binary content, %d bytes, %s)", len(raw), firstNonEmpty(ct, "unknown type"))
	}
	return string(raw)
}

// apiGet calls a JSON API of r and decodes the answer.
func (e *Env) apiGet(ctx context.Context, r *workspace.Resource, method, path string, query map[string]any, body any, out any) error {
	u, err := apiURL(r.Str("url"), path, query)
	if err != nil {
		return err
	}
	hc, auth, err := e.apiClient(ctx, r)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := auth(req); err != nil {
		return err
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(b))
		var pe struct{ Error, Reason string }
		if json.Unmarshal(b, &pe) == nil && firstNonEmpty(pe.Error, pe.Reason) != "" {
			msg = firstNonEmpty(pe.Error, pe.Reason)
		}
		if len(msg) > 600 {
			msg = msg[:600] + "…"
		}
		return fmt.Errorf("%s: %s", res.Status, msg)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("unexpected answer from %s: %v", r.ID(), err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// time ranges

// parseWhen reads a time: RFC 3339, a Unix timestamp, "now", or a duration
// before now ("1h", "30m", "2d").
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "now" {
		return now, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 1e9 {
		sec, frac := math.Modf(f)
		return time.Unix(int64(sec), int64(frac*1e9)), nil
	}
	d, err := parseDuration(strings.TrimPrefix(strings.TrimPrefix(s, "now-"), "-"))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q: use RFC 3339, a Unix time or a duration before now like 1h or 2d", s)
	}
	return now.Add(-d), nil
}

// parseDuration also accepts days and weeks (2d, 1w).
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	for unit, mult := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(s, unit); ok {
			if f, err := strconv.ParseFloat(n, 64); err == nil {
				return time.Duration(f * float64(mult)), nil
			}
		}
	}
	return time.ParseDuration(s)
}

func timeRange(start, end, since string, def time.Duration) (time.Time, time.Time, error) {
	now := time.Now()
	to, err := parseWhen(end, now)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	var from time.Time
	switch {
	case start != "":
		if from, err = parseWhen(start, now); err != nil {
			return time.Time{}, time.Time{}, err
		}
	case since != "":
		d, err := parseDuration(since)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid since %q (e.g. 15m, 1h, 2d)", since)
		}
		from = to.Add(-d)
	default:
		from = to.Add(-def)
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("start must be before end")
	}
	return from, to, nil
}

// ---------------------------------------------------------------------------
// metrics

func toolMetricsQuery() Tool {
	return Tool{
		Def: llm.Tool{Name: "metrics_query", Description: "Query a workspace Prometheus (or a compatible API: VictoriaMetrics, Thanos, Mimir) through its VPN or jump host. action=query runs PromQL: instant by default, a range when since or start is given (returns per-series min/avg/max/last and the chart in the chat). Other actions: series (match a selector), labels, label_values (label), alerts (firing alerts), rules, targets (scrape health). Read-only.",
			Schema: obj(map[string]any{
				"source": str("Prometheus resource name (default: the workspace's only one)"),
				"action": enum("What to do (default query)", "query", "series", "labels", "label_values", "alerts", "rules", "targets"),
				"query":  str("PromQL, e.g. sum by (instance) (rate(node_cpu_seconds_total{mode!=\"idle\"}[5m]))"),
				"since":  str("Range query over the last duration, e.g. 1h, 6h, 2d"),
				"start":  str("Range start (RFC 3339, Unix time or a duration before now)"),
				"end":    str("Range end / instant time (default now)"),
				"step":   str("Range resolution, e.g. 30s, 5m (default: about 120 points)"),
				"label":  str("label_values: the label name, e.g. job"),
				"match":  str("series: a selector, e.g. up{job=\"node\"}"),
			})},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Source, Action, Query, Since, Start, End, Step, Label, Match string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			r, err := env.apiResource(in.Source, "Prometheus")
			if err != nil {
				return nil, err
			}
			action := firstNonEmpty(in.Action, "query")
			if action == "query" && strings.TrimSpace(in.Query) == "" {
				return nil, errors.New("query is empty")
			}
			envName, protected := targetInfo(env.WS, r)
			return &Call{Risk: RiskRead, Target: "prometheus/" + r.Name, Environment: envName, Protected: protected, Match: action + " " + in.Query,
				Credentials: env.apiCredentials(r), Run: func(ctx context.Context) (string, error) {
					ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
					defer cancel()
					return promAction(ctx, env, r, action, in.Query, in.Since, in.Start, in.End, in.Step, in.Label, in.Match)
				}}, nil
		},
	}
}

type promResp struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	Error     string          `json:"error"`
	Warnings  []string        `json:"warnings"`
	ErrorType string          `json:"errorType"`
}

func promAction(ctx context.Context, env *Env, r *workspace.Resource, action, query, since, start, end, step, label, match string) (string, error) {
	var res promResp
	call := func(path string, q map[string]any) error {
		if err := env.apiGet(ctx, r, "GET", path, q, nil, &res); err != nil {
			return err
		}
		if res.Status != "success" {
			return fmt.Errorf("%s: %s", res.ErrorType, res.Error)
		}
		return nil
	}
	warn := func(s string) string {
		if len(res.Warnings) > 0 {
			s += "\nwarnings: " + strings.Join(res.Warnings, "; ")
		}
		return s
	}
	switch action {
	case "query":
		if since == "" && start == "" {
			q := map[string]any{"query": query}
			if end != "" {
				t, err := parseWhen(end, time.Now())
				if err != nil {
					return "", err
				}
				q["time"] = t.Unix()
			}
			if err := call("/api/v1/query", q); err != nil {
				return "", err
			}
			return warn(renderPromInstant(ctx, query, res.Data)), nil
		}
		from, to, err := timeRange(start, end, since, time.Hour)
		if err != nil {
			return "", err
		}
		st := step
		if st == "" {
			st = fmt.Sprintf("%ds", max(15, int(to.Sub(from).Seconds()/120)))
		}
		if err := call("/api/v1/query_range", map[string]any{"query": query, "start": from.Unix(), "end": to.Unix(), "step": st}); err != nil {
			return "", err
		}
		return warn(renderPromRange(ctx, query, from, to, st, res.Data)), nil
	case "series":
		sel := firstNonEmpty(match, query)
		if sel == "" {
			return "", errors.New("series needs match (a selector)")
		}
		if err := call("/api/v1/series", map[string]any{"match[]": sel, "start": time.Now().Add(-time.Hour).Unix()}); err != nil {
			return "", err
		}
		var series []map[string]string
		json.Unmarshal(res.Data, &series)
		var b strings.Builder
		fmt.Fprintf(&b, "%d series match %s (last hour):\n", len(series), sel)
		for i, s := range series {
			if i == 200 {
				fmt.Fprintf(&b, "... %d more\n", len(series)-200)
				break
			}
			b.WriteString(promLabels(s) + "\n")
		}
		return b.String(), nil
	case "labels", "label_values":
		path := "/api/v1/labels"
		if action == "label_values" {
			if label == "" {
				return "", errors.New("label_values needs label")
			}
			path = "/api/v1/label/" + url.PathEscape(label) + "/values"
		}
		q := map[string]any{}
		if match != "" {
			q["match[]"] = match
		}
		if err := call(path, q); err != nil {
			return "", err
		}
		var vals []string
		json.Unmarshal(res.Data, &vals)
		return fmt.Sprintf("%d values: %s", len(vals), truncate(strings.Join(vals, ", "), 20000)), nil
	case "alerts":
		if err := call("/api/v1/alerts", nil); err != nil {
			return "", err
		}
		var d struct {
			Alerts []struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
				State       string            `json:"state"`
				ActiveAt    string            `json:"activeAt"`
				Value       string            `json:"value"`
			} `json:"alerts"`
		}
		json.Unmarshal(res.Data, &d)
		if len(d.Alerts) == 0 {
			return "No alerts are pending or firing.", nil
		}
		var b strings.Builder
		var rows []map[string]any
		for _, a := range d.Alerts {
			name := a.Labels["alertname"]
			fmt.Fprintf(&b, "- %s [%s] since %s %s\n", name, a.State, a.ActiveAt, promLabels(a.Labels))
			if s := firstNonEmpty(a.Annotations["summary"], a.Annotations["description"]); s != "" {
				fmt.Fprintf(&b, "  %s\n", s)
			}
			rows = append(rows, map[string]any{"name": name, "state": a.State, "since": a.ActiveAt, "severity": a.Labels["severity"], "summary": firstNonEmpty(a.Annotations["summary"], a.Annotations["description"]), "labels": promLabels(a.Labels)})
		}
		setView(ctx, map[string]any{"kind": "alerts", "alerts": rows})
		return b.String(), nil
	case "rules":
		if err := call("/api/v1/rules", nil); err != nil {
			return "", err
		}
		var d struct {
			Groups []struct {
				Name  string `json:"name"`
				File  string `json:"file"`
				Rules []struct {
					Name   string `json:"name"`
					Type   string `json:"type"`
					Query  string `json:"query"`
					State  string `json:"state"`
					Health string `json:"health"`
				} `json:"rules"`
			} `json:"groups"`
		}
		json.Unmarshal(res.Data, &d)
		var b strings.Builder
		for _, g := range d.Groups {
			fmt.Fprintf(&b, "group %s (%s):\n", g.Name, g.File)
			for _, rl := range g.Rules {
				fmt.Fprintf(&b, "- %s %s [%s%s]: %s\n", rl.Type, rl.Name, rl.Health, strings.TrimSuffix(" "+rl.State, " "), oneLine(rl.Query, 200))
			}
		}
		return truncate(firstNonEmpty(b.String(), "No rules."), 40000), nil
	case "targets":
		if err := call("/api/v1/targets", map[string]any{"state": "active"}); err != nil {
			return "", err
		}
		var d struct {
			ActiveTargets []struct {
				Labels     map[string]string `json:"labels"`
				ScrapeURL  string            `json:"scrapeUrl"`
				Health     string            `json:"health"`
				LastError  string            `json:"lastError"`
				LastScrape string            `json:"lastScrape"`
			} `json:"activeTargets"`
		}
		json.Unmarshal(res.Data, &d)
		up, down := 0, 0
		var bad strings.Builder
		for _, t := range d.ActiveTargets {
			if t.Health == "up" {
				up++
				continue
			}
			down++
			fmt.Fprintf(&bad, "- %s %s: %s %s\n", t.Labels["job"], t.ScrapeURL, t.Health, t.LastError)
		}
		return fmt.Sprintf("%d targets up, %d not up\n%s", up, down, bad.String()), nil
	}
	return "", fmt.Errorf("unknown action %q", action)
}

func promLabels(l map[string]string) string {
	name := l["__name__"]
	keys := make([]string, 0, len(l))
	for k := range l {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Quote(l[k]))
	}
	return name + "{" + strings.Join(parts, ", ") + "}"
}

func renderPromInstant(ctx context.Context, query string, data json.RawMessage) string {
	var d struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	json.Unmarshal(data, &d)
	switch d.ResultType {
	case "vector":
		var v []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		json.Unmarshal(d.Result, &v)
		if len(v) == 0 {
			return "empty result for " + query
		}
		sort.SliceStable(v, func(i, j int) bool { return promFloat(v[i].Value[1]) > promFloat(v[j].Value[1]) })
		var b strings.Builder
		var rows []map[string]any
		fmt.Fprintf(&b, "%d series (highest first):\n", len(v))
		for i, x := range v {
			val := fmt.Sprint(x.Value[1])
			if i < 300 {
				fmt.Fprintf(&b, "%s %s\n", promLabels(x.Metric), val)
			}
			if i < 100 {
				rows = append(rows, map[string]any{"labels": promLabels(x.Metric), "value": val})
			}
		}
		if len(v) > 300 {
			fmt.Fprintf(&b, "... %d more\n", len(v)-300)
		}
		setView(ctx, map[string]any{"kind": "vector", "query": query, "rows": rows, "total": len(v)})
		return b.String()
	case "scalar", "string":
		var v [2]any
		json.Unmarshal(d.Result, &v)
		return fmt.Sprintf("%s = %v", d.ResultType, v[1])
	case "matrix":
		return renderPromRange(ctx, query, time.Time{}, time.Time{}, "", data)
	}
	return string(d.Result)
}

func promFloat(v any) float64 {
	s, _ := v.(string)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) {
		return math.Inf(-1)
	}
	return f
}

func renderPromRange(ctx context.Context, query string, from, to time.Time, step string, data json.RawMessage) string {
	var d struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	}
	json.Unmarshal(data, &d)
	if len(d.Result) == 0 {
		return "empty result for " + query
	}
	type stat struct {
		labels              string
		min, max, avg, last float64
		points              [][2]float64
	}
	var stats []stat
	for _, s := range d.Result {
		st := stat{labels: promLabels(s.Metric), min: math.Inf(1), max: math.Inf(-1)}
		n := 0
		for _, p := range s.Values {
			ts, _ := p[0].(float64)
			f := promFloat(p[1])
			if math.IsInf(f, 0) {
				continue
			}
			st.points = append(st.points, [2]float64{ts, f})
			st.min, st.max = math.Min(st.min, f), math.Max(st.max, f)
			st.avg += f
			st.last = f
			n++
		}
		if n > 0 {
			st.avg /= float64(n)
			stats = append(stats, st)
		}
	}
	sort.SliceStable(stats, func(i, j int) bool { return stats[i].max > stats[j].max })
	var b strings.Builder
	if !from.IsZero() {
		fmt.Fprintf(&b, "%d series from %s to %s (step %s), sorted by max:\n", len(stats), from.Format(time.RFC3339), to.Format(time.RFC3339), step)
	}
	var view []map[string]any
	for i, st := range stats {
		if i < 100 {
			fmt.Fprintf(&b, "%s min=%s avg=%s max=%s last=%s\n", st.labels, fnum(st.min), fnum(st.avg), fnum(st.max), fnum(st.last))
		}
		if i < 12 {
			view = append(view, map[string]any{"labels": st.labels, "points": downsample(st.points, 160), "last": st.last, "max": st.max})
		}
	}
	if len(stats) > 100 {
		fmt.Fprintf(&b, "... %d more series\n", len(stats)-100)
	}
	setView(ctx, map[string]any{"kind": "timeseries", "query": query, "series": view, "total": len(stats), "step": step})
	return b.String()
}

func fnum(f float64) string {
	switch {
	case math.IsInf(f, 0) || math.IsNaN(f):
		return "-"
	case f == math.Trunc(f) && math.Abs(f) < 1e12:
		return strconv.FormatFloat(f, 'f', 0, 64)
	case math.Abs(f) >= 1000:
		return strconv.FormatFloat(f, 'f', 1, 64)
	}
	return strconv.FormatFloat(f, 'g', 4, 64)
}

// downsample keeps at most n points, picking evenly.
func downsample(p [][2]float64, n int) [][2]float64 {
	if len(p) <= n {
		return p
	}
	out := make([][2]float64, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, p[i*(len(p)-1)/(n-1)])
	}
	return out
}

// ---------------------------------------------------------------------------
// logs

var levelRe = regexp.MustCompile(`(?i)\b(fatal|panic|crit(?:ical)?|error|err|warn(?:ing)?|info|debug|trace)\b`)

func logLevel(line string) string {
	m := levelRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	switch l := strings.ToLower(m[1]); {
	case l == "fatal" || l == "panic" || strings.HasPrefix(l, "crit") || l == "error" || l == "err":
		return "error"
	case strings.HasPrefix(l, "warn"):
		return "warn"
	case l == "info":
		return "info"
	}
	return "debug"
}

type logLine struct {
	TS     time.Time `json:"ts"`
	Labels string    `json:"labels,omitempty"`
	Line   string    `json:"line"`
	Level  string    `json:"level,omitempty"`
}

func toolLogsQuery() Tool {
	return Tool{
		Def: llm.Tool{Name: "logs_query", Description: "Search the logs of a workspace Loki (LogQL, e.g. {app=\"shop\"} |= \"error\") or Elasticsearch/OpenSearch (Lucene query string, e.g. service.name:shop AND level:error) through its VPN or jump host. Newest first. action=labels lists Loki labels (or label values with label). Read-only.",
			Schema: obj(map[string]any{
				"source": str("Loki or Elasticsearch resource name (default: the workspace's only one)"),
				"action": enum("What to do (default query)", "query", "labels"),
				"query":  str("LogQL selector and filters (Loki) or a query string (Elasticsearch; empty = everything)"),
				"since":  str("Look back this far (default 1h), e.g. 15m, 6h, 2d"),
				"start":  str("Range start (RFC 3339, Unix time or a duration before now)"),
				"end":    str("Range end (default now)"),
				"limit":  num("Maximum lines (default 100, max 1000)"),
				"index":  str("Elasticsearch: index pattern (default the resource's, else all)"),
				"label":  str("labels: show the values of this label"),
			})},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Source, Action, Query, Since, Start, End, Index, Label string
				Limit                                                  int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			r, err := env.apiResource(in.Source, "Loki", "Elasticsearch")
			if err != nil {
				return nil, err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 100
			}
			limit = min(limit, 1000)
			envName, protected := targetInfo(env.WS, r)
			return &Call{Risk: RiskRead, Target: strings.ToLower(r.Kind) + "/" + r.Name, Environment: envName, Protected: protected,
				Match: firstNonEmpty(in.Action, "query") + " " + in.Query, Credentials: env.apiCredentials(r), Run: func(ctx context.Context) (string, error) {
					ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
					defer cancel()
					from, to, err := timeRange(in.Start, in.End, in.Since, time.Hour)
					if err != nil {
						return "", err
					}
					if r.Kind == "Loki" {
						if in.Action == "labels" {
							return lokiLabels(ctx, env, r, in.Label, from, to)
						}
						if strings.TrimSpace(in.Query) == "" {
							return "", errors.New("Loki needs a LogQL query with a stream selector, e.g. {job=\"nginx\"}")
						}
						lines, err := lokiQuery(ctx, env, r, in.Query, from, to, limit)
						if err != nil {
							return "", err
						}
						return renderLogs(ctx, in.Query, lines, limit, from, to), nil
					}
					if in.Action == "labels" {
						return "", errors.New("labels is for Loki; for Elasticsearch use http_request on /_mapping")
					}
					lines, err := esQuery(ctx, env, r, in.Query, firstNonEmpty(in.Index, r.Str("index"), "*"), from, to, limit)
					if err != nil {
						return "", err
					}
					return renderLogs(ctx, in.Query, lines, limit, from, to), nil
				}}, nil
		},
	}
}

func lokiQuery(ctx context.Context, env *Env, r *workspace.Resource, query string, from, to time.Time, limit int) ([]logLine, error) {
	var res struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	err := env.apiGet(ctx, r, "GET", "/loki/api/v1/query_range", map[string]any{"query": query, "start": from.UnixNano(), "end": to.UnixNano(),
		"limit": limit, "direction": "backward"}, nil, &res)
	if err != nil {
		return nil, err
	}
	if res.Data.ResultType != "" && res.Data.ResultType != "streams" {
		return nil, fmt.Errorf("the query returns %s, not log lines: use a log query (a metric query belongs in Grafana or metrics_query)", res.Data.ResultType)
	}
	var out []logLine
	for _, s := range res.Data.Result {
		labels := promLabels(s.Stream)
		for _, v := range s.Values {
			ns, _ := strconv.ParseInt(v[0], 10, 64)
			out = append(out, logLine{TS: time.Unix(0, ns).UTC(), Labels: labels, Line: v[1], Level: firstNonEmpty(s.Stream["level"], s.Stream["detected_level"], logLevel(v[1]))})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func lokiLabels(ctx context.Context, env *Env, r *workspace.Resource, label string, from, to time.Time) (string, error) {
	path := "/loki/api/v1/labels"
	if label != "" {
		path = "/loki/api/v1/label/" + url.PathEscape(label) + "/values"
	}
	var res struct {
		Data []string `json:"data"`
	}
	if err := env.apiGet(ctx, r, "GET", path, map[string]any{"start": from.UnixNano(), "end": to.UnixNano()}, nil, &res); err != nil {
		return "", err
	}
	what := "labels"
	if label != "" {
		what = "values of " + label
	}
	return fmt.Sprintf("%d %s: %s", len(res.Data), what, truncate(strings.Join(res.Data, ", "), 20000)), nil
}

func esQuery(ctx context.Context, env *Env, r *workspace.Resource, query, index string, from, to time.Time, limit int) ([]logLine, error) {
	tf := firstNonEmpty(r.Str("timeField"), "@timestamp")
	must := []any{}
	if q := strings.TrimSpace(query); q != "" {
		must = append(must, map[string]any{"query_string": map[string]any{"query": q, "default_operator": "AND"}})
	}
	body := map[string]any{
		"size": limit,
		"sort": []any{map[string]any{tf: map[string]any{"order": "desc", "unmapped_type": "date"}}},
		"query": map[string]any{"bool": map[string]any{"must": must,
			"filter": []any{map[string]any{"range": map[string]any{tf: map[string]any{"gte": from.Format(time.RFC3339Nano), "lte": to.Format(time.RFC3339Nano)}}}}}},
	}
	var res struct {
		Hits struct {
			Hits []struct {
				Index  string         `json:"_index"`
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if strings.ContainsAny(index, "/?#") {
		return nil, fmt.Errorf("invalid index pattern %q", index)
	}
	if err := env.apiGet(ctx, r, "POST", "/"+index+"/_search", map[string]any{"ignore_unavailable": "true"}, body, &res); err != nil {
		return nil, err
	}
	var out []logLine
	for _, h := range res.Hits.Hits {
		src := h.Source
		ts, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(lookup(src, tf)))
		msg := firstNonEmpty(str2(lookup(src, "message")), str2(lookup(src, "log")), str2(lookup(src, "msg")), str2(lookup(src, "log.original")))
		if msg == "" {
			b, _ := json.Marshal(src)
			msg = string(b)
		}
		var labels []string
		for _, k := range []string{"service.name", "kubernetes.pod.name", "container.name", "host.name", "app", "kubernetes.namespace"} {
			if v := str2(lookup(src, k)); v != "" {
				labels = append(labels, k+"="+strconv.Quote(v))
			}
		}
		level := firstNonEmpty(str2(lookup(src, "log.level")), str2(lookup(src, "level")), logLevel(msg))
		out = append(out, logLine{TS: ts.UTC(), Labels: h.Index + "{" + strings.Join(labels, ", ") + "}", Line: msg, Level: strings.ToLower(level)})
	}
	return out, nil
}

// lookup finds a dotted field in a document, nested or flat.
func lookup(doc map[string]any, key string) any {
	if v, ok := doc[key]; ok {
		return v
	}
	head, rest, ok := strings.Cut(key, ".")
	if !ok {
		return nil
	}
	if sub, ok := doc[head].(map[string]any); ok {
		return lookup(sub, rest)
	}
	return nil
}

func str2(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return fmt.Sprint(v)
}

func renderLogs(ctx context.Context, query string, lines []logLine, limit int, from, to time.Time) string {
	view := lines
	if len(view) > 500 {
		view = view[:500]
	}
	setView(ctx, map[string]any{"kind": "logs", "query": query, "lines": view, "from": from, "to": to})
	if len(lines) == 0 {
		return fmt.Sprintf("No log lines from %s to %s for %s", from.Format(time.RFC3339), to.Format(time.RFC3339), query)
	}
	var b strings.Builder
	levels := map[string]int{}
	for _, l := range lines {
		levels[l.Level]++
	}
	fmt.Fprintf(&b, "%d lines, newest first", len(lines))
	if len(lines) >= limit {
		b.WriteString(" (limit reached: narrow the query or the time range for older lines)")
	}
	if levels["error"]+levels["warn"] > 0 {
		fmt.Fprintf(&b, "; %d error, %d warn", levels["error"], levels["warn"])
	}
	b.WriteString(":\n")
	last := ""
	for _, l := range lines {
		if l.Labels != last {
			fmt.Fprintf(&b, "--- %s\n", l.Labels)
			last = l.Labels
		}
		fmt.Fprintf(&b, "%s %s\n", l.TS.Format("2006-01-02T15:04:05.000Z"), oneLine(l.Line, 2000))
	}
	return b.String()
}
