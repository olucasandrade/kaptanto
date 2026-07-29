// Package enrich implements the optional fail-open HTTP enrichment stage
// (AIC-01/02) that runs between parse/TOAST-merge and EventLog.Append.
//
// Throughput: enrichment is serial per matching event and bounds ingest to
// roughly 1/timeout events/sec for matching tables in the worst case; scope
// `tables` narrowly. Non-matching events skip the HTTP call at near-zero cost.
//
// Crash during enrichment re-sends the event on restart (checkpoint advances
// only after Append). Enricher endpoints must tolerate duplicate POSTs.
package enrich

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/routing"
)

const (
	// maxAIContextBytes is the AIC-02 bound on ai_context response bodies.
	maxAIContextBytes = 16 * 1024

	defaultTimeout     = 150 * time.Millisecond
	defaultWarnEvery   = time.Second
	defaultOperations0 = "insert"
	defaultOperations1 = "update"
)

// Failure reason labels for enrichment_failures_total{reason}.
const (
	ReasonTimeout   = "timeout"
	ReasonStatus    = "status"
	ReasonError     = "error"
	ReasonInvalid   = "invalid"
	ReasonOversize  = "oversize"
	ReasonNonObject   = "non_object"
	ReasonCircuitOpen = "circuit_open"
)

// envRefRegex validates STRICT ${VAR} secret references (ACT-02).
var envRefRegex = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// Enricher optionally attaches AIContext to matching ChangeEvents via HTTP.
// Enrich never returns an error to the caller — failures fail open (AIC-01).
type Enricher struct {
	enabled   bool
	matcher   *routing.Matcher
	client    *httpEnrichClient
	metrics   *observability.KaptantoMetrics
	warnEvery time.Duration

	warnMu   sync.Mutex
	lastWarn time.Time

	// warnFn overrides slog.Warn in tests; nil uses slog.Default().Warn.
	warnFn func(msg string, args ...any)
}

// Compile validates cfg and returns a ready Enricher. An empty URL or empty
// Tables list yields a disabled Enricher (Enrich is a no-op). Empty Operations
// defaults to insert,update. AuthToken supports ${VAR} expansion.
func Compile(cfg config.EnrichmentConfig, m *observability.KaptantoMetrics) (*Enricher, error) {
	e := &Enricher{
		metrics:   m,
		warnEvery: defaultWarnEvery,
	}

	if strings.TrimSpace(cfg.URL) == "" || len(cfg.Tables) == 0 {
		// Disabled: empty URL, or empty tables (explicit opt-in required).
		return e, nil
	}

	ops := cfg.Operations
	if len(ops) == 0 {
		ops = []string{defaultOperations0, defaultOperations1}
	}

	matcher, err := routing.Compile(routing.MatchConfig{
		Tables:     cfg.Tables,
		Operations: ops,
	})
	if err != nil {
		return nil, fmt.Errorf("enrichment: %w", err)
	}

	timeout := defaultTimeout
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("enrichment: timeout: parse %q: %w", cfg.Timeout, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("enrichment: timeout must be positive, got %s", cfg.Timeout)
		}
		timeout = d
	}

	authToken, err := expandAuthToken(cfg.AuthToken)
	if err != nil {
		return nil, fmt.Errorf("enrichment: auth-token: %w", err)
	}

	policy := newURLPolicy(cfg.AllowHosts, cfg.InsecureAllowPrivate)
	client, err := newHTTPEnrichClient(strings.TrimSpace(cfg.URL), timeout, authToken, policy)
	if err != nil {
		return nil, fmt.Errorf("enrichment: %w", err)
	}

	e.enabled = true
	e.matcher = matcher
	e.client = client
	return e, nil
}

// Enabled reports whether Enrich will attempt HTTP calls for matching events.
func (e *Enricher) Enabled() bool {
	return e != nil && e.enabled
}

// Enrich mutates ev.AIContext on a successful enricher response. Matching is
// checked first; non-matching events return immediately. All HTTP/parse
// failures fail open: ev is left unenriched and a metric/warn is recorded.
func (e *Enricher) Enrich(ctx context.Context, ev *event.ChangeEvent) {
	if e == nil || !e.enabled || ev == nil {
		return
	}

	matched, err := e.matcher.Match(ev)
	if err != nil || !matched {
		// Malformed row or non-match: skip enrichment (fail open / no-op).
		return
	}

	aiCtx, reason, callErr := e.client.post(ctx, ev)
	if reason != "" {
		e.failOpen(reason, callErr)
		return
	}
	if aiCtx != nil {
		ev.AIContext = aiCtx
	}
}

func (e *Enricher) failOpen(reason string, err error) {
	if e.metrics != nil {
		e.metrics.EnrichmentFailuresTotal.WithLabelValues(reason).Inc()
	}
	args := []any{"reason", reason}
	if err != nil {
		args = append(args, "err", err)
	}
	e.warnRateLimited("enrichment failed open; appending unenriched (AIC-01)", args...)
}

func (e *Enricher) warnRateLimited(msg string, args ...any) {
	e.warnMu.Lock()
	defer e.warnMu.Unlock()
	now := time.Now()
	if !e.lastWarn.IsZero() && now.Sub(e.lastWarn) < e.warnEvery {
		return
	}
	e.lastWarn = now
	if e.warnFn != nil {
		e.warnFn(msg, args...)
		return
	}
	slog.Warn(msg, args...)
}

func expandAuthToken(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", nil
	}
	if !envRefRegex.MatchString(trimmed) {
		return "", fmt.Errorf("must be an environment variable reference like ${VAR}")
	}
	varName := trimmed[2 : len(trimmed)-1]
	val, ok := os.LookupEnv(varName)
	if !ok || val == "" {
		return "", fmt.Errorf("references ${%s} which is unset", varName)
	}
	return val, nil
}

// Wrap returns an EventLog that runs Enrich before every Append/AppendBatch.
// ctx is propagated into Enrich so shutdown cancellation can abort in-flight
// HTTP calls. When e is nil or disabled, inner is returned unchanged. Optional
// PartitionNotifier and AppendObservable interfaces are preserved when the
// inner log implements them so router notify and watermark observers keep working.
func Wrap(ctx context.Context, inner eventlog.EventLog, e *Enricher) eventlog.EventLog {
	if inner == nil || e == nil || !e.enabled {
		return inner
	}
	if ctx == nil {
		ctx = context.Background()
	}
	base := &enrichingLog{ctx: ctx, inner: inner, enricher: e}
	_, hasNotify := inner.(eventlog.PartitionNotifier)
	_, hasObs := inner.(eventlog.AppendObservable)
	switch {
	case hasNotify && hasObs:
		return &enrichingFull{enrichingLog: base}
	case hasNotify:
		return &enrichingNotifying{enrichingLog: base}
	case hasObs:
		return &enrichingObservable{enrichingLog: base}
	default:
		return base
	}
}

// enrichingLog implements eventlog.EventLog, enriching before durable write.
type enrichingLog struct {
	ctx      context.Context
	inner    eventlog.EventLog
	enricher *Enricher
}

func (w *enrichingLog) Append(ev *event.ChangeEvent) (uint64, error) {
	w.enricher.Enrich(w.ctx, ev)
	return w.inner.Append(ev)
}

func (w *enrichingLog) AppendBatch(evs []*event.ChangeEvent) ([]uint64, error) {
	for _, ev := range evs {
		w.enricher.Enrich(w.ctx, ev)
	}
	return w.inner.AppendBatch(evs)
}

func (w *enrichingLog) ReadPartition(ctx context.Context, partition uint32, fromSeq uint64, limit int) ([]eventlog.LogEntry, error) {
	return w.inner.ReadPartition(ctx, partition, fromSeq, limit)
}

func (w *enrichingLog) Close() error {
	return w.inner.Close()
}

type enrichingNotifying struct{ *enrichingLog }

func (w *enrichingNotifying) NotifyCh(partition uint32) <-chan struct{} {
	return w.inner.(eventlog.PartitionNotifier).NotifyCh(partition)
}

type enrichingObservable struct{ *enrichingLog }

func (w *enrichingObservable) RegisterObserver(obs eventlog.AppendObserver) (unregister func()) {
	return w.inner.(eventlog.AppendObservable).RegisterObserver(obs)
}

type enrichingFull struct{ *enrichingLog }

func (w *enrichingFull) NotifyCh(partition uint32) <-chan struct{} {
	return w.inner.(eventlog.PartitionNotifier).NotifyCh(partition)
}

func (w *enrichingFull) RegisterObserver(obs eventlog.AppendObserver) (unregister func()) {
	return w.inner.(eventlog.AppendObservable).RegisterObserver(obs)
}

// SetWarnForTest replaces the rate-limited warn sink (tests only).
func SetWarnForTest(e *Enricher, fn func(msg string, args ...any)) {
	if e == nil {
		return
	}
	e.warnFn = fn
}

// SetWarnEveryForTest overrides the warn throttle interval (tests only).
func SetWarnEveryForTest(e *Enricher, d time.Duration) {
	if e == nil {
		return
	}
	e.warnEvery = d
}

// SetCircuitThresholdForTest lowers the consecutive-timeout threshold (tests only).
func SetCircuitThresholdForTest(e *Enricher, n int) {
	if e == nil || e.client == nil || n <= 0 {
		return
	}
	e.client.circuitThreshold = n
}
