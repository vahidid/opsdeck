package server

import (
	"errors"
	"net/http"
	"time"

	"opsdeck/internal/vault"
)

// Vault: status, unlock, items and their fields, SSH key pairs and passwords.

func (s *Server) vaultRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/vault/status", s.vaultStatus)
	mux.HandleFunc("POST /api/vault/init", s.vaultInit)
	mux.HandleFunc("POST /api/vault/unlock", s.vaultUnlock)
	mux.HandleFunc("POST /api/vault/lock", s.vaultLock)
	mux.HandleFunc("POST /api/vault/password", u(s.vaultPassword))
	mux.HandleFunc("GET /api/vault/types", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, vault.Types) })
	mux.HandleFunc("GET /api/vault/items", u(s.itemsList))
	mux.HandleFunc("POST /api/vault/items", u(s.itemCreate))
	mux.HandleFunc("PUT /api/vault/items/{id}", u(s.itemUpdate))
	mux.HandleFunc("DELETE /api/vault/items/{id}", u(s.itemDelete))
	mux.HandleFunc("POST /api/vault/items/{id}/reveal", u(s.itemReveal))
	mux.HandleFunc("GET /api/vault/items/{id}/totp", u(s.itemTOTP))
	mux.HandleFunc("POST /api/vault/ssh/generate", u(s.sshGenerate))
	mux.HandleFunc("POST /api/vault/ssh/import", u(s.sshImport))
	mux.HandleFunc("POST /api/vault/password-generator", s.passwordGen)
}

func (s *Server) vaultStatus(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.status()) }

func (s *Server) vaultInit(w http.ResponseWriter, r *http.Request) {
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.vault.Init(in.Password); err != nil {
		fail(w, err)
		return
	}
	for _, ws := range s.ws.List() {
		s.vault.EnsureScope(ws.Name())
	}
	s.audit.Record(auditEntry("vault.init", "", nil))
	writeJSON(w, 200, s.status())
}

func (s *Server) vaultUnlock(w http.ResponseWriter, r *http.Request) {
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.vault.Unlock(in.Password); err != nil {
		if errors.Is(err, vault.ErrBadPassword) {
			s.audit.Record(auditEntryResult("vault.unlock", "denied"))
			time.Sleep(500 * time.Millisecond)
		}
		fail(w, err)
		return
	}
	for _, ws := range s.ws.List() {
		s.vault.EnsureScope(ws.Name())
	}
	s.audit.Record(auditEntry("vault.unlock", "", nil))
	writeJSON(w, 200, s.status())
}

func (s *Server) vaultLock(w http.ResponseWriter, r *http.Request) {
	s.vault.Lock()
	writeJSON(w, 200, s.status())
}

func (s *Server) vaultPassword(w http.ResponseWriter, r *http.Request) {
	var in struct{ OldPassword, NewPassword string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	err := s.vault.ChangePassword(in.OldPassword, in.NewPassword)
	s.record(r, "", "vault.change_password", "", nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// itemDTO never includes secret values.
type fieldDTO struct {
	Name     string `json:"name"`
	Secret   bool   `json:"secret"`
	Value    string `json:"value,omitempty"`
	HasValue bool   `json:"hasValue"`
}

type itemDTO struct {
	ID          string       `json:"id"`
	Scope       string       `json:"scope"`
	Type        string       `json:"type"`
	Name        string       `json:"name"`
	Tags        []string     `json:"tags"`
	Notes       string       `json:"notes"`
	Fields      []fieldDTO   `json:"fields"`
	Policy      vault.Policy `json:"policy"`
	Ref         string       `json:"ref"`
	PublicKey   string       `json:"publicKey,omitempty"`
	Fingerprint string       `json:"fingerprint,omitempty"`
	Created     time.Time    `json:"created"`
	Updated     time.Time    `json:"updated"`
	Revision    int          `json:"revision"`
}

func toDTO(it vault.Item) itemDTO {
	if it.Tags == nil {
		it.Tags = []string{}
	}
	d := itemDTO{ID: it.ID, Scope: it.Scope, Type: it.Type, Name: it.Name, Tags: it.Tags, Notes: it.Notes, Policy: it.Policy,
		Ref: it.Ref(), Created: it.Created, Updated: it.Updated, Revision: it.Revision, Fields: []fieldDTO{}}
	for _, f := range it.Fields {
		fd := fieldDTO{Name: f.Name, Secret: f.Secret, HasValue: f.Value != ""}
		if !f.Secret {
			fd.Value = f.Value
		}
		d.Fields = append(d.Fields, fd)
	}
	if it.Type == "ssh-key" {
		d.PublicKey, d.Fingerprint = vault.PublicKeyInfo(it)
	}
	return d
}

func (s *Server) itemsList(w http.ResponseWriter, r *http.Request) {
	items, err := s.vault.List(r.URL.Query().Get("scope"))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]itemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toDTO(it))
	}
	writeJSON(w, 200, out)
}

type itemInput struct {
	Scope  string   `json:"scope"`
	Type   string   `json:"type"`
	Name   string   `json:"name"`
	Tags   []string `json:"tags"`
	Notes  string   `json:"notes"`
	Fields []struct {
		Name   string  `json:"name"`
		Value  *string `json:"value"`
		Secret bool    `json:"secret"`
	} `json:"fields"`
	Policy *vault.Policy `json:"policy"`
}

func (in itemInput) toItem() (vault.Item, map[string]bool) {
	it := vault.Item{Scope: in.Scope, Type: in.Type, Name: in.Name, Tags: in.Tags, Notes: in.Notes}
	if in.Policy != nil {
		it.Policy = *in.Policy
	}
	keep := map[string]bool{}
	for _, f := range in.Fields {
		v := ""
		if f.Value == nil {
			keep[f.Name] = true
		} else {
			v = *f.Value
		}
		it.Fields = append(it.Fields, vault.Field{Name: f.Name, Value: v, Secret: f.Secret})
	}
	return it, keep
}

func (s *Server) ensureScope(scope string) error {
	if scope == "" {
		return errors.New("scope is required")
	}
	if scope != vault.GlobalScope {
		if _, err := s.ws.Get(scope); err != nil {
			return err
		}
	}
	return s.vault.EnsureScope(scope)
}

func (s *Server) itemCreate(w http.ResponseWriter, r *http.Request) {
	var in itemInput
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.ensureScope(in.Scope); err != nil {
		fail(w, err)
		return
	}
	it, _ := in.toItem()
	if in.Policy == nil {
		it.Policy.AgentUse = true
	}
	created, err := s.vault.Create(it)
	s.record(r, scopeWS(in.Scope), "vault.create", "vault://"+in.Scope+"/"+in.Name, map[string]any{"type": in.Type}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, toDTO(created))
}

func scopeWS(scope string) string {
	if scope == vault.GlobalScope {
		return ""
	}
	return scope
}

func (s *Server) itemUpdate(w http.ResponseWriter, r *http.Request) {
	var in itemInput
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	it, keep := in.toItem()
	if in.Policy == nil {
		if cur, err := s.vault.Get(r.PathValue("id")); err == nil {
			it.Policy = cur.Policy
		}
	}
	updated, err := s.vault.Update(r.PathValue("id"), it, keep)
	if err != nil {
		fail(w, err)
		return
	}
	s.record(r, scopeWS(updated.Scope), "vault.update", updated.Ref(), nil, nil)
	writeJSON(w, 200, toDTO(updated))
}

func (s *Server) itemDelete(w http.ResponseWriter, r *http.Request) {
	it, err := s.vault.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.vault.Delete(it.ID); err != nil {
		fail(w, err)
		return
	}
	s.record(r, scopeWS(it.Scope), "vault.delete", it.Ref(), nil, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) itemReveal(w http.ResponseWriter, r *http.Request) {
	var in struct{ Field string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	it, err := s.vault.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	s.record(r, scopeWS(it.Scope), "vault.reveal", it.Ref()+"#"+in.Field, nil, nil)
	writeJSON(w, 200, map[string]string{"value": it.Get(in.Field)})
}

func (s *Server) itemTOTP(w http.ResponseWriter, r *http.Request) {
	it, err := s.vault.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	code, period, remaining, err := vault.TOTP(it.Get("totp"), time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"code": code, "period": period, "remaining": remaining})
}

func (s *Server) sshGenerate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope, Name, Algorithm, Comment string
		Tags                            []string
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.ensureScope(in.Scope); err != nil {
		fail(w, err)
		return
	}
	comment := in.Comment
	if comment == "" {
		comment = in.Name + "@opsdeck"
	}
	fields, err := vault.GenerateSSHKey(in.Algorithm, comment)
	if err != nil {
		fail(w, err)
		return
	}
	it, err := s.vault.Create(vault.Item{Scope: in.Scope, Type: "ssh-key", Name: in.Name, Tags: in.Tags, Fields: fields, Policy: vault.Policy{AgentUse: true}})
	s.record(r, scopeWS(in.Scope), "vault.ssh_generate", "vault://"+in.Scope+"/"+in.Name, map[string]any{"algorithm": in.Algorithm}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, toDTO(it))
}

func (s *Server) sshImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope, Name, PrivateKey, Passphrase string
		Tags                                []string
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.ensureScope(in.Scope); err != nil {
		fail(w, err)
		return
	}
	fields, err := vault.ImportSSHKey(in.PrivateKey, in.Passphrase)
	if err != nil {
		fail(w, err)
		return
	}
	it, err := s.vault.Create(vault.Item{Scope: in.Scope, Type: "ssh-key", Name: in.Name, Tags: in.Tags, Fields: fields, Policy: vault.Policy{AgentUse: true}})
	s.record(r, scopeWS(in.Scope), "vault.ssh_import", "vault://"+in.Scope+"/"+in.Name, nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, toDTO(it))
}

func (s *Server) passwordGen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Length  int
		Symbols bool
	}
	readJSON(r, &in)
	if in.Length == 0 {
		in.Length = 24
	}
	p, err := vault.GeneratePassword(in.Length, in.Symbols)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"password": p})
}

// providerModels lists the models a provider offers, for the model picker in
// Settings: an agent CLI is asked with the user's own login, an API with its key.
// The provider is the (possibly unsaved) config from the form.
