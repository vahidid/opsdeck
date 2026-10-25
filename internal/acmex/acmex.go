// Package acmex issues and renews TLS certificates from ACME certificate
// authorities (Let's Encrypt and others) with the dns-01 challenge, solved
// through the DNS connectors of this machine (internal/dnsx). A workspace
// describes what it needs as a Certificate resource; the certificate and its
// key live in the vault (vault://<ws>/cert-<name>), from where they are
// deployed to hosts without the agent ever seeing the key.
package acmex

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"opsdeck/internal/dnsx"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// CA is an ACME certificate authority the UI offers.
type CA struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Directory string `json:"directory"`
	Staging   bool   `json:"staging"` // test certificates, not trusted by browsers
	Wildcard  bool   `json:"wildcard"`
	Note      string `json:"note"`
}

// CAs lists the authorities that work with an account key alone (no
// external account binding).
var CAs = []CA{
	{ID: "letsencrypt", Label: "Let's Encrypt", Directory: acme.LetsEncryptURL, Wildcard: true, Note: "Free 90-day certificates; rate limits per registered domain (50 certificates a week)."},
	{ID: "letsencrypt-staging", Label: "Let's Encrypt (staging)", Directory: "https://acme-staging-v02.api.letsencrypt.org/directory", Staging: true, Wildcard: true, Note: "For trying the flow: generous limits, certificates are not trusted."},
	{ID: "buypass", Label: "Buypass Go SSL", Directory: "https://api.buypass.com/acme/directory", Note: "Free 180-day certificates; no wildcards."},
	{ID: "buypass-test", Label: "Buypass (test)", Directory: "https://api.test4.buypass.no/acme/directory", Staging: true, Note: "Buypass test environment; certificates are not trusted."},
	{ID: "custom", Label: "Another ACME CA", Wildcard: true, Note: "Any ACME directory URL: step-ca, an internal CA. External account binding (ZeroSSL, Google Trust Services) is not supported yet."},
}

// CAByID returns an authority.
func CAByID(id string) (CA, bool) {
	for _, c := range CAs {
		if c.ID == id {
			return c, true
		}
	}
	return CA{}, false
}

// Spec is a Certificate resource, read.
type Spec struct {
	Name      string
	Domains   []string
	CA        CA
	Directory string
	CACert    string
	Email     string
	KeyType   string
	RenewDays int
	AutoRenew bool
}

var domainRe = regexp.MustCompile(`^(\*\.)?([a-z0-9_](?:[a-z0-9_-]{0,61}[a-z0-9_])?\.)+[a-z0-9-]{2,63}$`)

// ReadSpec validates a Certificate resource.
func ReadSpec(r *workspace.Resource) (*Spec, error) {
	if r.Kind != "Certificate" {
		return nil, fmt.Errorf("%s is not a Certificate", r.ID())
	}
	s := &Spec{Name: r.Name, Email: strings.TrimSpace(r.Str("email")), KeyType: firstNonEmpty(r.Str("keyType"), "ec256"),
		RenewDays: r.Int("renewDays", 30), AutoRenew: r.Bool("autoRenew", false), CACert: strings.TrimSpace(r.Str("caCert"))}
	seen := map[string]bool{}
	for _, d := range r.List("domains") {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" || seen[d] {
			continue
		}
		if !domainRe.MatchString(d) {
			return nil, fmt.Errorf("%q is not a valid domain name (a wildcard is *.example.com)", d)
		}
		seen[d] = true
		s.Domains = append(s.Domains, d)
	}
	if len(s.Domains) == 0 {
		return nil, errors.New("the certificate needs at least one domain")
	}
	caID := firstNonEmpty(r.Str("ca"), "letsencrypt")
	ca, ok := CAByID(caID)
	if !ok {
		return nil, fmt.Errorf("unknown certificate authority %q", caID)
	}
	s.CA = ca
	s.Directory = ca.Directory
	if ca.ID == "custom" {
		s.Directory = strings.TrimSpace(r.Str("directoryUrl"))
		u, err := url.Parse(s.Directory)
		if s.Directory == "" || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, errors.New("ca: custom needs the ACME directory URL (directoryUrl)")
		}
	}
	switch s.KeyType {
	case "ec256", "ec384", "rsa2048", "rsa4096":
	default:
		return nil, fmt.Errorf("unknown key type %q (ec256, ec384, rsa2048, rsa4096)", s.KeyType)
	}
	if s.RenewDays <= 0 {
		s.RenewDays = 30
	}
	for _, d := range s.Domains {
		if strings.HasPrefix(d, "*.") && !ca.Wildcard {
			return nil, fmt.Errorf("%s does not issue wildcard certificates (%s)", ca.Label, d)
		}
	}
	return s, nil
}

// ItemName is the vault item that holds a certificate.
func ItemName(certName string) string { return "cert-" + certName }

// ItemRef is the vault reference of a workspace's certificate.
func ItemRef(ws *workspace.Workspace, certName string) string {
	return "vault://" + ws.Name() + "/" + ItemName(certName)
}

// Info describes a stored certificate (its public part).
type Info struct {
	Subject     string    `json:"subject"`
	Domains     []string  `json:"domains"`
	Issuer      string    `json:"issuer"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	Serial      string    `json:"serial"`
	Fingerprint string    `json:"fingerprint"` // SHA-256 of the leaf
	KeyType     string    `json:"keyType"`
	Chain       int       `json:"chain"` // certificates in the chain
	DaysLeft    int       `json:"daysLeft"`
}

// Inspect parses a PEM chain and describes its first certificate.
func Inspect(chainPEM string) (*Info, error) {
	var certs []*x509.Certificate
	rest := []byte(chainPEM)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("invalid certificate in the chain: %v", err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, errors.New("no certificate in the PEM data")
	}
	leaf := certs[0]
	sum := sha256.Sum256(leaf.Raw)
	keyType := "unknown"
	switch k := leaf.PublicKey.(type) {
	case *ecdsa.PublicKey:
		keyType = fmt.Sprintf("EC P-%d", k.Curve.Params().BitSize)
	case *rsa.PublicKey:
		keyType = fmt.Sprintf("RSA %d", k.N.BitLen())
	}
	return &Info{Subject: leaf.Subject.CommonName, Domains: leaf.DNSNames, Issuer: firstNonEmpty(leaf.Issuer.CommonName, leaf.Issuer.String()),
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, Serial: leaf.SerialNumber.Text(16), Fingerprint: hex.EncodeToString(sum[:]),
		KeyType: keyType, Chain: len(certs), DaysLeft: int(time.Until(leaf.NotAfter).Hours() / 24)}, nil
}

// Status is a Certificate resource with what the vault holds for it.
type Status struct {
	Name        string   `json:"name"`
	Domains     []string `json:"domains"`
	CA          string   `json:"ca"`
	CALabel     string   `json:"caLabel"`
	Staging     bool     `json:"staging,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Protected   bool     `json:"protected,omitempty"`
	Item        string   `json:"item"`
	Issued      bool     `json:"issued"`
	Info        *Info    `json:"info,omitempty"`
	IssuedAt    string   `json:"issuedAt,omitempty"`
	RenewDays   int      `json:"renewDays"`
	AutoRenew   bool     `json:"autoRenew"`
	// Due: the certificate should be renewed now (missing, expiring within
	// RenewDays, or not covering the domains any more).
	Due    bool   `json:"due"`
	Reason string `json:"reason,omitempty"` // why it is due
	Error  string `json:"error,omitempty"`  // an invalid spec
	Job    string `json:"job,omitempty"`    // a running issuance
}

// Manager issues certificates and keeps the running jobs.
type Manager struct {
	Vault *vault.Vault
	DNS   *dnsx.Manager
	// HTTPClient reaches the CA (nil: a default one; tests).
	HTTPClient *http.Client
	// Propagation waits until the TXT record is visible on the zone's name
	// servers (nil: the built-in check; tests set a no-op).
	Propagation func(ctx context.Context, fqdn, value string, nameServers []string, log func(string)) error
	// Record is called with every finished issuance (audit).
	Record func(ws, actor, certName string, detail map[string]any, err error)

	mu   sync.Mutex
	jobs map[string]*Job
}

// New returns a manager.
func New(v *vault.Vault, d *dnsx.Manager) *Manager {
	return &Manager{Vault: v, DNS: d, jobs: map[string]*Job{}}
}

// Status describes a Certificate resource.
func (m *Manager) Status(ws *workspace.Workspace, r *workspace.Resource) Status {
	e := ws.EnvironmentOf(r)
	st := Status{Name: r.Name, Environment: r.Environment, Protected: e.Protected, Item: ItemRef(ws, r.Name)}
	spec, err := ReadSpec(r)
	if err != nil {
		st.Error = err.Error()
		st.Domains = r.List("domains")
		return st
	}
	st.Domains, st.CA, st.CALabel, st.Staging, st.RenewDays, st.AutoRenew = spec.Domains, spec.CA.ID, spec.CA.Label, spec.CA.Staging, spec.RenewDays, spec.AutoRenew
	if j := m.running(ws.Name(), r.Name); j != nil {
		st.Job = j.ID
	}
	it, _, err := m.Vault.Resolve(st.Item)
	if err != nil {
		st.Due, st.Reason = true, "not issued yet"
		return st
	}
	info, err := Inspect(it.Get("certificate"))
	if err != nil {
		st.Due, st.Reason = true, "the stored certificate cannot be read: "+err.Error()
		return st
	}
	st.Issued, st.Info, st.IssuedAt = true, info, it.Get("issued_at")
	switch {
	case time.Now().After(info.NotAfter):
		st.Due, st.Reason = true, "expired"
	case time.Until(info.NotAfter) < time.Duration(spec.RenewDays)*24*time.Hour:
		st.Due, st.Reason = true, fmt.Sprintf("expires in %d days", info.DaysLeft)
	case !covers(info.Domains, spec.Domains):
		st.Due, st.Reason = true, "the domains changed"
	}
	return st
}

func covers(have, want []string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[strings.ToLower(h)] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// Issued is the result of an issuance.
type Issued struct {
	Item string `json:"item"`
	Info *Info  `json:"info"`
}

// Issue obtains a certificate for the resource and stores it in the vault,
// logging each step through log. It takes minutes (DNS propagation).
func (m *Manager) Issue(ctx context.Context, ws *workspace.Workspace, r *workspace.Resource, log func(string)) (*Issued, error) {
	if log == nil {
		log = func(string) {}
	}
	spec, err := ReadSpec(r)
	if err != nil {
		return nil, err
	}
	if m.DNS == nil {
		return nil, errors.New("DNS connectors are not available")
	}
	log(fmt.Sprintf("Certificate %s for %s from %s", spec.Name, strings.Join(spec.Domains, ", "), spec.CA.Label))

	client, err := m.client(ctx, ws, spec, log)
	if err != nil {
		return nil, err
	}

	// The zones first: every domain must be reachable through a connector
	// before the CA is asked for anything.
	solver := newSolver(m, ws, log)
	for _, d := range spec.Domains {
		if _, _, err := solver.zoneFor(ctx, strings.TrimPrefix(d, "*.")); err != nil {
			return nil, err
		}
	}

	log("Requesting an order…")
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(spec.Domains...))
	if err != nil {
		return nil, fmt.Errorf("the CA refused the order: %w", acmeErr(err))
	}
	defer solver.cleanup(log)
	for _, u := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, u)
		if err != nil {
			return nil, acmeErr(err)
		}
		name := authz.Identifier.Value
		if authz.Status == acme.StatusValid {
			log(fmt.Sprintf("%s: already authorized", name))
			continue
		}
		var chal *acme.Challenge
		for _, c := range authz.Challenges {
			if c.Type == "dns-01" {
				chal = c
			}
		}
		if chal == nil {
			return nil, fmt.Errorf("%s: the CA offers no dns-01 challenge", name)
		}
		value, err := client.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return nil, err
		}
		if err := solver.present(ctx, name, value); err != nil {
			return nil, err
		}
		if _, err := client.Accept(ctx, chal); err != nil {
			return nil, fmt.Errorf("%s: the CA did not accept the challenge: %w", name, acmeErr(err))
		}
		log(fmt.Sprintf("%s: waiting for the CA to check the record…", name))
		if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
			return nil, fmt.Errorf("%s: validation failed: %w", name, acmeErr(err))
		}
		log(fmt.Sprintf("%s: authorized", name))
	}

	log("Generating the key and the request…")
	key, keyPEM, err := newKey(spec.KeyType)
	if err != nil {
		return nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: spec.Domains[0]}, DNSNames: spec.Domains}, key)
	if err != nil {
		return nil, err
	}
	log("Finalizing the order…")
	der, certURL, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, fmt.Errorf("the CA did not issue the certificate: %w", acmeErr(err))
	}
	var chain strings.Builder
	for _, b := range der {
		pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: b})
	}
	info, err := Inspect(chain.String())
	if err != nil {
		return nil, err
	}
	log(fmt.Sprintf("Issued by %s, valid until %s (%d days)", info.Issuer, info.NotAfter.Format("2006-01-02"), info.DaysLeft))

	ref, err := m.store(ws, spec, chain.String(), keyPEM, info, certURL)
	if err != nil {
		return nil, err
	}
	log("Stored in the vault as " + ref)
	return &Issued{Item: ref, Info: info}, nil
}

// client returns an ACME client with the workspace's account at the CA,
// registering one when needed.
func (m *Manager) client(ctx context.Context, ws *workspace.Workspace, spec *Spec, log func(string)) (*acme.Client, error) {
	acctName := "acme-account-" + accountKey(spec)
	ref := "vault://" + ws.Name() + "/" + acctName
	var signer crypto.Signer
	it, _, err := m.Vault.Resolve(ref)
	if err == nil {
		signer, err = parseKey(it.Get("content"))
		if err != nil {
			return nil, fmt.Errorf("%s: the stored account key cannot be read: %v", ref, err)
		}
	} else {
		k, keyPEM, err := newKey("ec256")
		if err != nil {
			return nil, err
		}
		signer = k
		it, err = m.Vault.Create(vault.Item{Scope: ws.Name(), Type: "file", Name: acctName, Tags: []string{"acme"},
			Notes:  "ACME account key at " + spec.CA.Label + " (" + spec.Directory + "). Used by opsdeck to issue and renew certificates.",
			Fields: []vault.Field{{Name: "filename", Value: acctName + ".pem"}, {Name: "content", Value: keyPEM, Secret: true}, {Name: "directory", Value: spec.Directory}, {Name: "email", Value: spec.Email}}})
		if err != nil {
			return nil, err
		}
		log("Created an account key at " + spec.CA.Label + " (" + ref + ")")
	}
	hc := m.HTTPClient
	if hc == nil {
		hc, err = caClient(spec.CACert)
		if err != nil {
			return nil, err
		}
	}
	client := &acme.Client{Key: signer, DirectoryURL: spec.Directory, HTTPClient: hc, UserAgent: "opsdeck"}
	acct := &acme.Account{}
	if spec.Email != "" {
		acct.Contact = []string{"mailto:" + spec.Email}
	}
	a, err := client.Register(ctx, acct, acme.AcceptTOS)
	switch {
	case errors.Is(err, acme.ErrAccountAlreadyExists):
		// the client cached the account URL (KID)
	case err != nil:
		return nil, fmt.Errorf("could not register the account at %s: %w", spec.CA.Label, acmeErr(err))
	default:
		log("Registered the account at " + spec.CA.Label)
		if a != nil && a.URI != "" && it.Get("account_url") == "" {
			it.Fields = append(it.Fields, vault.Field{Name: "account_url", Value: a.URI})
			m.Vault.Update(it.ID, it, map[string]bool{"content": true})
		}
	}
	return client, nil
}

// accountKey names the account item: one per CA (and per custom
// directory host).
func accountKey(spec *Spec) string {
	if spec.CA.ID != "custom" {
		return spec.CA.ID
	}
	u, _ := url.Parse(spec.Directory)
	host := strings.ToLower(u.Hostname())
	return "custom-" + regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(host, "-")
}

func caClient(caCert string) (*http.Client, error) {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 20 * time.Second, ResponseHeaderTimeout: 60 * time.Second, ForceAttemptHTTP2: true}
	if ca := strings.TrimSpace(caCert); ca != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, errors.New("the CA's TLS certificate is not valid PEM")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return &http.Client{Transport: tr, Timeout: 90 * time.Second}, nil
}

// store writes the certificate into the vault (replacing the previous one,
// keeping its policy).
func (m *Manager) store(ws *workspace.Workspace, spec *Spec, chain, keyPEM string, info *Info, certURL string) (string, error) {
	name := ItemName(spec.Name)
	item := vault.Item{Scope: ws.Name(), Type: "certificate", Name: name, Tags: []string{"tls", "acme"},
		Notes:  "TLS certificate for " + strings.Join(spec.Domains, ", ") + ", issued by opsdeck from " + spec.CA.Label + ". Renewed in place: deploy it again after a renewal.",
		Policy: vault.Policy{AgentUse: true},
		Fields: []vault.Field{
			{Name: "certificate", Value: chain},
			{Name: "private_key", Value: keyPEM, Secret: true},
			{Name: "domains", Value: strings.Join(info.Domains, " ")},
			{Name: "not_before", Value: info.NotBefore.UTC().Format(time.RFC3339)},
			{Name: "not_after", Value: info.NotAfter.UTC().Format(time.RFC3339)},
			{Name: "issuer", Value: info.Issuer},
			{Name: "serial", Value: info.Serial},
			{Name: "fingerprint_sha256", Value: info.Fingerprint},
			{Name: "ca", Value: spec.CA.ID},
			{Name: "issued_at", Value: time.Now().UTC().Format(time.RFC3339)},
		}}
	if certURL != "" {
		item.Fields = append(item.Fields, vault.Field{Name: "certificate_url", Value: certURL})
	}
	items, err := m.Vault.List(ws.Name())
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.Name == name {
			item.Policy = it.Policy
			saved, err := m.Vault.Update(it.ID, item, nil)
			if err != nil {
				return "", err
			}
			return saved.Ref(), nil
		}
	}
	saved, err := m.Vault.Create(item)
	if err != nil {
		return "", err
	}
	return saved.Ref(), nil
}

// Material returns the stored certificate chain and key of a certificate
// (for deployment; never shown to the agent).
func (m *Manager) Material(ws *workspace.Workspace, certName string) (chain, key string, info *Info, err error) {
	it, _, err := m.Vault.Resolve(ItemRef(ws, certName))
	if err != nil {
		return "", "", nil, fmt.Errorf("certificate %s is not issued yet (%v)", certName, err)
	}
	chain, key = it.Get("certificate"), it.Get("private_key")
	if chain == "" || key == "" {
		return "", "", nil, fmt.Errorf("%s holds no certificate and key", it.Ref())
	}
	info, err = Inspect(chain)
	return chain, key, info, err
}

// SplitChain separates the leaf from the intermediates of a PEM chain.
func SplitChain(chain string) (leaf, rest string) {
	var blocks []string
	data := []byte(chain)
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		blocks = append(blocks, string(pem.EncodeToMemory(block)))
	}
	if len(blocks) == 0 {
		return chain, ""
	}
	return blocks[0], strings.Join(blocks[1:], "")
}

// ----------------------------------------------------------------------------
// keys and errors

func newKey(typ string) (crypto.Signer, string, error) {
	switch typ {
	case "ec256", "ec384":
		curve := elliptic.P256()
		if typ == "ec384" {
			curve = elliptic.P384()
		}
		k, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return nil, "", err
		}
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, "", err
		}
		return k, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
	case "rsa2048", "rsa4096":
		bits := 2048
		if typ == "rsa4096" {
			bits = 4096
		}
		k, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return nil, "", err
		}
		return k, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})), nil
	}
	return nil, "", fmt.Errorf("unknown key type %q", typ)
}

func parseKey(pemData string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("not PEM")
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		s, ok := k.(crypto.Signer)
		if !ok {
			return nil, errors.New("not a signing key")
		}
		return s, nil
	}
	return nil, fmt.Errorf("unsupported key type %q", block.Type)
}

// acmeErr makes the CA's problem documents readable.
func acmeErr(err error) error {
	var ae *acme.Error
	if errors.As(err, &ae) {
		msg := ae.Detail
		if msg == "" {
			msg = ae.ProblemType
		}
		var subs []string
		for _, s := range ae.Subproblems {
			subs = append(subs, s.Identifier.Value+": "+s.Detail)
		}
		if len(subs) > 0 {
			msg += " (" + strings.Join(subs, "; ") + ")"
		}
		return fmt.Errorf("%s [%s]", msg, strings.TrimPrefix(ae.ProblemType, "urn:ietf:params:acme:error:"))
	}
	var aze *acme.AuthorizationError
	if errors.As(err, &aze) {
		var msgs []string
		for _, e := range aze.Errors {
			msgs = append(msgs, acmeErr(e).Error())
		}
		sort.Strings(msgs)
		if len(msgs) == 0 {
			return fmt.Errorf("authorization of %s failed", aze.Identifier)
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	var oe *acme.OrderError
	if errors.As(err, &oe) && oe.Problem != nil {
		return acmeErr(oe.Problem)
	}
	return err
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
