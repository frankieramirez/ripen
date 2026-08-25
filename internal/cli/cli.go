// Package cli is Ripen's command surface. Every verb answers with one
// Response envelope on stdout — success and failure alike — unless a
// read command is given --pretty, which renders the same payload as
// text. Pretty is never inferred from a TTY: an agent in a pty still
// gets the envelope. Humans get a second line on stderr when something
// failed; machines can ignore stderr entirely.
//
// Exit codes are the other half of the contract: 0 success, 1
// operational failure, 2 configuration or usage, and 3 read narrowly as
// "a human needs to look at this" — an open breaker or a failed
// rollback, nothing else.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/frankieramirez/ripen/internal/app"
	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/daemon"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/mcpserver"
	"github.com/frankieramirez/ripen/internal/response"
	"github.com/frankieramirez/ripen/internal/state"
	"github.com/frankieramirez/ripen/internal/updater"
	"github.com/frankieramirez/ripen/internal/webui"
)

// Exit codes. Three is deliberately narrow: it means the Circuit breaker
// is open or a rollback failed, and a person has to decide what happens
// next. Everything else operational is 1.
const (
	ExitOK        = 0
	ExitOperation = 1
	ExitUsage     = 2
	ExitAttention = 3
)

// DefaultConfigPath is where Ripen looks when nothing says otherwise.
const DefaultConfigPath = "/etc/ripen/policy.yaml"

// Run executes one command and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithInput(args, os.Stdin, stdout, stderr)
}

// RunWithInput is Run with an explicit input stream, which only the MCP
// server needs: its transport is stdin and stdout.
func RunWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return write(stdout, stderr, response.Fail("", now(), response.CodeUsage,
			"a command is required"), ExitUsage, false)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "-h", "--help", "help":
		usage(stderr)
		return ExitOK
	}

	// Two verbs own their process and never write a Response envelope.
	// The daemon's whole output is the Event stream on stderr; the MCP
	// server's stdout belongs to the protocol alone.
	switch command {
	case "daemon":
		return daemonVerb(rest, stderr)
	case "mcp":
		return mcpVerb(rest, stdin, stdout, stderr)
	}

	envelope, code, pretty := dispatch(command, rest, stderr)
	return write(stdout, stderr, envelope, code, pretty)
}

func write(stdout, stderr io.Writer, envelope response.Envelope, code int, pretty bool) int {
	var err error
	if pretty {
		err = writePretty(stdout, envelope)
	} else {
		err = response.Write(stdout, envelope)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ripen: could not write the response: %v\n", err)
		return ExitOperation
	}
	if !envelope.OK && envelope.Error != nil {
		// The envelope is the contract; this line is for the person
		// reading a terminal, and never carries a stack trace.
		fmt.Fprintf(stderr, "ripen: %s: %s\n", envelope.Error.Code, envelope.Error.Message)
	}
	return code
}

func dispatch(command string, args []string, stream io.Writer) (response.Envelope, int, bool) {
	switch command {
	case "version":
		return response.Succeed("version", now(), response.Version{Versions: app.Versions()}), ExitOK, false
	case "schema":
		return response.Succeed("schema", now(), response.SchemaSet{
			SchemaVersion: response.SchemaVersion,
			Schemas:       response.Schemas(),
		}), ExitOK, false
	case "notify":
		if len(args) == 0 || args[0] != "test" {
			return response.Fail("notify", now(), response.CodeUsage,
				"the only notify subcommand is `notify test`"), ExitUsage, false
		}
		return withApp("notify-test", args[1:], stream)
	case "status", "candidates", "audit", "explain", "run", "propose", "clear-proposal", "clear-breaker":
		return withApp(command, args, stream)
	default:
		return response.Fail(command, now(), response.CodeUsage,
			fmt.Sprintf("unknown command %q", command)), ExitUsage, false
	}
}

// withApp runs the verbs that need a policy and a state store.
func withApp(command string, args []string, stream io.Writer) (response.Envelope, int, bool) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", defaultConfigPath(), "path to the policy file")

	options := registerFlags(command, flags)
	// Parse in rounds so flags may follow the positional argument:
	// `ripen clear-proposal media --reason ...` is the natural way to
	// type it, and the standard flag package stops at the first
	// non-flag token.
	remaining := args
	for {
		if err := flags.Parse(remaining); err != nil {
			return response.Fail(command, now(), response.CodeUsage, err.Error()), ExitUsage, options.pretty
		}
		rest := flags.Args()
		if len(rest) == 0 {
			break
		}
		options.arguments = append(options.arguments, rest[0])
		remaining = rest[1:]
	}

	loaded, err := app.Open(*configPath)
	if err != nil {
		return response.Fail(command, now(), response.CodeConfigInvalid, err.Error()), ExitUsage, options.pretty
	}
	defer func() { _ = loaded.Close() }()

	envelope, code := execute(loaded, command, options, stream)
	return envelope, code, options.pretty
}

// verbOptions is every flag any verb takes. One struct keeps the
// dispatch flat; each verb registers only the flags it accepts, so an
// unknown flag is still a usage error.
type verbOptions struct {
	arguments []string
	pretty    bool
	mode      string
	reason    string
	limit     int
	cursor    string
	runID     string
	stack     string
	service   string
	backend   string
	result    string
}

func registerFlags(command string, flags *flag.FlagSet) *verbOptions {
	options := &verbOptions{}
	switch command {
	case "status", "candidates", "audit", "explain":
		flags.BoolVar(&options.pretty, "pretty", false, "render the same payload as text")
	}
	switch command {
	case "run":
		flags.StringVar(&options.mode, "mode", "", "monitor or apply; defaults to the configured mode")
	case "audit":
		flags.IntVar(&options.limit, "limit", 50, "maximum attempts to return")
		flags.StringVar(&options.cursor, "cursor", "", "continue from a previous page's next_cursor")
		flags.StringVar(&options.runID, "run", "", "only attempts from one run")
		flags.StringVar(&options.stack, "stack", "", "only attempts for one stack")
		flags.StringVar(&options.service, "service", "", "only attempts for one service")
		flags.StringVar(&options.backend, "backend", "", "only attempts for one backend")
		flags.StringVar(&options.result, "result", "", "only attempts with one result code")
	case "clear-breaker", "clear-proposal":
		flags.StringVar(&options.reason, "reason", "", "why this is being cleared; recorded")
	}
	return options
}

func execute(loaded *app.App, command string, options *verbOptions,
	stream io.Writer) (response.Envelope, int) {
	switch command {
	case "status":
		return read(loaded, command, func() (any, error) { return loaded.Status() })
	case "candidates":
		return read(loaded, command, func() (any, error) { return loaded.Candidates() })
	case "audit":
		return auditVerb(loaded, options)
	case "explain":
		stack, envelope, ok := argument(command, options, "a stack name is required")
		if !ok {
			return envelope, ExitUsage
		}
		return read(loaded, command, func() (any, error) { return loaded.Explain(stack) })
	case "run":
		return runVerb(loaded, options, stream)
	case "propose":
		return proposeVerb(loaded, options, stream)
	case "clear-proposal":
		return clearProposalVerb(loaded, options, stream)
	case "clear-breaker":
		return clearBreakerVerb(loaded, options, stream)
	case "notify-test":
		return notifyTestVerb(loaded, stream)
	default:
		return response.Fail(command, now(), response.CodeUsage,
			fmt.Sprintf("unknown command %q", command)), ExitUsage
	}
}

func read(_ *app.App, command string, build func() (any, error)) (response.Envelope, int) {
	data, err := build()
	if err != nil {
		return failure(command, err)
	}
	return response.Succeed(command, now(), data), ExitOK
}

func auditVerb(loaded *app.App, options *verbOptions) (response.Envelope, int) {
	filter := state.AuditFilter{
		Limit:   options.limit,
		RunID:   options.runID,
		Backend: domain.Backend(options.backend),
		Stack:   options.stack,
		Service: options.service,
		Result:  domain.ResultCode(options.result),
	}
	if options.cursor != "" {
		cursor, err := strconv.ParseInt(options.cursor, 10, 64)
		if err != nil {
			return response.Fail("audit", now(), response.CodeUsage,
				"cursor must be a value from a previous page's next_cursor"), ExitUsage
		}
		filter.Cursor = cursor
	}
	audit, err := loaded.Audit(filter)
	if err != nil {
		return failure("audit", err)
	}
	return response.Succeed("audit", now(), audit), ExitOK
}

func runVerb(loaded *app.App, options *verbOptions, stream io.Writer) (response.Envelope, int) {
	mode := loaded.Policy.Mode
	if options.mode != "" {
		parsed, err := domain.ParseMode(options.mode)
		if err != nil {
			return response.Fail("run", now(), response.CodeUsage, err.Error()), ExitUsage
		}
		mode = parsed
	}
	engine, drain, err := engineFor(loaded, stream)
	if err != nil {
		return failure("run", err)
	}
	defer drain()
	report, err := engine.Run(mode)
	if err != nil {
		return failure("run", err)
	}

	code := ExitOK
	if app.NeedsAttention(report) {
		code = ExitAttention
	}
	return response.Succeed("run", now(), app.RunPayload(report)), code
}

func proposeVerb(loaded *app.App, options *verbOptions, stream io.Writer) (response.Envelope, int) {
	stack, envelope, ok := argument("propose", options, "a stack name is required")
	if !ok {
		return envelope, ExitUsage
	}
	engine, drain, err := engineFor(loaded, stream)
	if err != nil {
		return failure("propose", err)
	}
	defer drain()
	result, runID, err := engine.Propose(stack)
	if err != nil {
		return failure("propose", err)
	}
	switch result.Code {
	case domain.ResultProposed:
		return response.Succeed("propose", now(), app.ProposedPayload(result, runID)), ExitOK
	case domain.ResultBreakerOpen:
		return response.Fail("propose", now(), response.CodeBreakerOpen, result.Detail), ExitAttention
	case domain.ResultBusy:
		return response.Fail("propose", now(), response.CodeStateLocked, result.Detail), ExitOperation
	case domain.ResultEngineUnavailable:
		return response.Fail("propose", now(), response.CodeBackendUnavailable, result.Detail), ExitOperation
	default:
		return response.Fail("propose", now(), response.CodePreconditionFailed, result.Detail), ExitOperation
	}
}

func clearProposalVerb(loaded *app.App, options *verbOptions,
	stream io.Writer) (response.Envelope, int) {
	stack, envelope, ok := argument("clear-proposal", options, "a stack name is required")
	if !ok {
		return envelope, ExitUsage
	}
	if strings.TrimSpace(options.reason) == "" {
		return response.Fail("clear-proposal", now(), response.CodeUsage,
			"--reason is required: clearing a proposal is a decision, and it is recorded"), ExitUsage
	}
	engine, drain, err := engineFor(loaded, stream)
	if err != nil {
		return failure("clear-proposal", err)
	}
	defer drain()
	status, err := engine.ClearProposal(stack, options.reason)
	if err != nil {
		return response.Fail("clear-proposal", now(), response.CodeNotFound, err.Error()), ExitOperation
	}
	return response.Succeed("clear-proposal", now(), response.Acknowledged{
		Changed: true,
		Reason:  options.reason,
		Breaker: response.Breaker{Open: status.BreakerOpen, Reason: response.Optional(status.BreakerReason)},
		Detail:  fmt.Sprintf("cleared the pending proposal for %q", stack),
	}), ExitOK
}

func clearBreakerVerb(loaded *app.App, options *verbOptions,
	stream io.Writer) (response.Envelope, int) {
	if strings.TrimSpace(options.reason) == "" {
		return response.Fail("clear-breaker", now(), response.CodeUsage,
			"--reason is required: an operator has to say what they fixed"), ExitUsage
	}
	engine, drain, err := engineFor(loaded, stream)
	if err != nil {
		return failure("clear-breaker", err)
	}
	defer drain()
	status, err := engine.ClearBreaker(options.reason)
	if err != nil {
		return failure("clear-breaker", err)
	}
	return response.Succeed("clear-breaker", now(), response.Acknowledged{
		Changed: true,
		Reason:  options.reason,
		Breaker: response.Breaker{Open: status.BreakerOpen, Reason: response.Optional(status.BreakerReason)},
		Detail:  "the circuit breaker is closed",
	}), ExitOK
}

// engineFor builds the write path with its Event stream attached. The
// returned function drains the Notifier: delivery is asynchronous, and a
// CLI process that exits immediately would otherwise take the queue with
// it.
func engineFor(loaded *app.App, stream io.Writer) (*updater.Updater, func(), error) {
	events, webhook, err := loaded.Events(domain.ActorCLI, stream)
	if err != nil {
		return nil, nil, err
	}
	drain := func() {
		if webhook != nil {
			_ = webhook.Close()
		}
	}
	engine, err := loaded.Updater(domain.ActorCLI, events)
	if err != nil {
		drain()
		return nil, nil, err
	}
	return engine, drain, nil
}

func notifyTestVerb(loaded *app.App, stream io.Writer) (response.Envelope, int) {
	if loaded.Policy.Notifier == nil || loaded.Policy.Notifier.Webhook == nil {
		return response.Fail("notify-test", now(), response.CodePreconditionFailed,
			"no notifier is configured, so there is nothing to test"), ExitOperation
	}
	_, webhook, err := loaded.Events(domain.ActorCLI, stream)
	if err != nil {
		return failure("notify-test", err)
	}
	defer func() { _ = webhook.Close() }()

	delivery := webhook.Test()
	health, err := loaded.NotifierHealth(webhook.Dropped())
	if err != nil {
		return failure("notify-test", err)
	}
	if delivery != nil {
		return response.Fail("notify-test", now(), response.CodeBackendUnavailable,
			delivery.Error()), ExitOperation
	}
	return response.Succeed("notify-test", now(), response.NotifyTest{
		Delivered: true,
		Detail:    "the webhook accepted a notifier.test event",
		Health:    health,
	}), ExitOK
}

// daemonVerb runs the scheduled loop. It writes nothing to stdout, ever.
func daemonVerb(args []string, stream io.Writer) int {
	flags := flag.NewFlagSet("daemon", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", defaultConfigPath(), "path to the policy file")
	mode := flags.String("mode", "", "monitor or apply; defaults to the configured mode")
	once := flags.Bool("once", false, "run one cycle and exit")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stream, "ripen: usage: %v\n", err)
		return ExitUsage
	}

	loaded, err := app.Open(*configPath)
	if err != nil {
		fmt.Fprintf(stream, "ripen: config_invalid: %v\n", err)
		return ExitUsage
	}
	defer func() { _ = loaded.Close() }()

	selected := loaded.Policy.Mode
	if *mode != "" {
		parsed, err := domain.ParseMode(*mode)
		if err != nil {
			fmt.Fprintf(stream, "ripen: usage: %v\n", err)
			return ExitUsage
		}
		selected = parsed
	}

	events, webhook, err := loaded.Events(domain.ActorDaemon, stream)
	if err != nil {
		fmt.Fprintf(stream, "ripen: %v\n", err)
		return ExitOperation
	}
	defer func() {
		if webhook != nil {
			_ = webhook.Close()
		}
	}()
	engine, err := loaded.Updater(domain.ActorDaemon, events)
	if err != nil {
		fmt.Fprintf(stream, "ripen: %v\n", err)
		return ExitOperation
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The Web UI is off unless the policy turns it on. Its listener is
	// bound before the loop starts, so a taken port or an exposed bind
	// without a token fails now rather than quietly later.
	if settings := loaded.Policy.UI; settings != nil && settings.Enabled {
		token, err := webui.ReadToken(settings.TokenFile)
		if err != nil {
			fmt.Fprintf(stream, "ripen: %v\n", err)
			return ExitOperation
		}
		ui, err := webui.New(webui.Options{
			App:     loaded,
			Address: settings.Address,
			Token:   token,
		})
		if err != nil {
			fmt.Fprintf(stream, "ripen: %v\n", err)
			return ExitOperation
		}
		listener, err := net.Listen("tcp", ui.Address())
		if err != nil {
			fmt.Fprintf(stream, "ripen: the web ui could not bind: %v\n", err)
			return ExitOperation
		}
		go func() {
			if err := ui.Serve(ctx, listener); err != nil {
				fmt.Fprintf(stream, "ripen: the web ui stopped: %v\n", err)
			}
		}()
	}

	if err := daemon.Run(ctx, daemon.Options{
		Updater:  engine,
		Mode:     selected,
		Interval: time.Duration(loaded.Policy.CheckIntervalSeconds) * time.Second,
		Once:     *once,
	}); err != nil {
		fmt.Fprintf(stream, "ripen: %v\n", err)
		return ExitOperation
	}
	return ExitOK
}

// mcpVerb serves MCP over stdio. Nothing but the protocol is ever
// written to stdout — not a log line, not an envelope, not a warning.
func mcpVerb(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", defaultConfigPath(), "path to the policy file")
	enableWrites := flags.Bool("enable-writes", false,
		"register the three write tools; apply mode and clear-breaker are never available")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "ripen: usage: %v\n", err)
		return ExitUsage
	}

	loaded, err := app.Open(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "ripen: config_invalid: %v\n", err)
		return ExitUsage
	}
	defer func() { _ = loaded.Close() }()

	server, err := mcpserver.New(mcpserver.Options{
		App:          loaded,
		EnableWrites: *enableWrites,
		Stream:       stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ripen: %v\n", err)
		return ExitOperation
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	transport := &mcp.IOTransport{
		Reader: io.NopCloser(stdin),
		Writer: nopCloser{stdout},
	}
	if err := server.Run(ctx, transport); err != nil && !sessionEnded(err) {
		fmt.Fprintf(stderr, "ripen: %v\n", err)
		return ExitOperation
	}
	return ExitOK
}

// sessionEnded reports whether the server stopped because the client
// hung up, which is how an MCP session normally ends — the host closes
// the pipe — and not a failure to report.
func sessionEnded(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return true
	}
	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == serverClosingCode
}

// serverClosingCode is the JSON-RPC code the SDK uses for calls that
// arrive while the connection is shutting down.
const serverClosingCode = -32004

// nopCloser lets the transport own a writer it must not close: stdout
// belongs to the process, not to one session.
type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error { return nil }

func argument(command string, options *verbOptions, message string) (string, response.Envelope, bool) {
	if len(options.arguments) != 1 || options.arguments[0] == "" {
		return "", response.Fail(command, now(), response.CodeUsage, message), false
	}
	return options.arguments[0], response.Envelope{}, true
}

// failure maps an operational error to its envelope. Adapter and engine
// failures are reported, never raised as a stack trace.
func failure(command string, err error) (response.Envelope, int) {
	var engine *backend.EngineUnavailableError
	switch {
	case errors.As(err, &engine):
		return response.Fail(command, now(), response.CodeBackendUnavailable, err.Error()), ExitOperation
	case errors.Is(err, updater.ErrUnknownStack):
		return response.Fail(command, now(), response.CodeNotFound, err.Error()), ExitOperation
	case errors.Is(err, updater.ErrNotProposable):
		return response.Fail(command, now(), response.CodePreconditionFailed, err.Error()), ExitOperation
	default:
		return response.Fail(command, now(), response.CodeInternal, err.Error()), ExitOperation
	}
}

func defaultConfigPath() string {
	if path := os.Getenv("RIPEN_CONFIG"); path != "" {
		return path
	}
	return DefaultConfigPath
}

func now() time.Time {
	return time.Now().UTC()
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: ripen <command> [flags]")
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "reads:")
	fmt.Fprintln(writer, "  status [--pretty]              every configured service and its baseline")
	fmt.Fprintln(writer, "  candidates [--pretty]          digests under observation")
	fmt.Fprintln(writer, "  audit [--pretty] [--run id] [--limit]  what Ripen has done")
	fmt.Fprintln(writer, "  explain [--pretty] <stack>     why the next run would or would not act")
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "writes:")
	fmt.Fprintln(writer, "  run [--mode monitor|apply]  one transaction over every enabled stack")
	fmt.Fprintln(writer, "  propose <stack>             open a digest-pin proposal")
	fmt.Fprintln(writer, "  clear-proposal <stack> --reason <why>")
	fmt.Fprintln(writer, "  clear-breaker --reason <why>")
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "  daemon [--once]             run on the configured interval")
	fmt.Fprintln(writer, "  notify test                 send a real event through the webhook")
	fmt.Fprintln(writer, "  mcp [--enable-writes]       serve the agent surface over stdio")
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "other:")
	fmt.Fprintln(writer, "  schema                      the response schemas")
	fmt.Fprintln(writer, "  version                     build metadata")
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "every command answers one JSON Response envelope on stdout.")
	fmt.Fprintln(writer, "status, candidates, audit, and explain accept --pretty to render the same payload as text.")
	fmt.Fprintln(writer, "--pretty is never inferred from a TTY.")
	fmt.Fprintln(writer, "--config <path> selects the policy file (default $RIPEN_CONFIG or "+DefaultConfigPath+").")
}
