// Package webui is the optional read-only Web UI, embedded in the
// binary and served inside `ripen daemon`. It is off unless the policy
// turns it on.
//
// Read-only is structural, not a setting: there is no handler that
// writes anything. The breaker banner shows the command to clear the
// breaker as text to copy, because clearing it is a decision an operator
// makes deliberately at a terminal, with a reason — not a button they
// can hit while looking at a red box.
package webui

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/frankieramirez/ripen/internal/app"
	"github.com/frankieramirez/ripen/internal/response"
	"github.com/frankieramirez/ripen/internal/state"
)

//go:embed assets/*.html
var pages embed.FS

//go:embed static
var static embed.FS

const tokenCookie = "ripen_ui"

// Options configures the Web UI.
type Options struct {
	App     *app.App
	Address string
	// Token guards a non-loopback bind. Empty is allowed only on
	// loopback.
	Token string
}

// Server is the Web UI.
type Server struct {
	loaded    *app.App
	address   string
	token     string
	templates *template.Template
	server    *http.Server
}

// New builds the Web UI, refusing an exposed bind without a token.
// There is no insecure escape hatch: if Ripen is reachable from the
// network, something has to prove it is the operator.
func New(options Options) (*Server, error) {
	if options.App == nil {
		return nil, errors.New("the web ui needs a loaded policy")
	}
	address := options.Address
	if address == "" {
		address = "127.0.0.1:7476"
	}
	loopback, err := bindsToLoopback(address)
	if err != nil {
		return nil, err
	}
	if !loopback && options.Token == "" {
		return nil, fmt.Errorf(
			"ui.address %s is reachable from the network, so ui.token_file or RIPEN_UI_TOKEN is required",
			address)
	}
	templates, err := template.New("").Funcs(template.FuncMap{
		"short": shortDigest,
	}).ParseFS(pages, "assets/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		loaded:    options.App,
		address:   address,
		token:     options.Token,
		templates: templates,
	}, nil
}

// ReadToken finds the UI token: the environment first, then the file the
// policy names.
func ReadToken(tokenFile string) (string, error) {
	if token := strings.TrimSpace(os.Getenv("RIPEN_UI_TOKEN")); token != "" {
		return token, nil
	}
	if tokenFile == "" {
		return "", nil
	}
	raw, err := os.ReadFile(tokenFile) // #nosec G304 -- the token path is operator-supplied by design
	if err != nil {
		return "", fmt.Errorf("reading the ui token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("the ui token file is empty")
	}
	return token, nil
}

// Handler is the whole surface. Every route reads; none of them write.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = writer.Write([]byte("ok\n"))
	})

	mux.HandleFunc("GET /sign-in", s.signInPage)
	mux.HandleFunc("POST /sign-in", s.signIn)

	mux.Handle("GET /static/", s.guard(http.FileServerFS(static)))
	mux.Handle("GET /{$}", s.guard(http.HandlerFunc(s.overview)))
	mux.Handle("GET /audit", s.guard(http.HandlerFunc(s.audit)))
	mux.Handle("GET /policy", s.guard(http.HandlerFunc(s.policy)))
	mux.Handle("GET /api/status", s.guard(http.HandlerFunc(s.statusAPI)))
	mux.Handle("GET /api/audit", s.guard(http.HandlerFunc(s.auditAPI)))
	return mux
}

// ListenAndServe runs the UI until the context ends.
func (s *Server) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}

// Serve runs the UI on an existing listener.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	s.server = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdown)
	}()
	if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Address is where the UI is listening, useful once a port was chosen
// for it.
func (s *Server) Address() string { return s.address }

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if s.token == "" || s.authorized(request) {
			next.ServeHTTP(writer, request)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/api/") {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="ripen"`)
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Redirect(writer, request, "/sign-in", http.StatusSeeOther)
	})
}

func (s *Server) authorized(request *http.Request) bool {
	header := request.Header.Get("Authorization")
	if token, found := strings.CutPrefix(header, "Bearer "); found && s.matches(token) {
		return true
	}
	cookie, err := request.Cookie(tokenCookie)
	return err == nil && s.matches(cookie.Value)
}

func (s *Server) matches(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(candidate)), []byte(s.token)) == 1
}

func (s *Server) signInPage(writer http.ResponseWriter, request *http.Request) {
	if s.token == "" || s.authorized(request) {
		http.Redirect(writer, request, "/", http.StatusSeeOther)
		return
	}
	s.render(writer, "sign-in.html", map[string]any{"Title": "Sign in"})
}

func (s *Server) signIn(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	if !s.matches(request.PostForm.Get("token")) {
		writer.WriteHeader(http.StatusUnauthorized)
		s.render(writer, "sign-in.html", map[string]any{
			"Title": "Sign in",
			"Error": "That token does not match.",
		})
		return
	}
	http.SetCookie(writer, &http.Cookie{ // #nosec G124 -- Secure is set exactly when the request was
		Name:     tokenCookie,
		Value:    s.token,
		Path:     "/",
		HttpOnly: true,
		Secure:   request.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(writer, request, "/", http.StatusSeeOther)
}

func (s *Server) overview(writer http.ResponseWriter, _ *http.Request) {
	status, err := s.loaded.Status()
	if err != nil {
		s.fail(writer, err)
		return
	}
	s.render(writer, "overview.html", map[string]any{
		"Title":  "Overview",
		"Status": status,
	})
}

func (s *Server) audit(writer http.ResponseWriter, request *http.Request) {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	cursor, _ := strconv.ParseInt(request.URL.Query().Get("cursor"), 10, 64)
	trail, err := s.loaded.Audit(state.AuditFilter{Limit: limit, Cursor: cursor})
	if err != nil {
		s.fail(writer, err)
		return
	}
	s.render(writer, "audit.html", map[string]any{
		"Title": "Audit trail",
		"Audit": trail,
	})
}

func (s *Server) policy(writer http.ResponseWriter, _ *http.Request) {
	status, err := s.loaded.Status()
	if err != nil {
		s.fail(writer, err)
		return
	}
	s.render(writer, "policy.html", map[string]any{
		"Title":  "Policy",
		"Status": status,
	})
}

func (s *Server) statusAPI(writer http.ResponseWriter, _ *http.Request) {
	status, err := s.loaded.Status()
	if err != nil {
		s.failAPI(writer, "status", err)
		return
	}
	s.writeEnvelope(writer, response.Succeed("status", time.Now().UTC(), status))
}

func (s *Server) auditAPI(writer http.ResponseWriter, request *http.Request) {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	trail, err := s.loaded.Audit(state.AuditFilter{Limit: limit})
	if err != nil {
		s.failAPI(writer, "audit", err)
		return
	}
	s.writeEnvelope(writer, response.Succeed("audit", time.Now().UTC(), trail))
}

func (s *Server) writeEnvelope(writer http.ResponseWriter, envelope response.Envelope) {
	writer.Header().Set("Content-Type", "application/json")
	if !envelope.OK {
		writer.WriteHeader(http.StatusInternalServerError)
	}
	_ = response.Write(writer, envelope)
}

func (s *Server) failAPI(writer http.ResponseWriter, command string, err error) {
	s.writeEnvelope(writer, response.Fail(command, time.Now().UTC(), response.CodeInternal, err.Error()))
}

func (s *Server) fail(writer http.ResponseWriter, err error) {
	writer.WriteHeader(http.StatusInternalServerError)
	s.render(writer, "error.html", map[string]any{"Title": "Something went wrong", "Error": err.Error()})
}

func (s *Server) render(writer http.ResponseWriter, page string, data map[string]any) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(writer, page, data); err != nil {
		_, _ = writer.Write([]byte("<p>the page could not be rendered</p>"))
	}
}

func bindsToLoopback(address string) (bool, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false, fmt.Errorf("ui.address must be host:port: %w", err)
	}
	if host == "" {
		return false, nil
	}
	if host == "localhost" {
		return true, nil
	}
	parsed := net.ParseIP(host)
	if parsed == nil {
		return false, nil
	}
	return parsed.IsLoopback(), nil
}

func shortDigest(value any) string {
	var digest string
	switch typed := value.(type) {
	case string:
		digest = typed
	case *string:
		if typed == nil {
			return "\u2014"
		}
		digest = *typed
	default:
		return "\u2014"
	}
	trimmed := strings.TrimPrefix(digest, "sha256:")
	if trimmed == "" {
		return "\u2014"
	}
	if len(trimmed) > 12 {
		return trimmed[:12]
	}
	return trimmed
}
