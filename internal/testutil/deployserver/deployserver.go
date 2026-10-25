// Package deployserver fakes the deployment platform APIs opsdeck talks
// to, for tests: Portainer (stacks, the Docker proxy), Argo CD (the REST
// gateway: applications, managed resources, sync, rollback, logs) and
// Komodo (the /read, /execute, /auth RPC endpoints, with v2's GET /user).
// Each checks its own auth header and records what it was asked.
package deployserver

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Token is the API token the Portainer and Argo CD fakes accept; Key and
// Secret are Komodo's. They are random for each test binary, so no
// credential-shaped literal sits in the source.
var Token, Key, Secret = rnd("tok"), rnd("key"), rnd("sec")

func rnd(p string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return p + "-" + hex.EncodeToString(b)
}

// Call is a request the fake received.
type Call struct {
	Method, Path, Query string
	Body                map[string]any
	Raw                 string
}

// Server is one fake platform.
type Server struct {
	URL   string
	mu    sync.Mutex
	Calls []Call
	// Portainer: stack files by id; Argo CD: the last sync revision.
	Files map[string]string
}

func (s *Server) record(r *http.Request) Call {
	b, _ := io.ReadAll(r.Body)
	c := Call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Raw: string(b)}
	json.Unmarshal(b, &c.Body)
	s.mu.Lock()
	s.Calls = append(s.Calls, c)
	s.mu.Unlock()
	return c
}

// Find returns the calls with this method and path.
func (s *Server) Find(method, path string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.Calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// ----------------------------------------------------------------------------
// Portainer

// Portainer starts a fake Portainer 2.21 with the environment "local" (id
// 1) and three compose stacks: shop (id 1, file in Portainer, two
// containers, one unhealthy), billing (id 2, from Git) and other (id 3,
// stopped).
func Portainer(t testing.TB) *Server {
	s := &Server{Files: map[string]string{"1": "services:\n  web:\n    image: shop:1.0\n  worker:\n    image: shop:1.0\n", "2": "services:\n  api:\n    image: billing:2\n", "3": "services: {}\n"}}
	stacks := []map[string]any{
		{"Id": 1, "Name": "shop", "Type": 2, "EndpointId": 1, "Status": 1, "EntryPoint": "docker-compose.yml", "Env": []map[string]string{{"name": "TAG", "value": "1.0"}},
			"CreationDate": 1700000000, "UpdateDate": 1760000000, "UpdatedBy": "admin", "GitConfig": nil},
		{"Id": 2, "Name": "billing", "Type": 2, "EndpointId": 1, "Status": 1, "EntryPoint": "compose.yml", "Env": []any{},
			"CreationDate": 1700000000, "UpdateDate": 0, "GitConfig": map[string]any{"URL": "https://git.example.com/billing.git", "ReferenceName": "refs/heads/main",
				"ConfigFilePath": "compose.yml", "ConfigHash": "0123456789abcdef0123456789abcdef01234567", "Authentication": map[string]any{"Username": "deploy"}}},
		{"Id": 3, "Name": "other", "Type": 2, "EndpointId": 1, "Status": 2, "Env": []any{}, "CreationDate": 1700000000},
	}
	containers := []map[string]any{
		{"Id": "c-web", "Names": []string{"/shop-web-1"}, "Image": "shop:1.0", "State": "running", "Status": "Up 2 hours (healthy)",
			"Labels": map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.service": "web"}},
		{"Id": "c-worker", "Names": []string{"/shop-worker-1"}, "Image": "shop:1.0", "State": "running", "Status": "Up 2 hours (unhealthy)",
			"Labels": map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.service": "worker"}},
		{"Id": "c-api", "Names": []string{"/billing-api-1"}, "Image": "billing:2", "State": "running", "Status": "Up 1 day",
			"Labels": map[string]string{"com.docker.compose.project": "billing", "com.docker.compose.service": "api"}},
	}
	h := func(w http.ResponseWriter, r *http.Request) {
		c := s.record(r)
		if r.Header.Get("X-API-Key") != Token {
			writeJSON(w, 401, map[string]string{"message": "Unauthorized", "details": "invalid API key"})
			return
		}
		p := r.URL.Path
		stack := func(id string) map[string]any {
			for _, st := range stacks {
				if fmt.Sprint(st["Id"]) == id {
					return st
				}
			}
			return nil
		}
		parts := strings.Split(strings.Trim(p, "/"), "/")
		switch {
		case p == "/api/system/version":
			writeJSON(w, 200, map[string]any{"ServerVersion": "2.21.0", "ServerEdition": "CE"})
		case p == "/api/users/me":
			writeJSON(w, 200, map[string]any{"Id": 1, "Username": "admin", "Role": 1})
		case p == "/api/endpoints":
			writeJSON(w, 200, []map[string]any{{"Id": 1, "Name": "local", "Type": 1, "Status": 1}})
		case p == "/api/stacks" && r.Method == "GET":
			writeJSON(w, 200, stacks)
		case len(parts) >= 3 && parts[1] == "stacks":
			st := stack(parts[2])
			if st == nil {
				writeJSON(w, 404, map[string]string{"message": "Unable to find a stack with the specified identifier inside the database", "details": "object not found inside the database"})
				return
			}
			needEP := func() bool {
				if r.URL.Query().Get("endpointId") != "1" {
					writeJSON(w, 400, map[string]string{"message": "Invalid query parameter: endpointId"})
					return false
				}
				return true
			}
			switch {
			case len(parts) == 3 && r.Method == "GET":
				writeJSON(w, 200, st)
			case len(parts) == 4 && parts[3] == "file":
				writeJSON(w, 200, map[string]string{"StackFileContent": s.Files[parts[2]]})
			case len(parts) == 3 && r.Method == "PUT":
				if !needEP() {
					return
				}
				if st["GitConfig"] != nil {
					writeJSON(w, 400, map[string]string{"message": "git stacks cannot be updated with a file"})
					return
				}
				s.mu.Lock()
				s.Files[parts[2]] = fmt.Sprint(c.Body["StackFileContent"])
				s.mu.Unlock()
				writeJSON(w, 200, st)
			case len(parts) == 5 && parts[3] == "git" && parts[4] == "redeploy" && r.Method == "PUT":
				writeJSON(w, 200, st)
			case len(parts) == 4 && (parts[3] == "start" || parts[3] == "stop") && r.Method == "POST":
				if !needEP() {
					return
				}
				if parts[3] == "stop" {
					st["Status"] = 2
				} else {
					st["Status"] = 1
				}
				writeJSON(w, 200, st)
			default:
				http.NotFound(w, r)
			}
		case strings.HasPrefix(p, "/api/endpoints/1/docker/containers/json"):
			var f map[string][]string
			json.Unmarshal([]byte(r.URL.Query().Get("filters")), &f)
			out := []map[string]any{}
			for _, ct := range containers {
				for _, l := range f["label"] {
					k, v, _ := strings.Cut(l, "=")
					if ct["Labels"].(map[string]string)[k] == v {
						out = append(out, ct)
					}
				}
			}
			writeJSON(w, 200, out)
		case strings.HasPrefix(p, "/api/endpoints/1/docker/containers/") && strings.HasSuffix(p, "/logs"):
			id := parts[5]
			w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
			frame := func(typ byte, s string) {
				h := []byte{typ, 0, 0, 0, 0, 0, 0, 0}
				binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
				w.Write(h)
				io.WriteString(w, s)
			}
			frame(1, "2026-10-25T10:00:00Z "+id+" started\n")
			frame(2, "2026-10-25T10:00:01Z "+id+" warning: slow\n")
		case strings.HasPrefix(p, "/api/endpoints/1/docker/containers/") && strings.HasSuffix(p, "/restart") && r.Method == "POST":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// ----------------------------------------------------------------------------
// Argo CD

// state is a JSON string of a Kubernetes object, as Argo CD sends it.
func state(o any) string {
	b, _ := json.Marshal(o)
	return string(b)
}

func deployment(image string) map[string]any {
	return map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "shop-web", "namespace": "shop"},
		"spec": map[string]any{"replicas": 2, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "web", "image": image}}}}}}
}

// ArgoCD starts a fake Argo CD v3 with two applications in the argocd
// namespace: shop (OutOfSync, Healthy, manual sync, two history entries,
// a Deployment whose image differs from Git) and web (Synced, Degraded,
// auto-sync; its resources have no health on the app, as with v3's
// resource tree).
func ArgoCD(t testing.TB) *Server {
	s := &Server{Files: map[string]string{}}
	apps := map[string]map[string]any{
		"shop": {"metadata": map[string]any{"name": "shop", "namespace": "argocd"},
			"spec": map[string]any{"project": "default", "source": map[string]any{"repoURL": "https://git.example.com/deploy.git", "path": "shop", "targetRevision": "main"},
				"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"}},
			"status": map[string]any{"sync": map[string]any{"status": "OutOfSync", "revision": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
				"health":         map[string]any{"status": "Healthy"},
				"operationState": map[string]any{"phase": "Succeeded", "message": "successfully synced (all tasks run)", "startedAt": "2026-10-24T10:00:00Z", "finishedAt": "2026-10-24T10:00:30Z"},
				"history": []any{
					map[string]any{"id": 1, "revision": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "deployedAt": "2026-10-20T10:00:00Z", "deployStartedAt": "2026-10-20T09:59:50Z", "source": map[string]any{"repoURL": "https://git.example.com/deploy.git"}},
					map[string]any{"id": 2, "revision": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "deployedAt": "2026-10-24T10:00:30Z", "initiatedBy": map[string]any{"username": "alice"}},
				},
				"resources": []any{
					map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "shop", "name": "shop-web", "status": "OutOfSync", "health": map[string]any{"status": "Healthy"}},
					map[string]any{"version": "v1", "kind": "Service", "namespace": "shop", "name": "shop-web", "status": "Synced", "health": map[string]any{"status": "Healthy"}},
				}}},
		"web": {"metadata": map[string]any{"name": "web", "namespace": "argocd"},
			"spec": map[string]any{"project": "frontend", "source": map[string]any{"repoURL": "https://git.example.com/deploy.git", "path": "web", "targetRevision": "HEAD"},
				"destination": map[string]any{"name": "prod-eu", "namespace": "web"}, "syncPolicy": map[string]any{"automated": map[string]any{"prune": true}}},
			"status": map[string]any{"sync": map[string]any{"status": "Synced", "revision": "cccccccccccccccccccccccccccccccccccccccc"},
				"health":               map[string]any{"status": "Degraded", "message": "Deployment web has 0/2 ready replicas"},
				"history":              []any{map[string]any{"id": 7, "revision": "cccccccccccccccccccccccccccccccccccccccc", "deployedAt": "2026-10-25T08:00:00Z", "initiatedBy": map[string]any{"automated": true}}},
				"resourceHealthSource": "appTree",
				"resources": []any{
					map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "web", "name": "web", "status": "Synced"},
				}}},
	}
	h := func(w http.ResponseWriter, r *http.Request) {
		c := s.record(r)
		if r.Header.Get("Authorization") != "Bearer "+Token {
			if r.URL.Path == "/api/version" {
				writeJSON(w, 200, map[string]string{"Version": "v3.1.0+abc"})
				return
			}
			writeJSON(w, 401, map[string]any{"error": "no session information", "code": 16, "message": "no session information"})
			return
		}
		p := r.URL.Path
		switch {
		case p == "/api/version":
			writeJSON(w, 200, map[string]string{"Version": "v3.1.0+abc", "GoVersion": "go1.24"})
			return
		case p == "/api/v1/session/userinfo":
			writeJSON(w, 200, map[string]any{"loggedIn": true, "username": "opsdeck", "iss": "argocd"})
			return
		case p == "/api/v1/applications" && r.Method == "GET":
			writeJSON(w, 200, map[string]any{"items": []any{apps["shop"], apps["web"]}, "metadata": map[string]any{}})
			return
		}
		rest, ok := strings.CutPrefix(p, "/api/v1/applications/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		name, sub, _ := strings.Cut(rest, "/")
		app := apps[name]
		if app == nil {
			writeJSON(w, 404, map[string]any{"error": "applications.argoproj.io \"" + name + "\" not found", "code": 5, "message": "applications.argoproj.io \"" + name + "\" not found"})
			return
		}
		// appNamespace: in the body for sync and rollback, in the query elsewhere
		ns := r.URL.Query().Get("appNamespace")
		if sub == "sync" || sub == "rollback" {
			ns, _ = c.Body["appNamespace"].(string)
		}
		if ns != "argocd" {
			writeJSON(w, 400, map[string]any{"message": "appNamespace missing (got " + ns + ")"})
			return
		}
		switch {
		case sub == "" && r.Method == "GET":
			writeJSON(w, 200, app)
		case sub == "resource-tree":
			writeJSON(w, 200, map[string]any{"nodes": []any{
				map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "web", "name": "web", "health": map[string]any{"status": "Degraded", "message": "0/2 ready"}},
			}})
		case sub == "managed-resources":
			writeJSON(w, 200, map[string]any{"items": []any{
				map[string]any{"group": "apps", "kind": "Deployment", "namespace": "shop", "name": "shop-web", "modified": true,
					"liveState": state(deployment("shop:1.0")), "normalizedLiveState": state(deployment("shop:1.0")),
					"targetState": state(deployment("shop:1.1")), "predictedLiveState": state(deployment("shop:1.1"))},
				map[string]any{"kind": "Service", "namespace": "shop", "name": "shop-web", "modified": false,
					"liveState": state(map[string]any{"kind": "Service"}), "targetState": state(map[string]any{"kind": "Service"})},
			}})
		case sub == "sync" && r.Method == "POST":
			writeJSON(w, 200, app)
		case sub == "rollback" && r.Method == "POST":
			writeJSON(w, 200, app)
		case sub == "operation" && r.Method == "DELETE":
			writeJSON(w, 200, map[string]any{})
		case sub == "resource/actions" && r.Method == "POST":
			if c.Raw != `"restart"` {
				writeJSON(w, 400, map[string]any{"message": "body must be the action name as a JSON string, got " + c.Raw})
				return
			}
			writeJSON(w, 200, map[string]any{})
		case sub == "logs":
			for _, l := range []string{
				`{"result":{"content":"listening on :8080","podName":"shop-web-7d9f-abc","timeStampStr":"2026-10-25T10:00:00Z"}}`,
				`{"result":{"content":"GET /healthz 200","podName":"shop-web-7d9f-abc","timeStampStr":"2026-10-25T10:00:05Z"}}`,
				`{"result":{"content":"","podName":"","last":true}}`,
			} {
				io.WriteString(w, l+"\n")
			}
		default:
			http.NotFound(w, r)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// ----------------------------------------------------------------------------
// Komodo

// Komodo starts a fake Komodo v2 (GetUser through GET /user, /auth gone)
// on server prod-1 with the stack shop (running, the compose file changed
// since the last deploy, an image update for one service) and the
// deployment api (exited). Execute calls return an InProgress update that
// GetUpdate reports Complete.
func Komodo(t testing.TB) *Server {
	s := &Server{Files: map[string]string{}}
	h := func(w http.ResponseWriter, r *http.Request) {
		c := s.record(r)
		if r.Header.Get("X-Api-Key") != Key || r.Header.Get("X-Api-Secret") != Secret {
			writeJSON(w, 401, map[string]any{"error": "invalid api key or secret", "trace": []string{}})
			return
		}
		if r.URL.Path == "/user" && r.Method == "GET" {
			writeJSON(w, 200, map[string]any{"_id": map[string]string{"$oid": "u1"}, "username": "opsdeck-bot", "enabled": true, "admin": false})
			return
		}
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		typ, _ := c.Body["type"].(string)
		params, _ := c.Body["params"].(map[string]any)
		switch r.URL.Path + " " + typ {
		case "/read GetVersion":
			writeJSON(w, 200, map[string]string{"version": "2.3.3"})
		case "/read ListServers":
			writeJSON(w, 200, []any{map[string]any{"id": "s1", "type": "Server", "name": "prod-1", "tags": []string{}, "info": map[string]any{"state": "Ok"}}})
		case "/read ListStacks":
			writeJSON(w, 200, []any{map[string]any{"id": "st1", "type": "Stack", "name": "shop", "tags": []string{},
				"info": map[string]any{"server_id": "s1", "state": "running", "status": "Up 3 hours", "repo": "", "files_on_host": false,
					"services": []any{map[string]any{"service": "web", "image": "shop:1.0", "update_available": true}, map[string]any{"service": "db", "image": "postgres:16", "update_available": false}}}}})
		case "/read ListDeployments":
			writeJSON(w, 200, []any{map[string]any{"id": "d1", "type": "Deployment", "name": "api", "tags": []string{},
				"info": map[string]any{"server_id": "s1", "state": "exited", "status": "Exited (1) 5 minutes ago", "image": "api:3.2", "update_available": false}}})
		case "/read GetStack":
			if params["stack"] != "st1" {
				writeJSON(w, 404, map[string]any{"error": "no stack found"})
				return
			}
			writeJSON(w, 200, map[string]any{"_id": map[string]string{"$oid": "st1"}, "name": "shop",
				"config": map[string]any{"server_id": "s1", "file_contents": "services:\n  web:\n    image: shop:1.1\n  db:\n    image: postgres:16\n"},
				"info":   map[string]any{"deployed_contents": []any{map[string]any{"path": "compose.yaml", "contents": "services:\n  web:\n    image: shop:1.0\n  db:\n    image: postgres:16\n"}}}})
		case "/read ListUpdates":
			writeJSON(w, 200, map[string]any{"updates": []any{
				map[string]any{"id": "u9", "operation": "DeployStack", "start_ts": 1761300000000, "end_ts": 1761300012000, "success": true, "username": "alice", "status": "Complete",
					"target": map[string]any{"type": "Stack", "id": "st1"}},
			}})
		case "/read GetStackLog":
			writeJSON(w, 200, map[string]any{"stage": "get logs", "command": "docker compose logs", "stdout": "web-1  | ready\ndb-1   | listening", "stderr": "", "success": true})
		case "/read GetDeploymentLog":
			writeJSON(w, 200, map[string]any{"stdout": "", "stderr": "panic: missing DATABASE_URL", "success": true})
		case "/execute DeployStack", "/execute RestartStack", "/execute StopDeployment", "/execute Deploy", "/execute StartDeployment":
			writeJSON(w, 200, map[string]any{"_id": map[string]string{"$oid": "u10"}, "operation": typ, "start_ts": 1761400000000, "success": false, "status": "InProgress", "logs": []any{}})
		case "/read GetUpdate":
			writeJSON(w, 200, map[string]any{"_id": map[string]string{"$oid": fmt.Sprint(params["id"])}, "operation": "DeployStack", "start_ts": 1761400000000, "end_ts": 1761400009000,
				"success": true, "status": "Complete", "logs": []any{
					map[string]any{"stage": "Compose Up", "command": "docker compose up -d", "stdout": "Container shop-web-1 Started", "stderr": "", "success": true}}})
		case "/auth GetUser":
			http.NotFound(w, r) // v2: the user is at GET /user
		default:
			writeJSON(w, 400, map[string]any{"error": "unknown request " + typ})
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}
