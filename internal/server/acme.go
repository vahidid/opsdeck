package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/acmex"
	"opsdeck/internal/audit"
	"opsdeck/internal/workspace"
)

// Certificates: the Certificate resources of a workspace, issued and renewed
// from ACME CAs with dns-01 through the DNS connectors, stored in the vault
// and deployed to hosts from there.

func (s *Server) acmeRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/acme/cas", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, acmex.CAs) })
	C := "/api/workspaces/{ws}/certificates"
	mux.HandleFunc("GET "+C, u(s.certList))
	mux.HandleFunc("GET "+C+"/jobs", u(s.certJobs))
	mux.HandleFunc("GET "+C+"/jobs/{id}", u(s.certJob))
	mux.HandleFunc("GET "+C+"/{name}", u(s.certGet))
	mux.HandleFunc("POST "+C+"/{name}/issue", u(s.certIssue))
	mux.HandleFunc("POST "+C+"/{name}/deploy", u(s.certDeploy))
}

func (s *Server) certList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	out := []acmex.Status{}
	for _, res := range ws.Resources {
		if res.Kind == "Certificate" {
			out = append(out, s.acme.Status(ws, res))
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) certificate(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, *workspace.Resource, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, nil, false
	}
	res := ws.Find("Certificate", r.PathValue("name"))
	if res == nil {
		writeErr(w, 404, "certificate not found")
		return nil, nil, false
	}
	return ws, res, true
}

// CertDetail is a certificate with its public chain.
type CertDetail struct {
	acmex.Status
	Chain string `json:"chain,omitempty"`
}

func (s *Server) certGet(w http.ResponseWriter, r *http.Request) {
	ws, res, ok := s.certificate(w, r)
	if !ok {
		return
	}
	d := CertDetail{Status: s.acme.Status(ws, res)}
	if d.Issued {
		if it, _, err := s.vault.Resolve(d.Item); err == nil {
			d.Chain = it.Get("certificate")
		}
	}
	writeJSON(w, 200, d)
}

func (s *Server) certIssue(w http.ResponseWriter, r *http.Request) {
	ws, res, ok := s.certificate(w, r)
	if !ok {
		return
	}
	j, err := s.acme.Start(ws, res, "user")
	if err != nil {
		fail(w, err)
		return
	}
	s.record(r, ws.Name(), "certificate.issue", "certificate/"+res.Name, map[string]any{"job": j.ID, "domains": res.List("domains")}, nil)
	writeJSON(w, 200, j.View(0))
}

func (s *Server) certJobs(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	list := s.acme.Jobs(ws.Name())
	if list == nil {
		list = []acmex.JobView{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) certJob(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	j, found := s.acme.Job(r.PathValue("id"))
	if !found || j.Workspace != ws.Name() {
		writeErr(w, 404, "job not found (it may have finished a while ago)")
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	if r.URL.Query().Get("wait") != "" && !j.Done() {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		j.Wait(ctx)
	}
	writeJSON(w, 200, j.View(since))
}

// certDeploy writes a certificate to a host and optionally runs a command
// there afterwards (reload the server).
func (s *Server) certDeploy(w http.ResponseWriter, r *http.Request) {
	ws, res, ok := s.certificate(w, r)
	if !ok {
		return
	}
	var in struct {
		Host    string `json:"host"`
		Command string `json:"command"`
		acmex.DeployOptions
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	host := ws.Find("Host", strings.TrimPrefix(in.Host, "host/"))
	if host == nil {
		writeErr(w, 400, "host not found")
		return
	}
	if err := in.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	written, err := s.acme.Deploy(ctx, s.hub, ws, res.Name, host, in.DeployOptions)
	detail := map[string]any{"host": host.Name, "files": written}
	out := map[string]any{"written": written}
	if err == nil && strings.TrimSpace(in.Command) != "" {
		detail["command"] = in.Command
		exec, xerr := s.hub.Exec(ctx, ws, host, in.Command, nil, 64<<10)
		out["exitCode"], out["stdout"], out["stderr"] = exec.ExitCode, exec.Stdout, exec.Stderr
		if xerr != nil {
			err = xerr
		} else if exec.ExitCode != 0 {
			err = errors.New("the command exited with " + strconv.Itoa(exec.ExitCode))
		}
		detail["exitCode"] = exec.ExitCode
	}
	s.record(r, ws.Name(), "certificate.deploy", "certificate/"+res.Name, detail, err)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 502, out)
		return
	}
	writeJSON(w, 200, out)
}

// acmeAudit records finished issuances (also the automatic renewals).
func (s *Server) acmeAudit(ws, actor, certName string, detail map[string]any, err error) {
	e := audit.Entry{Workspace: ws, Actor: actor, Action: "certificate.issued", Target: "certificate/" + certName, Detail: detail}
	if err != nil {
		e.Result = "error"
		if e.Detail == nil {
			e.Detail = map[string]any{}
		}
		e.Detail["error"] = err.Error()
	}
	s.audit.Record(e)
}
