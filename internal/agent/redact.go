package agent

import (
	"regexp"
	"sort"
	"strings"
)

// Redactor masks secrets before any text reaches a model: every secret value
// known to the vault, plus common credential patterns.
type Redactor struct {
	known func() map[string]string // value -> label
}

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), "[REDACTED PRIVATE KEY]"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[REDACTED AWS KEY ID]"},
	{regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})\b`), "[REDACTED GITHUB TOKEN]"},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`), "[REDACTED GITLAB TOKEN]"},
	{regexp.MustCompile(`\bsk-(ant-)?[A-Za-z0-9_\-]{20,}\b`), "[REDACTED API KEY]"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}\b`), "[REDACTED SLACK TOKEN]"},
	{regexp.MustCompile(`(?i)(authorization:\s*(bearer|basic)\s+)[A-Za-z0-9._~+/=\-]{8,}`), "${1}[REDACTED]"},
	// NAME=value / "name": "value" where the name contains a secret-ish word,
	// including prefixed names like DB_PASSWORD or aws_secret_access_key.
	{regexp.MustCompile(`(?i)\b([\w.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?)[\w.-]*["']?\s*[:=]\s*["']?)([^\s"',;]{4,})`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*://[^:/\s@]+:)([^@\s]{3,})@`), "${1}[REDACTED]@"},
	// WireGuard keys in configs
	{regexp.MustCompile(`(?i)((?:PrivateKey|PresharedKey)\s*=\s*)[A-Za-z0-9+/]{42,44}=?`), "${1}[REDACTED]"},
	// Kubernetes secret data blocks (base64 values under data:)
	{regexp.MustCompile(`(?m)^(\s+(?:token|password|tls\.key|client-key-data|client-certificate-data)\s*:\s*)\S{16,}$`), "${1}[REDACTED]"},
}

func NewRedactor(known func() map[string]string) *Redactor { return &Redactor{known: known} }

// Redact returns text with secrets masked.
func (r *Redactor) Redact(text string) string {
	if text == "" {
		return text
	}
	if r.known != nil {
		values := r.known()
		keys := make([]string, 0, len(values))
		for v := range values {
			keys = append(keys, v)
		}
		// Longest first so overlapping secrets are fully masked.
		sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
		for _, v := range keys {
			if strings.Contains(text, v) {
				text = strings.ReplaceAll(text, v, "[REDACTED "+values[v]+"]")
			}
			// Multi-line secrets (keys, configs) may appear line by line.
			if strings.Contains(v, "\n") {
				for _, line := range strings.Split(v, "\n") {
					line = strings.TrimSpace(line)
					if len(line) >= 24 && strings.Contains(text, line) {
						text = strings.ReplaceAll(text, line, "[REDACTED]")
					}
				}
			}
		}
	}
	for _, p := range secretPatterns {
		text = p.re.ReplaceAllString(text, p.repl)
	}
	return text
}
