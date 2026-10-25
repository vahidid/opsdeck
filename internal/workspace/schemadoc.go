package workspace

import (
	"fmt"
	"strings"
)

// SchemaMarkdown renders the resource kinds as docs/schema.md. The file is
// generated: a test fails when it no longer matches Kinds, and
// OPSDECK_UPDATE_DOCS=1 go test ./internal/workspace rewrites it.
func SchemaMarkdown() string {
	var b strings.Builder
	b.WriteString(`# Resource schema

<!-- Generated from internal/workspace/kinds.go by internal/workspace/schemadoc.go.
     Do not edit by hand: OPSDECK_UPDATE_DOCS=1 go test ./internal/workspace -->

The inventory of a workspace is a list of resources in ` + "`resources/*.yaml`" + ` (see
[workspace.md](workspace.md)), each with these common keys:

| Key | Meaning |
|---|---|
| ` + "`kind`" + ` | one of the kinds below |
| ` + "`name`" + ` | lower-case letters, digits and dashes; unique per kind |
| ` + "`environment`" + ` | one of the workspace's environments (` + "`prod`" + `, ` + "`staging`" + `…) |
| ` + "`tags`" + ` | free labels |
| ` + "`via`" + ` | how it is reached: ` + "`network/<name>`" + ` (a VPN or proxy) or ` + "`host/<name>`" + ` (a jump host); kinds marked *via* accept it |
| ` + "`description`" + `, ` + "`docs`" + ` | text for people and the agent; a path to a runbook |
| ` + "`spec`" + ` | the kind's own fields, below |

Secret fields (*vault reference*) hold ` + "`vault://<scope>/<item>[#field]`" + ` only, never the
secret itself. The format is ` + "`" + APIVersion + "`" + `; [compatibility.md](compatibility.md) says
what may change between versions.

`)
	b.WriteString("| Kind | Category | via |\n|---|---|---|\n")
	for _, k := range Kinds {
		via := ""
		if k.CanVia {
			via = "yes"
		}
		fmt.Fprintf(&b, "| [`%s`](#%s) | %s | %s |\n", k.Kind, strings.ToLower(k.Kind), k.Category, via)
	}
	for _, k := range Kinds {
		fmt.Fprintf(&b, "\n## %s\n\n%s.", k.Kind, strings.TrimSuffix(k.Label, "."))
		if k.CanVia {
			b.WriteString(" Accepts `via`.")
		}
		b.WriteString("\n\n| Field | Type | | Description |\n|---|---|---|---|\n")
		for _, f := range k.Fields {
			typ := f.Type
			switch f.Type {
			case "secretRef":
				typ = "vault reference"
				if len(f.ItemTypes) > 0 {
					typ += " (" + strings.Join(f.ItemTypes, ", ") + ")"
				}
			case "select":
				typ = "one of " + "`" + strings.Join(f.Options, "`, `") + "`"
			case "resourceRef":
				typ = "name of a " + strings.Join(f.RefKinds, "/")
			case "list":
				typ = "list"
				if len(f.RefKinds) > 0 {
					typ += " of " + strings.Join(f.RefKinds, "/") + " names"
				}
			case "gitConnection", "dnsConnection":
				typ = "connector id"
			}
			var flags []string
			if f.Required {
				flags = append(flags, "required")
			}
			if f.Default != "" {
				flags = append(flags, "default `"+f.Default+"`")
			}
			if f.ShowIf != "" {
				flags = append(flags, "with `"+f.ShowIf+"`")
			}
			desc := f.Label
			if f.Help != "" {
				desc += ": " + f.Help
			}
			if f.Placeholder != "" && f.Type != "secretRef" {
				desc += " (e.g. `" + f.Placeholder + "`)"
			}
			desc = strings.NewReplacer("|", "\\|", "\n", " ").Replace(desc)
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", f.Key, typ, strings.Join(flags, ", "), desc)
		}
	}
	return b.String()
}
