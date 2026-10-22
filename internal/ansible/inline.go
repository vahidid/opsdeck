package ansible

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Inline values: a YAML file in plain text with single values encrypted,
// as made by `ansible-vault encrypt_string`:
//
//	db_password: !vault |
//	          $ANSIBLE_VAULT;1.1;AES256
//	          6638…

// Inline is one encrypted value of a YAML file.
type Inline struct {
	Key     string `json:"key"`  // the YAML key ("" for a list item)
	Line    int    `json:"line"` // 1-based line of the key
	End     int    `json:"end"`  // last line of the value (1-based, inclusive)
	VaultID string `json:"vaultId"`
	text    string // vault text without indentation
}

var inlineRe = regexp.MustCompile(`^(\s*)(?:-\s+)?(?:(["']?)([^"'#:]+)["']?\s*:\s*)?!vault\s*\|[-+0-9]*\s*$`)

// FindInline lists the inline vault values of a YAML text.
func FindInline(text string) []Inline {
	lines := strings.Split(text, "\n")
	var out []Inline
	for i := 0; i < len(lines); i++ {
		m := inlineRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		indent := len(m[1])
		var body []string
		j := i + 1
		for ; j < len(lines); j++ {
			l := lines[j]
			if strings.TrimSpace(l) == "" {
				break
			}
			if len(l)-len(strings.TrimLeft(l, " \t")) <= indent {
				break
			}
			body = append(body, strings.TrimSpace(l))
		}
		if len(body) == 0 || !strings.HasPrefix(body[0], vaultMagic+";") {
			continue
		}
		in := Inline{Key: strings.TrimSpace(m[3]), Line: i + 1, End: j, text: strings.Join(body, "\n")}
		if h, err := ParseHeader(in.text); err == nil {
			in.VaultID = h.ID
		}
		out = append(out, in)
		i = j - 1
	}
	return out
}

// DecryptValue opens one inline value.
func (in Inline) DecryptValue(password string) (string, error) {
	b, err := Decrypt(in.text, password)
	return string(b), err
}

// EncryptString returns the YAML for name: !vault | … (or only the tagged
// block when name is empty), indented by indent spaces.
func EncryptString(name, value, password, id string, indent int) (string, error) {
	text, err := Encrypt([]byte(value), password, id)
	if err != nil {
		return "", err
	}
	pre := strings.Repeat(" ", indent)
	var b strings.Builder
	if name != "" {
		b.WriteString(pre + name + ": ")
	}
	b.WriteString("!vault |\n")
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		b.WriteString(pre + "          " + l + "\n")
	}
	return b.String(), nil
}

// RevealInline returns text with every inline value it can open replaced by
// its plaintext (as a quoted YAML string, marked with a comment), and the
// number of values that stayed encrypted.
func RevealInline(text string, open func(Inline) (string, error)) (string, int) {
	lines := strings.Split(text, "\n")
	values := FindInline(text)
	locked := 0
	for k := len(values) - 1; k >= 0; k-- {
		in := values[k]
		plain, err := open(in)
		if err != nil {
			locked++
			continue
		}
		prefix := lines[in.Line-1][:strings.Index(lines[in.Line-1], "!vault")]
		repl := prefix + strconv.Quote(plain) + "  # !vault, decrypted by opsdeck"
		lines = append(lines[:in.Line-1], append([]string{repl}, lines[in.End:]...)...)
	}
	return strings.Join(lines, "\n"), locked
}

// SetYAMLKey sets a top-level key of a YAML mapping (block style) to a
// literal value, keeping every other line as it is: the key's line (and
// the indented lines of its value) are replaced, or the key is appended.
// value is the YAML text after "key: " (already quoted or a !vault block).
func SetYAMLKey(text, key, value string) (string, error) {
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`).MatchString(key) {
		return "", fmt.Errorf("invalid variable name %q", key)
	}
	lines := strings.Split(text, "\n")
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || t == "---" || strings.HasPrefix(t, "#") {
			continue
		}
		if t == "-" || strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{") {
			return "", errors.New("the file is not a YAML mapping of variables")
		}
		break
	}
	keyRe := regexp.MustCompile(`^["']?` + regexp.QuoteMeta(key) + `["']?\s*:(\s|$)`)
	newLines := strings.Split(strings.TrimRight(key+": "+value, "\n"), "\n")
	for i, l := range lines {
		if !keyRe.MatchString(l) {
			continue
		}
		j := i + 1
		for ; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" {
				// a blank line inside a block scalar belongs to it only if
				// more indented lines follow
				k := j
				for k < len(lines) && strings.TrimSpace(lines[k]) == "" {
					k++
				}
				if k < len(lines) && (strings.HasPrefix(lines[k], " ") || strings.HasPrefix(lines[k], "\t")) {
					j = k
					continue
				}
				break
			}
			if !strings.HasPrefix(lines[j], " ") && !strings.HasPrefix(lines[j], "\t") {
				break
			}
		}
		out := append(append(append([]string{}, lines[:i]...), newLines...), lines[j:]...)
		return strings.Join(out, "\n"), nil
	}
	s := strings.TrimRight(text, "\n")
	if s != "" {
		s += "\n"
	}
	return s + strings.Join(newLines, "\n") + "\n", nil
}

// YAMLString quotes a value for SetYAMLKey.
func YAMLString(v string) string {
	if strings.Contains(v, "\n") {
		var b strings.Builder
		b.WriteString("|-\n")
		for _, l := range strings.Split(v, "\n") {
			b.WriteString("  " + l + "\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}
	return strconv.Quote(v)
}
