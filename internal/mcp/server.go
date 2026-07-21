// Package mcp implements the Model Context Protocol server foundation for
// Kaptanto: streamable-HTTP listener, API-key auth, per-key ACL/redaction,
// and NDJSON audit logging.
//
// Invariants:
//
//   - MCP-01 — ACL everywhere: every tool result is filtered through the
//     calling key's ACL + redaction; there is no unfiltered code path.
//   - MCP-03 — Audit completeness: every tool call (including failures and
//     ACL denials) writes one audit line; lines never contain row data or
//     key material. Audit write failure → slog.Error + the call proceeds.
//   - MCP-04 — Bounded impact: MCP disabled ⇒ zero pipeline cost.
//
// Schema tools (list_tables, get_table_schema) live in tools.go; subscription
// tools land in follow-ups.
//
// SDK: github.com/modelcontextprotocol/go-sdk (v1.6.1) — streamable-HTTP via
// mcp.NewStreamableHTTPHandler. Pure Go (no CGO). As of v1.6.1 there is no
// public ServerOptions session-close callback; follow-ups that need MCP-02
// cleanup can track sessions via InitializedHandler + ServerSession.Wait, or
// EventStore.SessionClosed.
package mcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/olucasandrade/kaptanto/internal/auth"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
)

// envRefRegex validates that a raw key value is exactly ${VAR}.
var envRefRegex = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// Defaults applied when fields are zero / unset.
const (
	DefaultPort             = 7655
	DefaultMaxSubscriptions = 16
	DefaultRingSize         = 1024
	DefaultAuditFileName    = "mcp-audit.ndjson"
)

// ResolvedKey is one authenticated API key with compiled ACL (secret held only
// for constant-time compare; never logged or audited).
type ResolvedKey struct {
	Name   string
	secret string
	ACL    *ACL
}

// Options configures the MCP HTTP listener.
type Options struct {
	Config           config.MCPConfig
	DataDir          string
	TLS              *tls.Config // ServerTLS reuse; nil = plaintext
	Metrics          *observability.KaptantoMetrics
	Logger           *slog.Logger
	Auditor          *Auditor     // optional override (tests); nil → built from config
	Listener         net.Listener // optional; when set, Port is ignored (tests)
	SourceType       string       // "postgres" | "mongodb"; default postgres
	ConfiguredTables []string     // from config.Tables keys
	Schema           SchemaProvider
}

// Server is the MCP streamable-HTTP listener lifecycle.
type Server struct {
	opts             Options
	keys             []*ResolvedKey
	auditor          *Auditor
	ownAudit         bool
	metrics          *observability.KaptantoMetrics
	log              *slog.Logger
	sdk              *sdk.Server
	httpSrv          *http.Server
	ln               net.Listener
	sourceType       string
	configuredTables []string

	schemaMu sync.RWMutex
	schema   SchemaProvider

	mu     sync.Mutex
	closed bool
}

// ResolveConfig validates MCP config, expands ${VAR} secrets, and applies
// defaults. Returns an error when enabled with zero keys or invalid refs.
func ResolveConfig(cfg config.MCPConfig, dataDir string) (config.MCPConfig, []*ResolvedKey, error) {
	out := cfg
	if out.Port == 0 {
		out.Port = DefaultPort
	}
	if out.MaxSubscriptions == 0 {
		out.MaxSubscriptions = DefaultMaxSubscriptions
	}
	if out.RingSize == 0 {
		out.RingSize = DefaultRingSize
	}
	if out.Audit.Enabled == nil {
		t := true
		out.Audit.Enabled = &t
	}
	if out.Audit.Path == "" {
		out.Audit.Path = filepath.Join(dataDir, DefaultAuditFileName)
	}

	if !out.Enabled {
		return out, nil, nil
	}
	if len(out.APIKeys) == 0 {
		return out, nil, fmt.Errorf("mcp: enabled but no api-keys configured")
	}

	seen := make(map[string]struct{}, len(out.APIKeys))
	resolved := make([]*ResolvedKey, 0, len(out.APIKeys))
	for i, k := range out.APIKeys {
		if strings.TrimSpace(k.Name) == "" {
			return out, nil, fmt.Errorf("mcp: api-keys[%d]: name is required", i)
		}
		if _, dup := seen[k.Name]; dup {
			return out, nil, fmt.Errorf("mcp: duplicate api-key name %q", k.Name)
		}
		seen[k.Name] = struct{}{}

		secret, err := resolveStrictEnvRef(k.Key, fmt.Sprintf("mcp: api-keys[%d] (%s) key", i, k.Name))
		if err != nil {
			return out, nil, err
		}
		acl, err := CompileACL(k)
		if err != nil {
			return out, nil, err
		}
		resolved = append(resolved, &ResolvedKey{Name: k.Name, secret: secret, ACL: acl})
		out.APIKeys[i].Key = "${REDACTED}" // never retain resolved secret in config copy
	}
	return out, resolved, nil
}

func resolveStrictEnvRef(raw, field string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%s: must be an environment variable reference like ${VAR}", field)
	}
	if !envRefRegex.MatchString(trimmed) {
		return "", fmt.Errorf("%s: must be an environment variable reference like ${VAR}", field)
	}
	varName := trimmed[2 : len(trimmed)-1]
	val, ok := os.LookupEnv(varName)
	if !ok || val == "" {
		return "", fmt.Errorf("%s: references ${%s} which is unset", field, varName)
	}
	return val, nil
}

// New builds an MCP server from options. When cfg.Enabled is false, New
// returns (nil, nil) so callers pay zero cost (MCP-04).
func New(opts Options) (*Server, error) {
	// Copy API keys so ResolveConfig can scrub secrets without mutating caller state.
	if n := len(opts.Config.APIKeys); n > 0 {
		cp := make([]config.MCPAPIKey, n)
		copy(cp, opts.Config.APIKeys)
		opts.Config.APIKeys = cp
	}
	cfg, keys, err := ResolveConfig(opts.Config, opts.DataDir)
	if err != nil {
		return nil, err
	}
	opts.Config = cfg
	if !cfg.Enabled {
		return nil, nil
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	var auditor *Auditor
	ownAudit := false
	if opts.Auditor != nil {
		auditor = opts.Auditor
	} else if cfg.Audit.Enabled == nil || *cfg.Audit.Enabled {
		auditor, err = NewAuditor(cfg.Audit.Path, log)
		if err != nil {
			return nil, err
		}
		ownAudit = true
	}

	impl := &sdk.Implementation{Name: "kaptanto", Version: "0.0.0"}
	mcpServer := sdk.NewServer(impl, &sdk.ServerOptions{
		Logger: log,
	})

	src := opts.SourceType
	if src == "" {
		src = SourcePostgres
	}
	configured := append([]string(nil), opts.ConfiguredTables...)

	s := &Server{
		opts:             opts,
		keys:             keys,
		auditor:          auditor,
		ownAudit:         ownAudit,
		metrics:          opts.Metrics,
		log:              log,
		sdk:              mcpServer,
		sourceType:       src,
		configuredTables: configured,
		schema:           opts.Schema,
	}
	s.registerSchemaTools()
	return s, nil
}

// SetSchemaProvider installs (or replaces) the live schema provider. Safe to
// call after New once the Postgres parser is available.
func (s *Server) SetSchemaProvider(p SchemaProvider) {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	s.schema = p
}

// SDK exposes the underlying MCP SDK server so follow-ups can register tools.
func (s *Server) SDK() *sdk.Server { return s.sdk }

// Keys returns resolved API keys (for follow-up tool wiring / tests).
func (s *Server) Keys() []*ResolvedKey { return s.keys }

// Auditor returns the audit writer (may be nil when audit disabled).
func (s *Server) Auditor() *Auditor { return s.auditor }

// LookupKey constant-time compares provided against every configured secret.
// Returns nil when no key matches. Always scans the full key list.
func (s *Server) LookupKey(provided string) *ResolvedKey {
	return lookupKey(s.keys, provided)
}

// lookupKey is the testable constant-time multi-key compare.
func lookupKey(keys []*ResolvedKey, provided string) *ResolvedKey {
	var match *ResolvedKey
	for _, k := range keys {
		if auth.CheckBearer(provided, k.secret) {
			match = k
		}
	}
	return match
}

// RecordToolCall writes an audit line and increments mcp_tool_calls_total.
// Safe when auditor or metrics are nil.
func (s *Server) RecordToolCall(keyName, tool string, paramsSummary, tables []string, outcome string, duration time.Duration) {
	if s.metrics != nil {
		s.metrics.MCPToolCallsTotal.WithLabelValues(tool, outcome).Inc()
	}
	if s.auditor == nil {
		return
	}
	s.auditor.Record(AuditRecord{
		Key:           keyName,
		Tool:          tool,
		ParamsSummary: paramsSummary,
		Tables:        tables,
		Outcome:       outcome,
		DurationMS:    duration.Milliseconds(),
	})
}

// Run listens until ctx is cancelled. It reuses opts.TLS when set.
func (s *Server) Run(ctx context.Context) error {
	handler := sdk.NewStreamableHTTPHandler(func(_ *http.Request) *sdk.Server {
		return s.sdk
	}, nil)
	mux := http.NewServeMux()
	mux.Handle("/", s.authMiddleware(handler))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         s.opts.TLS,
	}
	s.httpSrv = srv

	ln := s.opts.Listener
	var err error
	if ln == nil {
		addr := fmt.Sprintf(":%d", s.opts.Config.Port)
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("mcp: listen %s: %w", addr, err)
		}
	}
	s.ln = ln
	s.log.Info("mcp: listening", "addr", ln.Addr().String(), "tls", s.opts.TLS != nil)

	errCh := make(chan error, 1)
	go func() {
		var serveErr error
		if s.opts.TLS != nil {
			serveErr = srv.ServeTLS(ln, "", "")
		} else {
			serveErr = srv.Serve(ln)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}

// Close releases the auditor when owned by this server.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.ownAudit && s.auditor != nil {
		return s.auditor.Close()
	}
	return nil
}

// Addr returns the listener address once Run has started, or nil.
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

type ctxKey int

const keyPrincipal ctxKey = 1

// PrincipalFromContext returns the authenticated API key, if any.
func PrincipalFromContext(ctx context.Context) *ResolvedKey {
	v, _ := ctx.Value(keyPrincipal).(*ResolvedKey)
	return v
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := auth.ExtractBearerHTTP(r)
		k := s.LookupKey(token)
		if k == nil {
			s.RecordToolCall("unknown", "auth", nil, nil, OutcomeDenied, 0)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), keyPrincipal, k)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
