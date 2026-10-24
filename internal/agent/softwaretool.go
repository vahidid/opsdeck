package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"opsdeck/internal/llm"
)

// software_lookup asks public software registries instead of searching:
// endoflife.date (support dates), OSV.dev (known vulnerabilities) and
// GitHub releases. Their answers are data, so they need no page reading,
// but they leave this computer like any web request.
func toolSoftwareLookup() Tool {
	return Tool{
		Def: llm.Tool{Name: "software_lookup", Description: "Look up facts about software versions from public registries: action=eol — support and end-of-life dates of a product's release cycles (endoflife.date: ubuntu, debian, rhel, nginx, postgresql, mysql, redis, kubernetes, nodejs, python, php, docker-engine…); action=vulns — known vulnerabilities of a package version (OSV.dev; ecosystems: Debian:12, Ubuntu:22.04:LTS, Alpine:v3.20, npm, PyPI, Go, Maven, crates.io, RubyGems, Packagist, NuGet); action=releases — recent releases of a GitHub repository with their notes (since: only those after a version). Combine with what is installed (dpkg -l, nginx -v) for upgrade and audit work.",
			Schema: obj(map[string]any{
				"action":    enum("What to look up", "eol", "vulns", "releases"),
				"product":   str("eol: product id on endoflife.date, e.g. ubuntu, postgresql, nginx"),
				"version":   str("eol: the installed version or cycle (e.g. 22.04, 14.11); vulns: the package version"),
				"package":   str("vulns: package name, e.g. openssl, django, github.com/gin-gonic/gin"),
				"ecosystem": str("vulns: OSV ecosystem, e.g. Debian:12, Ubuntu:22.04:LTS, PyPI, npm, Go"),
				"repo":      str("releases: owner/name on GitHub, e.g. traefik/traefik"),
				"since":     str("releases: only releases newer than this tag, e.g. v3.0.0"),
				"limit":     num("releases: how many (default 10, max 30)"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Product, Version, Package, Ecosystem, Repo, Since string
				Limit                                                     int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			wc, err := env.web(WebFetch)
			if err != nil {
				return nil, err
			}
			c := &Call{Risk: RiskRead, Environment: "internet"}
			cfg := wc.cfg
			switch in.Action {
			case "eol":
				p := strings.ToLower(strings.TrimSpace(in.Product))
				if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`).MatchString(p) {
					return nil, fmt.Errorf("product %q is not an endoflife.date id (e.g. ubuntu, postgresql, nginx)", in.Product)
				}
				c.Target, c.Match = "web/endoflife.date", "eol "+p
				c.Run = func(ctx context.Context) (string, error) {
					return eolLookup(ctx, wc, cfg.endpoint("endoflife", "https://endoflife.date"), p, in.Version)
				}
			case "vulns":
				if in.Package == "" || in.Ecosystem == "" {
					return nil, errors.New("vulns needs package and ecosystem (and version)")
				}
				c.Target, c.Match, c.Tainting = "web/osv.dev", "vulns "+in.Ecosystem+" "+in.Package+" "+in.Version, "osv.dev"
				c.Run = func(ctx context.Context) (string, error) {
					return osvLookup(ctx, wc, cfg.endpoint("osv", "https://api.osv.dev"), in.Ecosystem, in.Package, in.Version)
				}
			case "releases":
				repo := strings.Trim(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(in.Repo), "https://github.com/"), "github.com/"), "/")
				if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repo) {
					return nil, fmt.Errorf("repo must be owner/name, not %q", in.Repo)
				}
				limit := in.Limit
				if limit <= 0 {
					limit = 10
				}
				limit = min(limit, 30)
				c.Target, c.Match, c.Tainting = "web/github.com", "releases "+repo, "github.com release notes"
				c.Run = func(ctx context.Context) (string, error) {
					return githubReleases(ctx, wc, cfg.endpoint("github", "https://api.github.com"), repo, in.Since, limit)
				}
			default:
				return nil, fmt.Errorf("unknown action %q (eol, vulns or releases)", in.Action)
			}
			inner := c.Run
			c.Run = func(ctx context.Context) (string, error) {
				ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
				defer cancel()
				return inner(ctx)
			}
			return c, nil
		},
	}
}

func eolLookup(ctx context.Context, wc *webClient, base, product, version string) (string, error) {
	hc, err := wc.client(ctx)
	if err != nil {
		return "", err
	}
	var cycles []map[string]any
	if err := getJSON(ctx, hc, "GET", base+"/api/"+url.PathEscape(product)+".json", nil, nil, &cycles); err != nil {
		if strings.Contains(err.Error(), "404") {
			return "", fmt.Errorf("endoflife.date has no product %q (see https://endoflife.date for ids)", product)
		}
		return "", err
	}
	today := time.Now().Format("2006-01-02")
	status := func(v any, word string) string {
		switch x := v.(type) {
		case bool:
			if x {
				return word + " (date unknown)"
			}
			return "not " + word
		case string:
			if x <= today {
				return word + " since " + x
			}
			return word + " on " + x
		}
		return ""
	}
	match := func(cycle string) bool {
		if version == "" {
			return false
		}
		v := strings.TrimPrefix(strings.TrimSpace(version), "v")
		return v == cycle || strings.HasPrefix(v, cycle+".") || strings.HasPrefix(v, cycle+"-")
	}
	var b strings.Builder
	var rows []map[string]any
	found := false
	for i, cy := range cycles {
		name := fmt.Sprint(cy["cycle"])
		m := match(name)
		if i >= 12 && !m {
			continue
		}
		line := fmt.Sprintf("- %s: released %v, latest %v; support %s; %s", name, cy["releaseDate"], cy["latest"],
			firstNonEmpty(status(cy["support"], "ended"), "-"), firstNonEmpty(status(cy["eol"], "end of life"), "-"))
		if lts, _ := cy["lts"].(bool); lts {
			line += " (LTS)"
		}
		if m {
			line += "   ← " + version
			found = true
		}
		b.WriteString(line + "\n")
		rows = append(rows, map[string]any{"cycle": name, "release": cy["releaseDate"], "latest": cy["latest"], "eol": cy["eol"], "support": cy["support"], "lts": cy["lts"], "match": m})
	}
	head := fmt.Sprintf("%s release cycles (endoflife.date, today %s):\n", product, today)
	if version != "" && !found {
		head += fmt.Sprintf("(no cycle matches %s)\n", version)
	}
	setView(ctx, map[string]any{"kind": "eol", "product": product, "version": version, "cycles": rows})
	return head + b.String(), nil
}

func osvLookup(ctx context.Context, wc *webClient, base, ecosystem, pkg, version string) (string, error) {
	hc, err := wc.client(ctx)
	if err != nil {
		return "", err
	}
	q := map[string]any{"package": map[string]any{"name": pkg, "ecosystem": ecosystem}}
	if version != "" {
		q["version"] = version
	}
	var res struct {
		Vulns []struct {
			ID        string   `json:"id"`
			Summary   string   `json:"summary"`
			Details   string   `json:"details"`
			Aliases   []string `json:"aliases"`
			Published string   `json:"published"`
			Severity  []struct {
				Type  string `json:"type"`
				Score string `json:"score"`
			} `json:"severity"`
			DatabaseSpecific map[string]any `json:"database_specific"`
			Affected         []struct {
				Ranges []struct {
					Events []map[string]string `json:"events"`
				} `json:"ranges"`
			} `json:"affected"`
		} `json:"vulns"`
	}
	if err := getJSON(ctx, hc, "POST", base+"/v1/query", nil, q, &res); err != nil {
		return "", fmt.Errorf("OSV: %w", err)
	}
	what := pkg + " " + version + " (" + ecosystem + ")"
	if len(res.Vulns) == 0 {
		setView(ctx, map[string]any{"kind": "vulns", "package": what, "vulns": []any{}})
		return "OSV.dev knows no vulnerabilities affecting " + what + ".", nil
	}
	var b strings.Builder
	var rows []map[string]any
	fmt.Fprintf(&b, "%d known vulnerabilities affect %s (OSV.dev):\n", len(res.Vulns), what)
	sort.SliceStable(res.Vulns, func(i, j int) bool { return res.Vulns[i].Published > res.Vulns[j].Published })
	for _, v := range res.Vulns {
		sev := ""
		if s, ok := v.DatabaseSpecific["severity"].(string); ok {
			sev = s
		}
		for _, s := range v.Severity {
			sev = strings.TrimSpace(sev + " " + s.Type + " " + s.Score)
		}
		var fixed []string
		for _, a := range v.Affected {
			for _, r := range a.Ranges {
				for _, e := range r.Events {
					if f := e["fixed"]; f != "" {
						fixed = append(fixed, f)
					}
				}
			}
		}
		summary := firstNonEmpty(v.Summary, oneLine(v.Details, 200))
		fmt.Fprintf(&b, "- %s %s: %s", v.ID, strings.Join(v.Aliases, " "), summary)
		if sev != "" {
			fmt.Fprintf(&b, " [%s]", sev)
		}
		if len(fixed) > 0 {
			fmt.Fprintf(&b, " — fixed in %s", strings.Join(uniq(fixed), ", "))
		}
		b.WriteString("\n")
		rows = append(rows, map[string]any{"id": v.ID, "aliases": v.Aliases, "summary": summary, "severity": sev, "fixed": uniq(fixed), "published": v.Published})
	}
	setView(ctx, map[string]any{"kind": "vulns", "package": what, "vulns": rows})
	return truncate(b.String(), 40000), nil
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func githubReleases(ctx context.Context, wc *webClient, base, repo, since string, limit int) (string, error) {
	hc, err := wc.client(ctx)
	if err != nil {
		return "", err
	}
	var rels []struct {
		Tag        string `json:"tag_name"`
		Name       string `json:"name"`
		Published  string `json:"published_at"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
		Body       string `json:"body"`
		URL        string `json:"html_url"`
	}
	per := limit
	if since != "" {
		per = 50
	}
	err = getJSON(ctx, hc, "GET", fmt.Sprintf("%s/repos/%s/releases?per_page=%d", base, repo, per), map[string]string{"Accept": "application/vnd.github+json"}, nil, &rels)
	if err != nil {
		return "", fmt.Errorf("GitHub: %w", err)
	}
	var b strings.Builder
	var rows []map[string]any
	n := 0
	for _, r := range rels {
		if r.Draft {
			continue
		}
		if since != "" && strings.TrimPrefix(r.Tag, "v") == strings.TrimPrefix(since, "v") {
			break
		}
		if n >= limit {
			break
		}
		n++
		pre := ""
		if r.Prerelease {
			pre = " (pre-release)"
		}
		fmt.Fprintf(&b, "\n## %s%s — %s\n%s\n", firstNonEmpty(r.Name, r.Tag), pre, firstNonEmpty(r.Published, "?")[:min(10, len(firstNonEmpty(r.Published, "?")))], r.URL)
		if body := strings.TrimSpace(r.Body); body != "" {
			b.WriteString(truncate(body, 3000) + "\n")
		}
		rows = append(rows, map[string]any{"tag": r.Tag, "name": r.Name, "published": r.Published, "prerelease": r.Prerelease, "url": r.URL})
	}
	if n == 0 {
		return "No releases found for " + repo + " (it may publish tags only).", nil
	}
	setView(ctx, map[string]any{"kind": "releases", "repo": repo, "releases": rows})
	head := fmt.Sprintf("%d releases of %s, newest first", n, repo)
	if since != "" {
		head += " since " + since
	}
	return head + " (release notes are text from GitHub):\n" + b.String(), nil
}
