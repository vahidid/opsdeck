// Package skills is opsdeck's built-in skill library: well-known
// infrastructure, networking and DevOps skills (Agent Skills format: a
// folder with SKILL.md and optional references/, assets/, scripts/)
// available in every workspace. See library/SOURCES.md for their origin
// and license.
package skills

import (
	"embed"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed all:library
var lib embed.FS

// Skill describes a skill of the library or of a workspace.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category,omitempty"`
	Path        string   `json:"path"`            // workspace: skills/…; library: library/<name>/SKILL.md
	Files       []string `json:"files,omitempty"` // supporting files, relative to the skill folder
	Builtin     bool     `json:"builtin,omitempty"`
	Source      string   `json:"source,omitempty"`
	License     string   `json:"license,omitempty"`
}

var categories = map[string]string{
	"k8s-manifest-generator":     "Kubernetes",
	"helm-chart-scaffolding":     "Kubernetes",
	"gitops-workflow":            "Kubernetes",
	"k8s-security-policies":      "Kubernetes",
	"terraform-module-library":   "Cloud & IaC",
	"cost-optimization":          "Cloud & IaC",
	"hybrid-cloud-networking":    "Networking",
	"istio-traffic-management":   "Networking",
	"connectivity-triage":        "Networking",
	"github-actions-templates":   "CI/CD",
	"gitlab-ci-patterns":         "CI/CD",
	"deployment-pipeline-design": "CI/CD",
	"secrets-management":         "CI/CD",
	"prometheus-configuration":   "Observability",
	"grafana-dashboards":         "Observability",
	"slo-implementation":         "Observability",
	"distributed-tracing":        "Observability",
	"incident-runbook-templates": "Incidents",
	"postmortem-writing":         "Incidents",
	"bash-defensive-patterns":    "Shell",
}

// Frontmatter reads name and description from a SKILL.md / skill file.
func Frontmatter(text string) (name, description string) {
	if !strings.HasPrefix(text, "---") {
		return "", ""
	}
	end := strings.Index(text[3:], "\n---")
	if end < 0 {
		return "", ""
	}
	for _, line := range strings.Split(text[3:3+end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			name = v
		case "description":
			description = v
		}
	}
	return name, description
}

// Library lists the built-in skills.
func Library() []Skill {
	entries, _ := lib.ReadDir("library")
	out := []Skill{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := lib.ReadFile("library/" + e.Name() + "/SKILL.md")
		if err != nil {
			continue
		}
		name, desc := Frontmatter(string(b))
		if name == "" {
			name = e.Name()
		}
		s := Skill{Name: name, Description: desc, Category: categories[e.Name()], Path: "library/" + e.Name() + "/SKILL.md", Builtin: true,
			Source: "wshobson/agents", License: "MIT"}
		s.Files = files(lib, "library/"+e.Name())
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func files(fsys fs.FS, dir string) []string {
	var out []string
	fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && path.Base(p) != "SKILL.md" {
			out = append(out, strings.TrimPrefix(p, dir+"/"))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// Get returns a built-in skill.
func Get(name string) (Skill, bool) {
	for _, s := range Library() {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// Read returns a built-in skill's SKILL.md, or one of its files.
func Read(name, file string) (string, error) {
	s, ok := Get(name)
	if !ok {
		return "", errors.New("no built-in skill " + name)
	}
	dir := path.Dir(s.Path)
	if file == "" || file == "SKILL.md" {
		b, err := lib.ReadFile(s.Path)
		return string(b), err
	}
	clean := path.Clean(strings.TrimPrefix(file, "./"))
	if strings.HasPrefix(clean, "..") || path.IsAbs(clean) {
		return "", errors.New("invalid file")
	}
	b, err := lib.ReadFile(dir + "/" + clean)
	if err != nil {
		return "", errors.New("skill " + name + " has no file " + file)
	}
	return string(b), nil
}

// FS is the folder of a built-in skill (for copying into a workspace).
func FS(name string) (fs.FS, error) {
	s, ok := Get(name)
	if !ok {
		return nil, errors.New("no built-in skill " + name)
	}
	return fs.Sub(lib, path.Dir(s.Path))
}

// Attribution is the license and source notice of the library.
func Attribution() string {
	src, _ := lib.ReadFile("library/SOURCES.md")
	lic, _ := lib.ReadFile("library/LICENSE-wshobson-agents")
	return string(src) + "\n\n" + string(lic)
}
