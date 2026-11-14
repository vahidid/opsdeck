package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opsdeck/internal/mcpx"
)

// Approvals in the connected harness: when an agent outside opsdeck calls a
// tool over MCP and its client can show a form (MCP elicitation), the
// approval the call waits for is sent to that client too. The card in the
// opsdeck app stays open; whichever answers first decides. The policy is
// unchanged: only calls opsdeck itself asks about are sent, never what the
// mode or the rules deny, and never a card that asks for a password.

// Elicitor asks the user of the connected client about an approval. It
// returns the decision ("approve" or "deny") and a note; ok is false when
// the user dismissed the form or the client could not ask, and the
// approval then waits in the app.
type Elicitor func(ctx context.Context, message string) (decision, note string, ok bool)

type elicitorKey struct{}

func withElicitor(ctx context.Context, e Elicitor) context.Context {
	return context.WithValue(ctx, elicitorKey{}, e)
}

func elicitorFrom(ctx context.Context) Elicitor {
	e, _ := ctx.Value(elicitorKey{}).(Elicitor)
	return e
}

// approvalSchema is the form the client shows: approve or deny, and a note.
var approvalSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"decision": map[string]any{"type": "string", "title": "Decision", "enum": []string{"approve", "deny"},
			"description": "approve runs it once; deny refuses it"},
		"note": map[string]any{"type": "string", "title": "Note", "description": "optional, passed to the agent with a denial"},
	},
	"required": []string{"decision"},
}

// askElicitor asks approvals through an MCP client (mcpx.Calls picks how
// for its protocol version); nil when the client cannot show a form.
func askElicitor(ask mcpx.Ask, meta mcp.Meta) Elicitor {
	if ask == nil {
		return nil
	}
	return func(ctx context.Context, message string) (string, string, bool) {
		res, err := ask(ctx, &mcp.ElicitParams{Meta: meta, Mode: "form", Message: message, RequestedSchema: approvalSchema})
		if err != nil || res == nil {
			return "", "", false
		}
		switch res.Action {
		case "accept":
			d, _ := res.Content["decision"].(string)
			note, _ := res.Content["note"].(string)
			if d != "approve" && d != "deny" {
				return "", "", false
			}
			return d, strings.TrimSpace(note), true
		case "decline":
			return "deny", "", true
		}
		return "", "", false // cancel: the user did not choose
	}
}

// approvalMessage is what the client shows: the same facts as the card in
// the app (already redacted).
func approvalMessage(req map[string]any) string {
	str := func(k string) string { s, _ := req[k].(string); return s }
	var b strings.Builder
	if str("stage") == "next" {
		fmt.Fprintf(&b, "opsdeck: continue %s?", firstNonEmpty(str("title"), str("tool")))
	} else {
		fmt.Fprintf(&b, "opsdeck asks for your approval: %s", str("tool"))
		if t := str("target"); t != "" {
			fmt.Fprintf(&b, " on %s", t)
		}
	}
	if env := str("environment"); env != "" && env != "internet" {
		fmt.Fprintf(&b, " in %s", env)
	}
	if r := str("risk"); r != "" {
		fmt.Fprintf(&b, " (%s)", r)
	}
	b.WriteString(".")
	if r := str("reason"); r != "" {
		fmt.Fprintf(&b, "\nWhy it asks: %s", r)
	}
	if hosts, _ := req["hosts"].([]string); len(hosts) > 1 {
		fmt.Fprintf(&b, "\nHosts: %s", strings.Join(hosts, ", "))
		if req["canary"] == true {
			fmt.Fprintf(&b, " (approving runs on %s first, then asks again)", hosts[0])
		}
	}
	if p := str("preview"); p != "" {
		if r := []rune(p); len(r) > 4000 {
			p = string(r[:4000]) + "\n…"
		}
		b.WriteString("\n\n" + p)
	}
	b.WriteString("\n\nThe same request is open in the opsdeck app; the first answer counts.")
	return b.String()
}
