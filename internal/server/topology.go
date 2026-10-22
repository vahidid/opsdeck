package server

import (
	"errors"
	"net/http"

	"opsdeck/internal/topology"
)

func (s *Server) topologyRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/topology/catalog", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"nodeTypes": topology.NodeTypes, "linkKinds": topology.LinkKinds,
			"zoneKinds": topology.ZoneKinds, "deploymentKinds": topology.DeploymentKinds, "providers": topology.Providers})
	})
	mux.HandleFunc("GET /api/workspaces/{ws}/diagram", u(s.topoGet))
	mux.HandleFunc("PUT /api/workspaces/{ws}/diagram", u(s.topoPut))
	mux.HandleFunc("GET /api/workspaces/{ws}/diagram/export", u(s.topoExport))
	mux.HandleFunc("POST /api/workspaces/{ws}/diagram/import-inventory", u(s.topoImport))
}

func (s *Server) topoGet(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	t, rev, err := topology.Load(ws)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"topology": t, "rev": rev, "text": topology.Render(t, ws.Resources)})
}

func (s *Server) topoPut(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Topology topology.Topology `json:"topology"`
		Rev      string            `json:"rev"`
		Force    bool              `json:"force"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	expect := in.Rev
	if in.Force {
		expect = ""
	}
	rev, err := topology.Save(ws, &in.Topology, expect)
	if errors.Is(err, topology.ErrConflict) {
		writeJSON(w, 409, apiError{Error: err.Error(), Code: "conflict"})
		return
	}
	s.record(r, ws.Name(), "topology.save", "topology", map[string]any{"nodes": len(in.Topology.Nodes), "links": len(in.Topology.Links)}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rev": rev, "text": topology.Render(&in.Topology, ws.Resources)})
}

// topoExport downloads the topology for an AI or a person: Markdown (a
// description plus the YAML) or the raw YAML.
func (s *Server) topoExport(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	t, _, err := topology.Load(ws)
	if err != nil {
		fail(w, err)
		return
	}
	t.Layout = nil
	y, _ := topology.Marshal(t)
	if r.URL.Query().Get("format") == "yaml" {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+ws.Name()+`-topology.yaml"`)
		w.Write(y)
		return
	}
	md := "# Infrastructure topology: " + ws.Name() + "\n\n" +
		"Zones contain components; each component lists its vendor/technologies, inventory link and what is deployed on it; " +
		"connections give protocol, port and encryption.\n\n" + topology.Render(t, ws.Resources) +
		"\n## Source (topology.yaml)\n\n```yaml\n" + string(y) + "```\n"
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+ws.Name()+`-topology.md"`)
	w.Write([]byte(md))
}

// topoImport returns the topology with inventory resources added (not saved).
func (s *Server) topoImport(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Topology topology.Topology `json:"topology"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	added := topology.ImportInventory(&in.Topology, ws)
	if added == nil {
		added = []string{}
	}
	writeJSON(w, 200, map[string]any{"topology": in.Topology, "added": added})
}
