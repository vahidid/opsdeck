package server

import (
	"net/http"

	"opsdeck/internal/vpn"
)

// vpnInstallStatus answers GET /api/vpn/install: with kind+mode it is the
// plan for that driver (what would run), without them the state of the
// running or finished install job.
func (s *Server) vpnInstallStatus(w http.ResponseWriter, r *http.Request) {
	if kind, mode := r.URL.Query().Get("kind"), r.URL.Query().Get("mode"); kind != "" && mode != "" {
		plan, err := vpn.PlanInstall(kind, mode, s.elevation())
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, plan)
		return
	}
	writeJSON(w, 200, vpn.GetInstallStatus())
}

// vpnInstallStart answers POST /api/vpn/install: run the plan for a driver
// in the background (one install at a time).
func (s *Server) vpnInstallStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
		Mode string `json:"mode"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := vpn.StartInstall(in.Kind, in.Mode, s.elevation()); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, vpn.GetInstallStatus())
}
