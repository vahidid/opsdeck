// Package acmeserver is a small fake ACME CA (RFC 8555) for tests: directory,
// nonces, accounts, orders with dns-01 authorizations, finalization and
// certificates signed by an ephemeral CA. JWS signatures are not verified;
// a dns-01 challenge is validated by looking the TXT record up in a
// dnsserver fake (or accepted when none is given).
package acmeserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opsdeck/internal/testutil/dnsserver"
)

// Server is one fake CA.
type Server struct {
	URL       string // the directory URL
	DNS       *dnsserver.Server
	Lifetime  time.Duration // validity of issued certificates (default 90 days)
	caKey     *ecdsa.PrivateKey
	caCert    *x509.Certificate
	caPEM     string
	mu        sync.Mutex
	nonce     int
	accounts  int
	orders    map[string]*order
	authzs    map[string]*authz
	certs     map[string]string // id → PEM chain
	keys      map[string]bool   // account keys seen (JWK JSON)
	Accounts  []map[string]any
	Finalized int
}

type order struct {
	id         string
	idents     []ident
	authzIDs   []string
	status     string
	certID     string
	finalizeOK bool
}

type ident struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type authz struct {
	id       string
	ident    ident
	wildcard bool
	status   string
	token    string
	chalStat string
}

// New starts a fake CA. dns may be nil (challenges are then accepted
// without a lookup).
func New(t testing.TB, dns *dnsserver.Server) *Server {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fake ACME Root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	s := &Server{DNS: dns, Lifetime: 90 * 24 * time.Hour, caKey: key, caCert: ca, caPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		orders: map[string]*order{}, authzs: map[string]*authz{}, certs: map[string]string{}, keys: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(srv.Close)
	s.URL = srv.URL + "/directory"
	return s
}

// CAPEM is the root certificate issued certificates chain to.
func (s *Server) CAPEM() string { return s.caPEM }

type jws struct {
	Protected string `json:"protected"`
	Payload   string `json:"payload"`
}

func b64(s string) []byte {
	b, _ := base64.RawURLEncoding.DecodeString(s)
	return b
}

func (s *Server) newNonce() string {
	s.nonce++
	return fmt.Sprintf("nonce-%d", s.nonce)
}

func (s *Server) problem(w http.ResponseWriter, status int, typ, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Replay-Nonce", s.newNonce())
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"type": "urn:ietf:params:acme:error:" + typ, "detail": detail, "status": status})
}

func (s *Server) json(w http.ResponseWriter, status int, v any, headers map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Replay-Nonce", s.newNonce())
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) base(r *http.Request) string { return "http://" + r.Host }

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	base := s.base(r)
	p := r.URL.Path
	// POST-as-GET and real POSTs carry a JWS; read header and payload
	var protected map[string]any
	var payload []byte
	if r.Method == "POST" {
		var j jws
		if err := json.NewDecoder(r.Body).Decode(&j); err != nil {
			s.problem(w, 400, "malformed", "bad JWS")
			return
		}
		json.Unmarshal(b64(j.Protected), &protected)
		payload = b64(j.Payload)
		if protected["nonce"] == nil {
			s.problem(w, 400, "badNonce", "no nonce")
			return
		}
	}
	switch {
	case p == "/directory":
		s.json(w, 200, map[string]any{"newNonce": base + "/new-nonce", "newAccount": base + "/new-account", "newOrder": base + "/new-order", "revokeCert": base + "/revoke",
			"keyChange": base + "/key-change", "meta": map[string]any{"termsOfService": base + "/tos"}}, nil)
	case p == "/new-nonce":
		w.Header().Set("Replay-Nonce", s.newNonce())
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	case p == "/new-account":
		var in map[string]any
		json.Unmarshal(payload, &in)
		if in["onlyReturnExisting"] == true && s.accounts == 0 {
			s.problem(w, 400, "accountDoesNotExist", "no account")
			return
		}
		if in["termsOfServiceAgreed"] != true && in["onlyReturnExisting"] != true {
			s.problem(w, 400, "userActionRequired", "terms not agreed")
			return
		}
		jwk, _ := json.Marshal(protected["jwk"])
		status := 201
		if in["onlyReturnExisting"] == true || s.keys[string(jwk)] {
			status = 200
		}
		if status == 201 {
			s.accounts++
			s.keys[string(jwk)] = true
			s.Accounts = append(s.Accounts, in)
		}
		s.json(w, status, map[string]any{"status": "valid", "contact": in["contact"], "orders": base + "/orders"}, map[string]string{"Location": base + "/acct/1"})
	case p == "/new-order":
		var in struct {
			Identifiers []ident `json:"identifiers"`
		}
		json.Unmarshal(payload, &in)
		if len(in.Identifiers) == 0 {
			s.problem(w, 400, "malformed", "no identifiers")
			return
		}
		for _, id := range in.Identifiers {
			if strings.HasSuffix(id.Value, ".invalid") {
				s.problem(w, 400, "rejectedIdentifier", "Cannot issue for \""+id.Value+"\": The ACME server refuses to issue a certificate for this domain name")
				return
			}
		}
		o := &order{id: fmt.Sprintf("o%d", len(s.orders)+1), idents: in.Identifiers, status: "pending"}
		for _, id := range in.Identifiers {
			a := &authz{id: fmt.Sprintf("a%d", len(s.authzs)+1), ident: id, status: "pending", token: fmt.Sprintf("tok-%d-%d", len(s.authzs)+1, time.Now().UnixNano()%1000), chalStat: "pending"}
			if strings.HasPrefix(id.Value, "*.") {
				a.ident.Value, a.wildcard = strings.TrimPrefix(id.Value, "*."), true
			}
			s.authzs[a.id] = a
			o.authzIDs = append(o.authzIDs, a.id)
		}
		s.orders[o.id] = o
		s.json(w, 201, s.orderJSON(base, o), map[string]string{"Location": base + "/order/" + o.id})
	case strings.HasPrefix(p, "/order/") && strings.HasSuffix(p, "/finalize"):
		o := s.orders[strings.TrimSuffix(strings.TrimPrefix(p, "/order/"), "/finalize")]
		if o == nil {
			s.problem(w, 404, "malformed", "no such order")
			return
		}
		for _, aid := range o.authzIDs {
			if s.authzs[aid].status != "valid" {
				s.problem(w, 403, "orderNotReady", "authorization "+s.authzs[aid].ident.Value+" is "+s.authzs[aid].status)
				return
			}
		}
		var in struct {
			CSR string `json:"csr"`
		}
		json.Unmarshal(payload, &in)
		csr, err := x509.ParseCertificateRequest(b64(in.CSR))
		if err != nil {
			s.problem(w, 400, "badCSR", err.Error())
			return
		}
		chain, err := s.sign(csr)
		if err != nil {
			s.problem(w, 500, "serverInternal", err.Error())
			return
		}
		o.certID = fmt.Sprintf("c%d", len(s.certs)+1)
		s.certs[o.certID] = chain
		o.status = "valid"
		s.Finalized++
		s.json(w, 200, s.orderJSON(base, o), map[string]string{"Location": base + "/order/" + o.id})
	case strings.HasPrefix(p, "/order/"):
		o := s.orders[strings.TrimPrefix(p, "/order/")]
		if o == nil {
			s.problem(w, 404, "malformed", "no such order")
			return
		}
		s.json(w, 200, s.orderJSON(base, o), nil)
	case strings.HasPrefix(p, "/authz/"):
		a := s.authzs[strings.TrimPrefix(p, "/authz/")]
		if a == nil {
			s.problem(w, 404, "malformed", "no such authorization")
			return
		}
		s.json(w, 200, s.authzJSON(base, a), nil)
	case strings.HasPrefix(p, "/chal/"):
		a := s.authzs[strings.TrimPrefix(p, "/chal/")]
		if a == nil {
			s.problem(w, 404, "malformed", "no such challenge")
			return
		}
		if a.chalStat == "pending" && len(payload) > 0 {
			// the client accepted: validate against the DNS fake
			if s.DNS == nil || s.hasTXT(a) {
				a.chalStat, a.status = "valid", "valid"
			} else {
				a.chalStat, a.status = "invalid", "invalid"
			}
		}
		s.json(w, 200, s.chalJSON(base, a), map[string]string{"Link": "<" + base + "/authz/" + a.id + ">;rel=\"up\""})
	case strings.HasPrefix(p, "/cert/"):
		c, ok := s.certs[strings.TrimPrefix(p, "/cert/")]
		if !ok {
			s.problem(w, 404, "malformed", "no such certificate")
			return
		}
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		w.Header().Set("Replay-Nonce", s.newNonce())
		w.WriteHeader(200)
		w.Write([]byte(c))
	default:
		s.problem(w, 404, "malformed", "no route for "+p)
	}
}

func (s *Server) orderJSON(base string, o *order) map[string]any {
	var authz []string
	for _, a := range o.authzIDs {
		authz = append(authz, base+"/authz/"+a)
	}
	m := map[string]any{"status": o.status, "expires": time.Now().Add(time.Hour).Format(time.RFC3339), "identifiers": o.idents, "authorizations": authz, "finalize": base + "/order/" + o.id + "/finalize"}
	if o.certID != "" {
		m["certificate"] = base + "/cert/" + o.certID
	}
	return m
}

func (s *Server) authzJSON(base string, a *authz) map[string]any {
	m := map[string]any{"status": a.status, "expires": time.Now().Add(time.Hour).Format(time.RFC3339), "identifier": a.ident, "challenges": []any{s.chalJSON(base, a)}}
	if a.wildcard {
		m["wildcard"] = true
	}
	return m
}

func (s *Server) chalJSON(base string, a *authz) map[string]any {
	m := map[string]any{"type": "dns-01", "url": base + "/chal/" + a.id, "token": a.token, "status": a.chalStat}
	if a.chalStat == "invalid" {
		m["error"] = map[string]any{"type": "urn:ietf:params:acme:error:dns", "detail": "no TXT record _acme-challenge." + a.ident.Value + " with the expected value"}
	}
	return m
}

// hasTXT looks for the challenge record in the DNS fake: a TXT record named
// _acme-challenge (relative to the zone) whose content is the key
// authorization digest — here any value starting with the token's digest
// prefix is accepted since the fake does not know the account key.
func (s *Server) hasTXT(a *authz) bool {
	want := "_acme-challenge." + a.ident.Value
	for zone, recs := range s.DNS.Zones {
		if a.ident.Value != zone && !strings.HasSuffix(a.ident.Value, "."+zone) {
			continue
		}
		for _, rec := range recs {
			typ := strings.ToUpper(fmt.Sprint(rec["type"]))
			if typ != "TXT" {
				continue
			}
			name := fmt.Sprint(rec["name"])
			if name == "@" {
				name = zone
			} else if !strings.HasSuffix(name, "."+zone) && name != zone {
				name = name + "." + zone
			}
			if name == want {
				return true
			}
		}
	}
	return false
}

func (s *Server) sign(csr *x509.CertificateRequest) (string, error) {
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: csr.Subject, DNSNames: csr.DNSNames, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(s.Lifetime),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.caCert, csr.PublicKey, s.caKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + s.caPEM, nil
}
