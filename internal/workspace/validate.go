package workspace

import (
	"fmt"
	"strings"
)

// Issue is a validation finding.
type Issue struct {
	Level    string `json:"level"` // error | warning
	Resource string `json:"resource"`
	Message  string `json:"message"`
}

// RefChecker reports whether a vault reference resolves (nil = skip vault checks).
type RefChecker func(ref string) error

// Validate checks schema, references and reachability chains.
func (w *Workspace) Validate(check RefChecker) []Issue {
	issues := []Issue{}
	add := func(level string, r *Resource, format string, args ...any) {
		id := ""
		if r != nil {
			id = r.ID()
		}
		issues = append(issues, Issue{Level: level, Resource: id, Message: fmt.Sprintf(format, args...)})
	}
	envs := map[string]bool{}
	for _, e := range w.Environments {
		envs[e.Name] = true
	}
	seen := map[string]bool{}
	for _, r := range w.Resources {
		if seen[r.ID()] {
			add("error", r, "duplicate resource")
		}
		seen[r.ID()] = true
		schema, ok := KindByName(r.Kind)
		if !ok {
			add("error", r, "unknown kind %q", r.Kind)
			continue
		}
		if r.Environment != "" && !envs[r.Environment] {
			add("warning", r, "environment %q is not defined in workspace.yaml", r.Environment)
		}
		for _, f := range schema.Fields {
			if f.Required && r.Str(f.Key) == "" && len(r.List(f.Key)) == 0 {
				add("error", r, "%s is required", f.Label)
			}
			if f.Type == "secretRef" && r.Str(f.Key) != "" {
				ref := r.Str(f.Key)
				if !strings.HasPrefix(ref, "vault://") {
					add("error", r, "%s must be a vault:// reference, never a literal secret", f.Label)
				} else if check != nil {
					if err := check(ref); err != nil {
						add("error", r, "%s: %v", f.Label, err)
					}
				}
			}
			if f.Type == "resourceRef" {
				for _, name := range r.List(f.Key) {
					found := false
					for _, k := range f.RefKinds {
						if w.Find(k, name) != nil {
							found = true
						}
					}
					if !found {
						add("error", r, "%s: %q not found", f.Label, name)
					}
				}
			}
		}
		if r.Via != "" {
			if !schema.CanVia {
				add("warning", r, "%s resources do not use 'via'", r.Kind)
			}
			hop := w.FindRef(r.Via)
			if hop == nil {
				add("error", r, "via %q not found (use host/<name> or network/<name>)", r.Via)
			} else if hop.Kind != "Host" && !isNetworkKind(hop.Kind) {
				add("error", r, "via must point to a host or a network, not %s", hop.Kind)
			}
		}
		if _, err := w.Chain(r); err != nil {
			add("error", r, "%v", err)
		}
		if r.Kind == "Host" && r.Str("auth") == "" && w.Settings.DefaultAuth == "" {
			add("warning", r, "no credential and no workspace default credential")
		}
	}
	if w.Settings.DefaultAuth != "" && check != nil {
		if err := check(w.Settings.DefaultAuth); err != nil {
			add("error", nil, "default credential: %v", err)
		}
	}
	for _, s := range w.MCPServers {
		for k, v := range s.Env {
			if strings.HasPrefix(v, "vault://") && check != nil {
				if err := check(v); err != nil {
					add("error", nil, "MCP server %s env %s: %v", s.Name, k, err)
				}
			}
		}
	}
	return issues
}

// Chain returns the chain of hops needed to reach r, outermost first
// (not including r itself). It detects cycles.
func (w *Workspace) Chain(r *Resource) ([]*Resource, error) {
	var chain []*Resource
	seen := map[string]bool{r.ID(): true}
	cur := r
	for cur.Via != "" {
		hop := w.FindRef(cur.Via)
		if hop == nil {
			return nil, fmt.Errorf("%s: via %q not found", cur.ID(), cur.Via)
		}
		if seen[hop.ID()] {
			return nil, fmt.Errorf("via loop at %s", hop.ID())
		}
		seen[hop.ID()] = true
		chain = append([]*Resource{hop}, chain...)
		cur = hop
	}
	return chain, nil
}

// Topology returns nodes and "reached via" edges.
type TopoNode struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Label       string `json:"label"`
}
type TopoEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (w *Workspace) Topology() ([]TopoNode, []TopoEdge) {
	nodes, edges := []TopoNode{}, []TopoEdge{}
	for _, r := range w.Resources {
		label := r.Str("address")
		if label == "" {
			label = r.Str("url")
		}
		nodes = append(nodes, TopoNode{ID: r.ID(), Kind: r.Kind, Name: r.Name, Environment: r.Environment, Label: label})
		if r.Via != "" {
			if hop := w.FindRef(r.Via); hop != nil {
				edges = append(edges, TopoEdge{From: r.ID(), To: hop.ID()})
			}
		}
		if r.Kind == "HostGroup" {
			for _, m := range w.GroupMembers(r) {
				edges = append(edges, TopoEdge{From: m.ID(), To: r.ID()})
			}
		}
	}
	return nodes, edges
}
