package agent

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	r := NewRedactor(func() map[string]string { return map[string]string{"hunter2-long": "vault://acme/db#password"} })
	in := `DB_PASSWORD=abc123xyz
POSTGRES_PASSWORD: s3cretvalue
"MYSQL_ROOT_PASSWORD=rootpw99",
aws_secret_access_key = AKIAxxxxSECRETxxxx
password is hunter2-long
Authorization: Bearer abcdefghijklmnop
url=postgres://app:pa55word@db:5432/x
-----BEGIN OPENSSH PRIVATE KEY-----
abc
-----END OPENSSH PRIVATE KEY-----
port=5432 user=app`
	out := r.Redact(in)
	for _, leak := range []string{"abc123xyz", "s3cretvalue", "rootpw99", "SECRETxxxx", "hunter2-long", "abcdefghijklmnop", "pa55word", "BEGIN OPENSSH"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q in:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "port=5432 user=app") {
		t.Errorf("over-redacted:\n%s", out)
	}
}
