package vault

import (
	"fmt"
	"strings"
	"time"
)

// Field is one named value of an item. Secret fields are never returned by
// listing APIs, only through an explicit (audited) reveal or internal use.
type Field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// Policy controls how an item may be used.
type Policy struct {
	AgentUse     bool `json:"agentUse"`     // the AI agent may *use* the item (never reveal)
	ConfirmOnUse bool `json:"confirmOnUse"` // every use needs an interactive confirmation
}

// Item is a decrypted vault entry.
type Item struct {
	ID       string    `json:"id"`
	Scope    string    `json:"scope"`
	Type     string    `json:"type"`
	Name     string    `json:"name"`
	Tags     []string  `json:"tags"`
	Notes    string    `json:"notes"`
	Fields   []Field   `json:"fields"`
	Policy   Policy    `json:"policy"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Revision int       `json:"revision"`
}

// Get returns the value of a field, or "".
func (it *Item) Get(name string) string {
	for _, f := range it.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// Ref is the reference used in workspace files.
func (it *Item) Ref() string { return "vault://" + it.Scope + "/" + it.Name }

func (it *Item) clone() Item {
	c := *it
	c.Tags = append([]string{}, it.Tags...)
	c.Fields = append([]Field{}, it.Fields...)
	return c
}

// FieldDef describes a field of an item type (used by the UI to build forms).
type FieldDef struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Secret    bool   `json:"secret"`
	Multiline bool   `json:"multiline"`
	Required  bool   `json:"required"`
}

// TypeDef describes an item type.
type TypeDef struct {
	Type   string     `json:"type"`
	Label  string     `json:"label"`
	Fields []FieldDef `json:"fields"`
}

// Types is the catalogue of item types.
var Types = []TypeDef{
	{Type: "ssh-key", Label: "SSH key", Fields: []FieldDef{
		{Name: "private_key", Label: "Private key", Secret: true, Multiline: true, Required: true},
		{Name: "passphrase", Label: "Passphrase", Secret: true},
		{Name: "public_key", Label: "Public key", Multiline: true},
		{Name: "certificate", Label: "Certificate", Multiline: true},
		{Name: "username", Label: "Default username"},
	}},
	{Type: "login", Label: "Login", Fields: []FieldDef{
		{Name: "username", Label: "Username"},
		{Name: "password", Label: "Password", Secret: true},
		{Name: "url", Label: "URL"},
		{Name: "totp", Label: "TOTP secret / otpauth URI", Secret: true},
	}},
	{Type: "token", Label: "API token", Fields: []FieldDef{
		{Name: "token", Label: "Token", Secret: true, Required: true},
		{Name: "url", Label: "Service URL"},
		{Name: "expires", Label: "Expires"},
	}},
	{Type: "kubeconfig", Label: "Kubeconfig", Fields: []FieldDef{
		{Name: "kubeconfig", Label: "Kubeconfig", Secret: true, Multiline: true, Required: true},
		{Name: "context", Label: "Default context"},
	}},
	{Type: "vpn", Label: "VPN config", Fields: []FieldDef{
		{Name: "config", Label: "WireGuard config (.conf)", Secret: true, Multiline: true, Required: true},
	}},
	{Type: "certificate", Label: "Certificate", Fields: []FieldDef{
		{Name: "certificate", Label: "Certificate chain (PEM)", Multiline: true, Required: true},
		{Name: "private_key", Label: "Private key (PEM)", Secret: true, Multiline: true},
		{Name: "passphrase", Label: "Passphrase", Secret: true},
	}},
	{Type: "file", Label: "Secret file", Fields: []FieldDef{
		{Name: "filename", Label: "File name"},
		{Name: "content", Label: "Content", Secret: true, Multiline: true, Required: true},
	}},
	{Type: "note", Label: "Secure note", Fields: []FieldDef{
		{Name: "content", Label: "Content", Secret: true, Multiline: true, Required: true},
	}},
}

func typeDef(t string) (TypeDef, bool) {
	for _, d := range Types {
		if d.Type == t {
			return d, true
		}
	}
	return TypeDef{}, false
}

// normalize validates an item and makes the secret flag follow the type
// definition for known fields (custom fields keep the caller's flag).
func normalize(it *Item) error {
	it.Name = strings.TrimSpace(it.Name)
	if it.Name == "" {
		return fmt.Errorf("name is required")
	}
	if strings.ContainsAny(it.Name, "/#?") {
		return fmt.Errorf("name must not contain '/', '#' or '?'")
	}
	def, ok := typeDef(it.Type)
	if !ok {
		return fmt.Errorf("unknown item type %q", it.Type)
	}
	known := map[string]FieldDef{}
	for _, f := range def.Fields {
		known[f.Name] = f
	}
	seen := map[string]bool{}
	out := it.Fields[:0]
	for _, f := range it.Fields {
		f.Name = strings.TrimSpace(f.Name)
		if f.Name == "" || seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		if d, ok := known[f.Name]; ok {
			f.Secret = d.Secret
		}
		out = append(out, f)
	}
	it.Fields = out
	for _, d := range def.Fields {
		if d.Required && it.Get(d.Name) == "" {
			return fmt.Errorf("field %q is required", d.Label)
		}
	}
	if it.Tags == nil {
		it.Tags = []string{}
	}
	return nil
}

// ParseRef splits "vault://scope/name#field".
func ParseRef(ref string) (scope, name, field string, err error) {
	rest, ok := strings.CutPrefix(ref, "vault://")
	if !ok {
		return "", "", "", fmt.Errorf("invalid vault reference %q", ref)
	}
	rest, field, _ = strings.Cut(rest, "#")
	scope, name, ok = strings.Cut(rest, "/")
	if !ok || scope == "" || name == "" {
		return "", "", "", fmt.Errorf("invalid vault reference %q (want vault://scope/name)", ref)
	}
	return scope, name, field, nil
}
