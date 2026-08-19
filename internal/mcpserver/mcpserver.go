// Package mcpserver is Ripen's MCP surface: stdio, tools only, and a
// strict subset of the CLI. Every tool maps to a CLI verb with the same
// guard, the same parameters, and the same Response envelope as its
// payload, so an agent and an operator are looking at one system rather
// than two.
//
// Two things are absent by construction, not by permission check. Apply
// mode has no tool, and neither does clearing the Circuit breaker: those
// are decisions for a person at a terminal. And in the default read-only
// mode the write tools are never registered and the write path is never
// built, so the process holds no credentials and opens no clients.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/frankieramirez/ripen/internal/app"
	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/notifier"
	"github.com/frankieramirez/ripen/internal/response"
	"github.com/frankieramirez/ripen/internal/state"
	"github.com/frankieramirez/ripen/internal/updater"
	"github.com/frankieramirez/ripen/internal/version"
)

// Options configures the MCP server.
type Options struct {
	App *app.App
	// EnableWrites is a process flag, never a tool parameter: what a
	// caller may do is decided when the server starts.
	EnableWrites bool
	// Stream receives the Event stream. It is stderr in production;
	// stdout belongs to the protocol alone.
	Stream io.Writer
}

// Server is a built MCP server and whatever it needs closing.
type Server struct {
	server  *mcp.Server
	webhook *notifier.Webhook
	names   []string
}

// New builds the server and registers its tools. Registration is the
// filter: a read-only server has no write tools to refuse, because they
// were never added.
func New(options Options) (*Server, error) {
	if options.App == nil {
		return nil, fmt.Errorf("an mcp server needs a loaded policy")
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ripen",
		Title:   "Ripen",
		Version: version.Version,
	}, &mcp.ServerOptions{
		Instructions: "Ripen is a fail-closed image updater. Reads are always available. " +
			"Applying an update and clearing the circuit breaker are deliberately absent: " +
			"they belong to a person at a terminal.",
	})
	built := &Server{server: server}
	registerReads(built, options.App)
	if !options.EnableWrites {
		return built, nil
	}

	events, webhook, err := options.App.Events(domain.ActorMCP, options.Stream)
	if err != nil {
		return nil, err
	}
	built.webhook = webhook
	engine, err := options.App.Updater(domain.ActorMCP, events)
	if err != nil {
		return nil, err
	}
	registerWrites(built, engine)
	return built, nil
}

// Run serves until the transport closes or the context ends.
func (s *Server) Run(ctx context.Context, transport mcp.Transport) error {
	defer func() {
		if s.webhook != nil {
			_ = s.webhook.Close()
		}
	}()
	return s.server.Run(ctx, transport)
}

// Tools lists the registered tool names. Registration is the whole
// read-only guarantee: what is not here was never added.
func (s *Server) Tools() []string {
	return append([]string(nil), s.names...)
}

// addTool registers one tool and remembers its name.
func addTool[In any](server *Server, tool *mcp.Tool, command string, handle func(In) (any, error)) {
	mcp.AddTool(server.server, tool, answer(command, handle))
	server.names = append(server.names, tool.Name)
}

// --- tool inputs ---

// noInput is a tool that takes nothing.
type noInput struct{}

type auditInput struct {
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum attempts to return; defaults to 50"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"continue from a previous page's next_cursor"`
	Run     string `json:"run,omitempty" jsonschema:"only attempts from one run id"`
	Backend string `json:"backend,omitempty" jsonschema:"only attempts for one backend"`
	Stack   string `json:"stack,omitempty" jsonschema:"only attempts for one stack"`
	Service string `json:"service,omitempty" jsonschema:"only attempts for one service"`
	Result  string `json:"result,omitempty" jsonschema:"only attempts with one result code"`
}

type stackInput struct {
	Stack string `json:"stack" jsonschema:"the stack name as the policy declares it"`
}

type clearProposalInput struct {
	Stack  string `json:"stack" jsonschema:"the stack whose pending proposal is being cleared"`
	Reason string `json:"reason" jsonschema:"why it is being cleared; recorded in the audit trail"`
}

// --- registration ---

func registerReads(server *Server, loaded *app.App) {
	addTool(server, &mcp.Tool{
		Name:  "status",
		Title: "Ripen status",
		Description: "Every configured service with its baseline digest, candidate, pending proposal, " +
			"and last result, plus the circuit breaker and the effective policy. " +
			"Maps to the `ripen status` command.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, "status", func(noInput) (any, error) { return loaded.Status() })

	addTool(server, &mcp.Tool{
		Name:  "candidates",
		Title: "Ripen candidates",
		Description: "Digests under observation and whether each has matured past the waiting window. " +
			"Maps to the `ripen candidates` command.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, "candidates", func(noInput) (any, error) { return loaded.Candidates() })

	addTool(server, &mcp.Tool{
		Name:  "audit",
		Title: "Ripen audit trail",
		Description: "What Ripen has done, newest first, from the durable audit trail. " +
			"Maps to the `ripen audit` command.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, "audit", func(input auditInput) (any, error) {
		return loaded.Audit(state.AuditFilter{
			Limit:   input.Limit,
			Cursor:  cursorOf(input.Cursor),
			RunID:   input.Run,
			Backend: domain.Backend(input.Backend),
			Stack:   input.Stack,
			Service: input.Service,
			Result:  domain.ResultCode(input.Result),
		})
	})

	addTool(server, &mcp.Tool{
		Name:  "explain",
		Title: "Explain a stack",
		Description: "Why the next run would, or would not, act on one stack, with the blockers listed " +
			"in the order a run would hit them. Reads policy and state only. " +
			"Maps to the `ripen explain <stack>` command.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, "explain", func(input stackInput) (any, error) { return loaded.Explain(input.Stack) })
}

func registerWrites(server *Server, engine *updater.Updater) {
	addTool(server, &mcp.Tool{
		Name:  "run_monitor_cycle",
		Title: "Run one monitor cycle",
		Description: "Observe every enabled stack and record baselines and candidates. " +
			"Monitor mode only: this can never deploy anything. " +
			"Maps to `ripen run --mode monitor`.",
	}, "run", func(noInput) (any, error) {
		report, err := engine.Run(domain.ModeMonitor)
		if err != nil {
			return nil, err
		}
		return app.RunPayload(report), nil
	})

	addTool(server, &mcp.Tool{
		Name:  "create_proposal",
		Title: "Open a digest-pin proposal",
		Description: "Open a pull request pinning one stack's matured candidate. Requires a configured " +
			"git_path, a closed breaker, and no proposal already under review. It never merges or " +
			"deploys. Maps to `ripen propose <stack>`.",
	}, "propose", func(input stackInput) (any, error) {
		result, runID, err := engine.Propose(input.Stack)
		if err != nil {
			return nil, err
		}
		if result.Code != domain.ResultProposed {
			return nil, fmt.Errorf("%s: %s", result.Code, result.Detail)
		}
		return app.ProposedPayload(result, runID), nil
	})

	addTool(server, &mcp.Tool{
		Name:  "clear_proposal",
		Title: "Clear a reviewed proposal",
		Description: "Drop a stale pending proposal after a human has reviewed it, so the service can " +
			"propose again. Maps to `ripen clear-proposal <stack> --reason <why>`.",
	}, "clear-proposal", func(input clearProposalInput) (any, error) {
		status, err := engine.ClearProposal(input.Stack, input.Reason)
		if err != nil {
			return nil, err
		}
		return response.Acknowledged{
			Changed: true,
			Reason:  input.Reason,
			Breaker: response.Breaker{
				Open:   status.BreakerOpen,
				Reason: response.Optional(status.BreakerReason),
			},
			Detail: fmt.Sprintf("cleared the pending proposal for %q", input.Stack),
		}, nil
	})
}

// answer adapts one read or write into a tool handler. Every result —
// success or failure — is the same Response envelope the CLI prints,
// carried as structuredContent. A failure sets isError so the caller can
// see it and correct itself; it is never a JSON-RPC protocol error.
func answer[In any](command string, handle func(In) (any, error)) mcp.ToolHandlerFor[In, response.Envelope] {
	return func(_ context.Context, _ *mcp.CallToolRequest, input In) (
		*mcp.CallToolResult, response.Envelope, error) {
		data, err := handle(input)
		if err != nil {
			envelope := response.Fail(command, time.Now().UTC(), errorCode(err), err.Error())
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			}, envelope, nil
		}
		return nil, response.Succeed(command, time.Now().UTC(), data), nil
	}
}

// errorCode classifies a failure into the same closed code set the CLI
// uses, so an agent reads the same error shape a person would.
func errorCode(err error) response.Code {
	var unavailable *backend.EngineUnavailableError
	switch {
	case errors.Is(err, updater.ErrUnknownStack):
		return response.CodeNotFound
	case errors.Is(err, updater.ErrNotProposable):
		return response.CodePreconditionFailed
	case errors.As(err, &unavailable):
		return response.CodeBackendUnavailable
	default:
		return response.CodePreconditionFailed
	}
}

func cursorOf(value string) int64 {
	var cursor int64
	_, _ = fmt.Sscanf(value, "%d", &cursor)
	return cursor
}
