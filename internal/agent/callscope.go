package agent

import (
	"context"
	"errors"
	"fmt"
)

// callScope is what a running tool call can do besides returning its
// output: report progress, attach a view the chat renders (search results,
// a chart), and ask the user to approve its next stage (the rest of a
// canary rollout).
type callScope struct {
	callID   string
	progress func(text string)
	confirm  func(ctx context.Context, step Step) (approved bool, note string)
	view     any
}

// Step is a further approval asked while a call runs.
type Step struct {
	Title   string // e.g. "Continue on the other 3 hosts"
	Reason  string
	Preview string
	Risk    string
}

type scopeKey struct{}

func withCallScope(ctx context.Context, sc *callScope) context.Context {
	return context.WithValue(ctx, scopeKey{}, sc)
}

func callScopeFrom(ctx context.Context) *callScope {
	sc, _ := ctx.Value(scopeKey{}).(*callScope)
	return sc
}

// reportProgress tells the chat what a long-running call is doing.
func reportProgress(ctx context.Context, format string, args ...any) {
	if sc := callScopeFrom(ctx); sc != nil && sc.progress != nil {
		sc.progress(fmt.Sprintf(format, args...))
	}
}

// setView attaches structured data the chat shows for the call. It is
// redacted like the output and never sent to the model.
func setView(ctx context.Context, v any) {
	if sc := callScopeFrom(ctx); sc != nil {
		sc.view = v
	}
}

// errNoConfirm is returned when a call asks for a further approval where
// nobody can give one.
var errNoConfirm = errors.New("no one can approve the next step here")

// confirmStep asks the user to approve the next stage of a running call.
func confirmStep(ctx context.Context, st Step) (bool, string, error) {
	sc := callScopeFrom(ctx)
	if sc == nil || sc.confirm == nil {
		return false, "", errNoConfirm
	}
	ok, note := sc.confirm(ctx, st)
	return ok, note, nil
}

// currentCallID is the id of the tool call running in ctx ("" outside one).
func currentCallID(ctx context.Context) string {
	if sc := callScopeFrom(ctx); sc != nil {
		return sc.callID
	}
	return ""
}
