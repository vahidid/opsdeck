// Package dnsserver fakes the DNS provider APIs opsdeck talks to, for tests:
// Cloudflare's API v4 (zones, dns_records, token verification) and
// ArvanCloud's CDN API 4.0 (domains, dns-records), each with an in-memory
// zone and the provider's own response shapes.
package dnsserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Token is the API token both fakes accept. It is random for each test
// binary, so no credential-shaped literal sits in the source.
var Token = func() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "test-" + hex.EncodeToString(b)
}()

// Record is a stored record in the fake's own (provider) shape.
type Record map[string]any

// Server is one fake provider.
type Server struct {
	URL      string
	Provider string
	mu       sync.Mutex
	next     int
	Zones    map[string][]Record // zone name → records (provider shape)
	Seen     []string            // "METHOD path" of every request
}

// Cloudflare starts a fake Cloudflare API with the zone example.com
// (id z1) holding an A and an MX record.
func Cloudflare(t testing.TB) *Server {
	s := &Server{Provider: "cloudflare", Zones: map[string][]Record{"example.com": {
		{"id": "r1", "type": "A", "name": "www.example.com", "content": "203.0.113.10", "ttl": 1, "proxied": true, "proxiable": true, "comment": "web"},
		{"id": "r2", "type": "MX", "name": "example.com", "content": "mail.example.com", "ttl": 3600, "priority": 10, "proxied": false, "proxiable": false},
	}}, next: 10}
	srv := httptest.NewServer(http.HandlerFunc(s.cloudflare))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// ArvanCloud starts a fake ArvanCloud CDN API with the domain example.ir
// holding an A record over two addresses and a TXT record.
func ArvanCloud(t testing.TB) *Server {
	s := &Server{Provider: "arvancloud", Zones: map[string][]Record{"example.ir": {
		{"id": "a1", "type": "a", "name": "www", "value": []any{map[string]any{"ip": "198.51.100.1", "port": nil, "weight": nil, "country": ""}, map[string]any{"ip": "198.51.100.2", "port": nil, "weight": nil, "country": ""}},
			"ttl": 120, "cloud": true, "upstream_https": "default", "ip_filter_mode": map[string]any{"count": "multi", "order": "rr", "geo_filter": "none"}},
		{"id": "a2", "type": "txt", "name": "@", "value": map[string]any{"text": "v=spf1 -all"}, "ttl": 3600, "cloud": false, "is_protected": false},
	}}, next: 10}
	srv := httptest.NewServer(http.HandlerFunc(s.arvan))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *Server) record(method, path string) {
	s.mu.Lock()
	s.Seen = append(s.Seen, method+" "+path)
	s.mu.Unlock()
}

func (s *Server) id() string {
	s.next++
	return fmt.Sprintf("n%d", s.next)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// Cloudflare

func cfOK(w http.ResponseWriter, status int, result any, total int) {
	writeJSON(w, status, map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": result,
		"result_info": map[string]any{"page": 1, "per_page": 100, "total_pages": 1, "count": total, "total_count": total}})
}

func cfErr(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, map[string]any{"success": false, "errors": []map[string]any{{"code": code, "message": msg}}, "messages": []any{}, "result": nil})
}

func (s *Server) cloudflare(w http.ResponseWriter, r *http.Request) {
	s.record(r.Method, r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+Token {
		cfErr(w, 400, 6003, "Invalid request headers")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.Split(p, "/")
	switch {
	case p == "user/tokens/verify":
		cfOK(w, 200, map[string]any{"id": "tok1", "status": "active"}, 1)
	case p == "zones":
		var zones []map[string]any
		for name := range s.Zones {
			if q := r.URL.Query().Get("name"); q != "" && q != name {
				continue
			}
			zones = append(zones, map[string]any{"id": "z1", "name": name, "status": "active", "paused": false, "name_servers": []string{"ada.ns.cloudflare.com", "bob.ns.cloudflare.com"},
				"original_name_servers": []string{"ns1.registrar.example"}, "plan": map[string]any{"name": "Free Website"}, "account": map[string]any{"id": "acc1", "name": "Acme Account"}, "modified_on": "2026-10-01T10:00:00Z"})
		}
		if zones == nil {
			zones = []map[string]any{}
		}
		cfOK(w, 200, zones, len(zones))
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "dns_records":
		if parts[1] != "z1" {
			cfErr(w, 404, 7003, "Could not route to /zones/"+parts[1]+", perhaps your object identifier is invalid?")
			return
		}
		recs := s.Zones["example.com"]
		switch {
		case r.Method == "GET" && len(parts) == 3:
			out := []Record{}
			for _, rec := range recs {
				if t := r.URL.Query().Get("type"); t != "" && rec["type"] != t {
					continue
				}
				out = append(out, rec)
			}
			cfOK(w, 200, out, len(out))
		case r.Method == "POST" && len(parts) == 3:
			var in Record
			json.NewDecoder(r.Body).Decode(&in)
			if in["type"] == "A" && in["content"] == "bad" {
				cfErr(w, 400, 9005, "Content for A record is invalid. Must be a valid IPv4 address")
				return
			}
			for _, rec := range recs {
				if rec["type"] == in["type"] && rec["name"] == in["name"] && rec["content"] == in["content"] {
					cfErr(w, 400, 81058, "An identical record already exists.")
					return
				}
			}
			in["id"] = s.id()
			in["proxiable"] = in["type"] == "A" || in["type"] == "AAAA" || in["type"] == "CNAME"
			if d, ok := in["data"].(map[string]any); ok && in["type"] == "SRV" {
				in["content"] = fmt.Sprintf("%v %v %v", d["weight"], d["port"], d["target"])
				in["priority"] = d["priority"]
			}
			in["modified_on"] = "2026-10-20T12:00:00Z"
			s.Zones["example.com"] = append(recs, in)
			cfOK(w, 200, in, 1)
		case (r.Method == "PUT" || r.Method == "PATCH") && len(parts) == 4:
			var in Record
			json.NewDecoder(r.Body).Decode(&in)
			for i, rec := range recs {
				if rec["id"] == parts[3] {
					in["id"] = parts[3]
					in["proxiable"] = rec["proxiable"]
					in["modified_on"] = "2026-10-21T12:00:00Z"
					recs[i] = in
					cfOK(w, 200, in, 1)
					return
				}
			}
			cfErr(w, 404, 81044, "Record does not exist.")
		case r.Method == "DELETE" && len(parts) == 4:
			for i, rec := range recs {
				if rec["id"] == parts[3] {
					s.Zones["example.com"] = append(recs[:i:i], recs[i+1:]...)
					cfOK(w, 200, map[string]any{"id": parts[3]}, 1)
					return
				}
			}
			cfErr(w, 404, 81044, "Record does not exist.")
		default:
			cfErr(w, 405, 7000, "No route for that URI")
		}
	default:
		cfErr(w, 404, 7000, "No route for that URI")
	}
}

// ---------------------------------------------------------------------------
// ArvanCloud

func arList(w http.ResponseWriter, data any, total int) {
	writeJSON(w, 200, map[string]any{"data": data, "links": map[string]any{"first": "", "last": "", "prev": nil, "next": nil},
		"meta": map[string]any{"current_page": 1, "from": 1, "last_page": 1, "per_page": 100, "to": total, "total": total}})
}

func (s *Server) arvan(w http.ResponseWriter, r *http.Request) {
	s.record(r.Method, r.URL.Path)
	if r.Header.Get("Authorization") != "Apikey "+Token {
		writeJSON(w, 401, map[string]any{"message": "Unauthenticated."})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	domain := func(name string) map[string]any {
		return map[string]any{"id": "d-" + name, "domain": name, "name": name, "status": "active", "plan_level": 1,
			"ns_keys": []string{"ns1.arvancdn.ir", "ns2.arvancdn.ir"}, "current_ns": []string{"ns1.arvancdn.ir", "ns2.arvancdn.ir"}, "updated_at": "2026-10-01T10:00:00Z"}
	}
	switch {
	case len(parts) == 1 && parts[0] == "domains":
		var out []map[string]any
		for name := range s.Zones {
			if q := r.URL.Query().Get("search"); q != "" && !strings.Contains(name, q) {
				continue
			}
			out = append(out, domain(name))
		}
		if out == nil {
			out = []map[string]any{}
		}
		arList(w, out, len(out))
	case len(parts) == 2 && parts[0] == "domains":
		if _, ok := s.Zones[parts[1]]; !ok {
			writeJSON(w, 404, map[string]any{"message": "Domain not found."})
			return
		}
		writeJSON(w, 200, map[string]any{"data": domain(parts[1])})
	case len(parts) >= 3 && parts[0] == "domains" && parts[2] == "dns-records":
		recs, ok := s.Zones[parts[1]]
		if !ok {
			writeJSON(w, 404, map[string]any{"message": "Domain not found."})
			return
		}
		switch {
		case r.Method == "GET" && len(parts) == 3:
			arList(w, recs, len(recs))
		case r.Method == "POST" && len(parts) == 3:
			var in Record
			json.NewDecoder(r.Body).Decode(&in)
			if in["type"] == nil || in["value"] == nil || in["ttl"] == nil {
				writeJSON(w, 422, map[string]any{"message": "The given data was invalid.", "errors": map[string][]string{"value": {"The value field is required."}}})
				return
			}
			in["id"] = s.id()
			in["updated_at"] = "2026-10-20T12:00:00Z"
			s.Zones[parts[1]] = append(recs, in)
			writeJSON(w, 201, map[string]any{"message": "DNS record created successfully.", "data": in})
		case r.Method == "PUT" && len(parts) == 4:
			var in Record
			json.NewDecoder(r.Body).Decode(&in)
			for i, rec := range recs {
				if rec["id"] == parts[3] {
					in["id"] = parts[3]
					in["updated_at"] = "2026-10-21T12:00:00Z"
					recs[i] = in
					writeJSON(w, 200, map[string]any{"message": "DNS record updated successfully.", "data": in})
					return
				}
			}
			writeJSON(w, 404, map[string]any{"message": "DNS record not found."})
		case r.Method == "DELETE" && len(parts) == 4:
			for i, rec := range recs {
				if rec["id"] == parts[3] {
					s.Zones[parts[1]] = append(recs[:i:i], recs[i+1:]...)
					writeJSON(w, 200, map[string]any{"message": "DNS record deleted successfully."})
					return
				}
			}
			writeJSON(w, 404, map[string]any{"message": "DNS record not found."})
		default:
			writeJSON(w, 405, map[string]any{"message": "Method not allowed."})
		}
	default:
		writeJSON(w, 404, map[string]any{"message": "Not found."})
	}
}
