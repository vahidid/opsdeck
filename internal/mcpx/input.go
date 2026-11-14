package mcpx

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Asking the user of an MCP client a question in the middle of a tool call
// (an approval). Clients show it as a form (elicitation). How it travels
// depends on the protocol version:
//
//   - before 2026-07-28 the server sends elicitation/create while the call
//     runs and the call waits for the answer;
//   - from 2026-07-28 (SEP-2322) a server may not send requests of its own
//     while it serves one: the call returns "input required" with the
//     question, and the client calls again with the answer. Calls keeps the
//     call running in between and picks it up again.

// Ask asks the client's user; nil when the client cannot show a form.
type Ask func(ctx context.Context, p *mcp.ElicitParams) (*mcp.ElicitResult, error)

// InputKey names the question in an input-required result.
const InputKey = "opsdeck"

// idleTimeout ends a call whose client asked nothing for this long after
// it was asked a question (it went away).
var idleTimeout = 30 * time.Minute

const modernVersion = "2026-07-28"

// SupportsForms says whether the client of an MCP session can show a form.
func SupportsForms(ss *mcp.ServerSession) bool {
	if ss == nil {
		return false
	}
	p := ss.InitializeParams()
	return p != nil && p.Capabilities != nil && p.Capabilities.Elicitation != nil
}

func modern(ss *mcp.ServerSession) bool {
	p := ss.InitializeParams()
	return p != nil && p.ProtocolVersion >= modernVersion
}

// Calls holds the tool calls that wait for their client to answer.
type Calls struct {
	mu    sync.Mutex
	calls map[string]*pausedCall
}

type pausedCall struct {
	owner  string // who may resume it (the workspace and session of the access key)
	asks   chan askReq
	answer chan answerMsg
	done   chan *mcp.CallToolResult
	cancel context.CancelFunc

	mu      sync.Mutex
	round   int
	idle    *time.Timer
	serving bool // a request waits for it; only one at a time
}

type askReq struct {
	round  int
	params *mcp.ElicitParams
}

type answerMsg struct {
	round int
	res   *mcp.ElicitResult
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// Run runs a tool call; run gets the Ask for this client (nil without
// forms). A call that comes back with the answer to a question resumes the
// call that asked it, if it has the same owner (an access key's session:
// one key can never answer or read another's call).
func (c *Calls) Run(ctx context.Context, owner string, req *mcp.CallToolRequest, run func(ctx context.Context, ask Ask) *mcp.CallToolResult) (*mcp.CallToolResult, error) {
	ss := req.Session
	if state := req.Params.RequestState; state != "" {
		return c.resume(ctx, owner, state, req.Params.InputResponses)
	}
	if !SupportsForms(ss) {
		return run(ctx, nil), nil
	}
	if !modern(ss) {
		return run(ctx, func(ctx context.Context, p *mcp.ElicitParams) (*mcp.ElicitResult, error) { return ss.Elicit(ctx, p) }), nil
	}
	// the call outlives this request: it may wait for the next one
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	pc := &pausedCall{owner: owner, asks: make(chan askReq), answer: make(chan answerMsg, 1), done: make(chan *mcp.CallToolResult, 1), cancel: cancel, serving: true}
	id := uuid.NewString()
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[string]*pausedCall{}
	}
	c.calls[id] = pc
	c.mu.Unlock()
	go func() { pc.done <- run(rctx, pc.ask) }()
	return c.wait(ctx, id, pc)
}

// ask hands the question to the request that waits (wait) and blocks
// until the client calls again with the answer of this round.
func (pc *pausedCall) ask(ctx context.Context, p *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	pc.mu.Lock()
	pc.round++
	round := pc.round
	pc.mu.Unlock()
	select {
	case pc.asks <- askReq{round, p}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for {
		select {
		case a := <-pc.answer:
			if a.round == round {
				if a.res == nil {
					return nil, errors.New("the client sent no answer")
				}
				return a.res, nil
			}
			// an answer to an earlier question, answered elsewhere meanwhile
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// wait returns the call's result, or its next question as input required.
// The caller holds pc.serving; wait gives it up.
func (c *Calls) wait(ctx context.Context, id string, pc *pausedCall) (*mcp.CallToolResult, error) {
	pc.mu.Lock()
	if pc.idle != nil {
		pc.idle.Stop()
		pc.idle = nil
	}
	pc.mu.Unlock()
	defer func() {
		pc.mu.Lock()
		pc.serving = false
		pc.mu.Unlock()
	}()
	select {
	case res := <-pc.done:
		c.drop(id)
		return res, nil
	case a := <-pc.asks:
		pc.mu.Lock()
		pc.idle = time.AfterFunc(idleTimeout, func() {
			pc.cancel()
			c.drop(id)
		})
		pc.mu.Unlock()
		return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{InputKey: a.params}, RequestState: id + ":" + strconv.Itoa(a.round)}, nil
	case <-ctx.Done():
		// the client hung up
		pc.cancel()
		c.drop(id)
		return nil, ctx.Err()
	}
}

func (c *Calls) resume(ctx context.Context, owner, state string, responses mcp.InputResponseMap) (*mcp.CallToolResult, error) {
	id, r, _ := strings.Cut(state, ":")
	round, _ := strconv.Atoi(r)
	c.mu.Lock()
	pc := c.calls[id]
	c.mu.Unlock()
	if pc == nil || pc.owner != owner {
		return errorResult("this request is no longer open in opsdeck (it timed out); call the tool again"), nil
	}
	pc.mu.Lock()
	if pc.serving {
		pc.mu.Unlock()
		return errorResult("this request is already being answered"), nil
	}
	pc.serving = true
	pc.mu.Unlock()
	res, _ := responses[InputKey].(*mcp.ElicitResult)
	select {
	case <-pc.answer: // drop an unread older answer
	default:
	}
	select {
	case pc.answer <- answerMsg{round, res}:
	case <-ctx.Done():
		pc.mu.Lock()
		pc.serving = false
		pc.mu.Unlock()
		return nil, ctx.Err()
	}
	return c.wait(ctx, id, pc)
}

func (c *Calls) drop(id string) {
	c.mu.Lock()
	delete(c.calls, id)
	c.mu.Unlock()
}
