package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/net/proxy"

	"opsdeck/internal/connect"
)

// Web access levels: the default comes from App settings → Web access, and
// each workspace can lower or raise it.
const (
	WebOff    = "off"
	WebSearch = "search" // web_search only
	WebFetch  = "fetch"  // web_search, web_fetch and software_lookup
)

func webAllows(level, need string) bool {
	switch need {
	case WebSearch:
		return level == WebSearch || level == WebFetch
	case WebFetch:
		return level == WebFetch
	}
	return false
}

// WebConfig is the agent's access to the internet.
type WebConfig struct {
	Access string // default level for workspaces ("" = fetch)
	// Search backend: duckduckgo (no key), searxng (SearchURL), brave or
	// tavily (Key).
	Search    string
	SearchURL string
	Key       func() (string, error)
	// Proxy for web requests: socks5://host:port or http://host:port.
	Proxy string
	// Domains read without asking, besides docDomains unless NoDefaultDomains.
	Domains          []string
	NoDefaultDomains bool
	// Endpoints replaces service URLs (tests): duckduckgo, brave, tavily,
	// endoflife, osv, github.
	Endpoints map[string]string
	// AllowPrivate lets requests reach private and loopback addresses (tests).
	AllowPrivate bool
}

func (c WebConfig) endpoint(name, def string) string {
	if v := c.Endpoints[name]; v != "" {
		return v
	}
	return def
}

// SearchLabel names the search backend for the user.
func (c WebConfig) SearchLabel() string {
	switch c.Search {
	case "searxng":
		return "SearXNG (" + c.SearchURL + ")"
	case "brave":
		return "Brave Search"
	case "tavily":
		return "Tavily"
	}
	return "DuckDuckGo"
}

// docDomains are documentation and reference sites the agent reads without
// asking (their subdomains too).
var docDomains = []string{
	"kubernetes.io", "docs.docker.com", "docker.com", "nginx.org", "nginx.com", "github.com", "raw.githubusercontent.com",
	"gist.githubusercontent.com", "gitlab.com", "docs.gitlab.com", "docs.github.com", "man7.org", "manpages.debian.org",
	"manpages.ubuntu.com", "wiki.archlinux.org", "docs.ansible.com", "helm.sh", "prometheus.io", "grafana.com",
	"postgresql.org", "dev.mysql.com", "mariadb.com", "mariadb.org", "mongodb.com", "redis.io", "docs.aws.amazon.com",
	"learn.microsoft.com", "cloud.google.com", "developer.hashicorp.com", "terraform.io", "traefik.io", "caddyserver.com",
	"haproxy.com", "haproxy.org", "letsencrypt.org", "eff.org", "ubuntu.com", "debian.org", "redhat.com", "fedoraproject.org",
	"almalinux.org", "rockylinux.org", "wireguard.com", "tailscale.com", "netbird.io", "openvpn.net", "systemd.io",
	"freedesktop.org", "kernel.org", "go.dev", "python.org", "nodejs.org", "npmjs.com", "pypi.org", "stackoverflow.com",
	"serverfault.com", "superuser.com", "stackexchange.com", "askubuntu.com", "endoflife.date", "osv.dev", "nist.gov",
	"cve.org", "mitre.org", "istio.io", "cilium.io", "readthedocs.io", "k3s.io", "rancher.com", "elastic.co",
	"opensearch.org", "rabbitmq.com", "apache.org", "cloudflare.com", "digitalocean.com", "hetzner.com", "openssl.org",
	"opentelemetry.io", "jaegertracing.io", "vectordotdev.github.io", "fluentbit.io", "argoproj.github.io", "fluxcd.io",
	"podman.io", "containerd.io", "etcd.io", "coredns.io", "cert-manager.io", "victoriametrics.com", "thanos.io",
	"proxmox.com", "zabbix.com", "mikrotik.com", "fortinet.com", "cisco.com", "pfsense.org", "opnsense.org",
}

// AllowedDomain reports whether pages of host are read without asking.
func (c WebConfig) AllowedDomain(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	list := c.Domains
	if !c.NoDefaultDomains {
		list = append(append([]string(nil), c.Domains...), docDomains...)
	}
	for _, d := range list {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "*."))
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

// webClient makes the agent's requests to the internet. Every connection
// goes through guardedDial: it only reaches public addresses, so a page or
// a redirect cannot point the agent at opsdeck itself, this computer or the
// local network.
type webClient struct {
	cfg   WebConfig
	via   func(ctx context.Context) (connect.DialFunc, error) // a workspace network (nil = direct or cfg.Proxy)
	cache *pageCache
}

const webUserAgent = "Mozilla/5.0 (compatible; opsdeck-agent/1.0; +https://github.com/vahidid/opsdeck)"

func (c *webClient) client(ctx context.Context) (*http.Client, error) {
	base, proxied, err := c.baseDial(ctx)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		DialContext:           guardedDial(base, proxied, c.cfg.AllowPrivate),
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 6 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to %s refused", req.URL.Scheme)
		}
		return nil
	}}, nil
}

// baseDial is how connections leave this computer: through the workspace's
// network, the configured proxy, or directly.
func (c *webClient) baseDial(ctx context.Context) (connect.DialFunc, bool, error) {
	if c.via != nil {
		d, err := c.via(ctx)
		return d, true, err
	}
	if c.cfg.Proxy == "" {
		var d net.Dialer
		return d.DialContext, false, nil
	}
	u, err := url.Parse(c.cfg.Proxy)
	if err != nil || u.Host == "" {
		return nil, false, fmt.Errorf("invalid web proxy %q (socks5://host:port or http://host:port)", c.cfg.Proxy)
	}
	var fwd net.Dialer
	switch u.Scheme {
	case "socks5", "socks5h", "socks":
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, &fwd)
		if err != nil {
			return nil, false, err
		}
		cd, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, false, errors.New("SOCKS dialer without context support")
		}
		return cd.DialContext, true, nil
	case "http", "https":
		return connectDialer(u), true, nil
	}
	return nil, false, fmt.Errorf("unsupported web proxy scheme %q", u.Scheme)
}

// connectDialer tunnels connections through an HTTP proxy with CONNECT.
func connectDialer(p *url.URL) connect.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", p.Host)
		if err != nil {
			return nil, fmt.Errorf("web proxy: %w", err)
		}
		if dl, ok := ctx.Deadline(); ok {
			conn.SetDeadline(dl)
		}
		req := &http.Request{Method: "CONNECT", URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
		if p.User != nil {
			pw, _ := p.User.Password()
			req.SetBasicAuth(p.User.Username(), pw)
			req.Header.Set("Proxy-Authorization", req.Header.Get("Authorization"))
			req.Header.Del("Authorization")
		}
		if err := req.Write(conn); err != nil {
			conn.Close()
			return nil, err
		}
		br := bufio.NewReader(conn)
		res, err := http.ReadResponse(br, req)
		if err != nil {
			conn.Close()
			return nil, err
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			conn.Close()
			return nil, fmt.Errorf("web proxy refused CONNECT %s: %s", addr, res.Status)
		}
		conn.SetDeadline(time.Time{})
		if br.Buffered() > 0 {
			return &bufferedConn{Conn: conn, r: br}, nil
		}
		return conn, nil
	}
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// errPrivate explains why an address is refused.
type errPrivate struct{ host, ip string }

func (e errPrivate) Error() string {
	at := e.host
	if e.ip != "" && e.ip != e.host {
		at += " (" + e.ip + ")"
	}
	return "refused: " + at + " is not a public internet address. The web tools only read public sites; reach the workspace's own panels and APIs with http_request, and hosts with exec"
}

// guardedDial resolves the name itself and only connects to public
// addresses (directly it dials the checked address, so the name cannot
// resolve differently in between). Through a proxy or VPN the other side
// gets the name, and a name that does not resolve here is left to it (local
// DNS may be filtered), unless it looks internal.
func guardedDial(base connect.DialFunc, proxied, allowPrivate bool) connect.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if allowPrivate {
			return base(ctx, network, addr)
		}
		if ip := net.ParseIP(host); ip != nil {
			if blockedIP(ip) {
				return nil, errPrivate{host, ""}
			}
			return base(ctx, network, addr)
		}
		if internalName(host) {
			return nil, errPrivate{host, ""}
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			if proxied {
				return base(ctx, network, addr)
			}
			return nil, err
		}
		for _, ip := range ips {
			if blockedIP(ip.IP) {
				return nil, errPrivate{host, ip.IP.String()}
			}
		}
		if proxied {
			// Proxies often filter by name: give them the name we checked.
			return base(ctx, network, addr)
		}
		var last error
		for _, ip := range ips {
			conn, err := base(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("%s has no address", host)
		}
		return nil, last
	}
}

var blockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "2001:db8::/32", "fec0::/10"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// blockedIP reports whether ip is not a public unicast address.
func blockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
		if v4.Equal(net.IPv4bcast) {
			return true
		}
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// internalName reports names that only make sense on a local network.
func internalName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.Contains(h, ".") || h == "localhost" {
		return true
	}
	for _, s := range []string{".local", ".localhost", ".internal", ".lan", ".home", ".corp", ".intranet", ".home.arpa", ".localdomain", ".test", ".invalid"} {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// pages

const maxPageBytes = 5 << 20

type webPage struct {
	URL         string `json:"url"`
	Final       string `json:"final"`
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Title       string `json:"title"`
	Text        string `json:"-"`
	Converted   bool   `json:"converted"` // HTML turned into Markdown
}

type pageCache struct {
	mu sync.Mutex
	m  map[string]cachedPage
}

type cachedPage struct {
	p  *webPage
	at time.Time
}

const pageTTL = 10 * time.Minute

func (c *pageCache) get(key string) *webPage {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok && time.Since(e.at) < pageTTL {
		return e.p
	}
	return nil
}

func (c *pageCache) put(key string, p *webPage) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedPage{}
	}
	if len(c.m) >= 64 {
		for k, e := range c.m {
			if time.Since(e.at) >= pageTTL || len(c.m) >= 64 {
				delete(c.m, k)
			}
		}
	}
	c.m[key] = cachedPage{p, time.Now()}
}

// fetch reads a page (from the cache for a while, so paging through it does
// not fetch it again). HTML becomes Markdown unless raw.
func (c *webClient) fetch(ctx context.Context, u string, raw bool) (*webPage, error) {
	key := fmt.Sprintf("%v|%s", raw, u)
	if p := c.cache.get(key); p != nil {
		return p, nil
	}
	hc, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/markdown,text/plain;q=0.9,application/json;q=0.9,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en,*;q=0.5")
	res, err := hc.Do(req)
	if err != nil {
		var ep errPrivate
		if errors.As(err, &ep) {
			return nil, ep
		}
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxPageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxPageBytes {
		body = body[:maxPageBytes]
	}
	p := &webPage{URL: u, Final: res.Request.URL.String(), Status: res.StatusCode}
	ct := res.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(body)
	}
	mt, _, _ := mime.ParseMediaType(ct)
	p.ContentType = mt
	if r, err := charset.NewReader(bytes.NewReader(body), ct); err == nil {
		if b, err := io.ReadAll(r); err == nil {
			body = b
		}
	}
	switch {
	case mt == "text/html" || mt == "application/xhtml+xml":
		if raw {
			p.Text = string(body)
		} else {
			base, _ := url.Parse(p.Final)
			p.Title, p.Text = htmlToMarkdown(string(body), base)
			p.Converted = true
		}
	case strings.Contains(mt, "json"):
		var buf bytes.Buffer
		if json.Indent(&buf, body, "", "  ") == nil {
			p.Text = buf.String()
		} else {
			p.Text = string(body)
		}
	case mt == "application/pdf":
		return nil, fmt.Errorf("%s is a PDF document (%d bytes); reading PDFs is not supported — look for an HTML version of the page", p.Final, len(body))
	default:
		if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
			return nil, fmt.Errorf("%s is binary content (%s, %d bytes), not a page", p.Final, firstNonEmpty(mt, "unknown type"), len(body))
		}
		p.Text = string(body)
	}
	if res.StatusCode >= 400 {
		excerpt := strings.TrimSpace(p.Text)
		if len(excerpt) > 600 {
			excerpt = excerpt[:600] + "…"
		}
		return nil, fmt.Errorf("HTTP %s from %s\n%s", res.Status, p.Final, excerpt)
	}
	c.cache.put(key, p)
	return p, nil
}

// ---------------------------------------------------------------------------
// search

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Domain  string `json:"domain"`
}

func (c *webClient) search(ctx context.Context, query string, n int) ([]searchResult, error) {
	hc, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	key := func() (string, error) {
		if c.cfg.Key == nil {
			return "", fmt.Errorf("%s needs an API key (App settings → Web access)", c.cfg.SearchLabel())
		}
		k, err := c.cfg.Key()
		if err == nil && k == "" {
			err = fmt.Errorf("%s needs an API key (App settings → Web access)", c.cfg.SearchLabel())
		}
		return k, err
	}
	var out []searchResult
	switch c.cfg.Search {
	case "searxng":
		if c.cfg.SearchURL == "" {
			return nil, errors.New("set the SearXNG address in App settings → Web access")
		}
		u := strings.TrimRight(c.cfg.SearchURL, "/") + "/search?" + url.Values{"q": {query}, "format": {"json"}}.Encode()
		var r struct {
			Results []struct{ URL, Title, Content string }
		}
		if err := getJSON(ctx, hc, "GET", u, nil, nil, &r); err != nil {
			return nil, fmt.Errorf("SearXNG: %w (the instance must allow format=json)", err)
		}
		for _, x := range r.Results {
			out = append(out, searchResult{Title: x.Title, URL: x.URL, Snippet: x.Content})
		}
	case "brave":
		k, err := key()
		if err != nil {
			return nil, err
		}
		u := c.cfg.endpoint("brave", "https://api.search.brave.com/res/v1/web/search") + "?" + url.Values{"q": {query}, "count": {fmt.Sprint(min(n, 20))}}.Encode()
		var r struct {
			Web struct {
				Results []struct{ URL, Title, Description string }
			}
		}
		if err := getJSON(ctx, hc, "GET", u, map[string]string{"X-Subscription-Token": k}, nil, &r); err != nil {
			return nil, fmt.Errorf("Brave Search: %w", err)
		}
		for _, x := range r.Web.Results {
			out = append(out, searchResult{Title: x.Title, URL: x.URL, Snippet: stripTags(x.Description)})
		}
	case "tavily":
		k, err := key()
		if err != nil {
			return nil, err
		}
		var r struct {
			Results []struct{ URL, Title, Content string }
		}
		body := map[string]any{"query": query, "max_results": min(n, 20), "search_depth": "basic"}
		if err := getJSON(ctx, hc, "POST", c.cfg.endpoint("tavily", "https://api.tavily.com/search"), map[string]string{"Authorization": "Bearer " + k}, body, &r); err != nil {
			return nil, fmt.Errorf("Tavily: %w", err)
		}
		for _, x := range r.Results {
			out = append(out, searchResult{Title: x.Title, URL: x.URL, Snippet: x.Content})
		}
	default:
		out, err = duckduckgo(ctx, hc, c.cfg.endpoint("duckduckgo", "https://html.duckduckgo.com/html/"), query)
		if err != nil {
			return nil, fmt.Errorf("DuckDuckGo: %w", err)
		}
	}
	var clean []searchResult
	seen := map[string]bool{}
	for _, r := range out {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		r.Domain = strings.TrimPrefix(u.Hostname(), "www.")
		r.Title = strings.TrimSpace(collapseSpace(r.Title))
		r.Snippet = strings.TrimSpace(collapseSpace(r.Snippet))
		if len(r.Snippet) > 400 {
			r.Snippet = r.Snippet[:400] + "…"
		}
		clean = append(clean, r)
		if len(clean) >= n {
			break
		}
	}
	return clean, nil
}

func getJSON(ctx context.Context, hc *http.Client, method, u string, headers map[string]string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", webUserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return fmt.Errorf("%s: %s", res.Status, msg)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("unexpected answer: %v", err)
	}
	return nil
}

// duckduckgo reads the results of DuckDuckGo's HTML page (no key needed).
func duckduckgo(ctx context.Context, hc *http.Client, endpoint, query string) ([]searchResult, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(url.Values{"q": {query}, "kl": {"wt-wt"}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", webUserAgent)
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", res.Status)
	}
	doc, err := html.Parse(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var out []searchResult
	walk(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode || !hasClass(n, "result") || hasClass(n, "result--ad") {
			return true
		}
		var r searchResult
		if a := find(n, func(x *html.Node) bool { return isTag(x, "a") && hasClass(x, "result__a") }); a != nil {
			r.Title = textOf(a)
			r.URL = ddgTarget(attr(a, "href"))
		}
		if s := find(n, func(x *html.Node) bool { return hasClass(x, "result__snippet") }); s != nil {
			r.Snippet = textOf(s)
		}
		if r.URL != "" {
			out = append(out, r)
		}
		return false
	})
	if len(out) == 0 && strings.Contains(strings.ToLower(textOf(doc)), "captcha") {
		return nil, errors.New("DuckDuckGo asked for a captcha; try again later or set another search backend in App settings → Web access")
	}
	return out, nil
}

// ddgTarget unwraps DuckDuckGo's redirect links (//duckduckgo.com/l/?uddg=…).
func ddgTarget(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if t := u.Query().Get("uddg"); t != "" {
		return t
	}
	return href
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func stripTags(s string) string {
	if !strings.Contains(s, "<") {
		return html.UnescapeString(s)
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	return textOf(doc)
}
