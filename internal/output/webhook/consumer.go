// Package webhooksink provides WebhookSinkConsumer, a router.Consumer
// implementation that delivers CDC events as HTTP requests to a configured
// webhook endpoint.
//
// Key design decisions:
//
//   - CHK-01 (Durability): Deliver only buffers a pendingReq in memory and
//     returns immediately; the actual HTTP call happens in FlushBatch, called
//     by the Router once per ReadPartition batch. The Router does not persist
//     this consumer's cursor at Deliver time — it records a provisional advance
//     and promotes it to the durable cursor only after FlushBatch returns nil.
//     A FlushBatch failure discards the provisional advance, so the Router
//     re-reads and re-delivers the same batch (after a backoff) instead of
//     losing it — see router.BatchFlusher and internal/router/router.go.
//
//   - DLV-02 (Per-key ordering): FlushBatch sends requests sequentially in
//     buffer order for a single partition. A URL change starts a new chunk;
//     events are never reordered across URLs. First error aborts the flush so
//     ordering holds across re-delivery.
//
//   - DLV-04 (Idempotency header): Single-event mode stamps every request with
//     X-Kaptanto-Idempotency-Key set to entry.Event.IdempotencyKey (including
//     when a transform reshaped the body). Batch mode always stamps
//     X-Kaptanto-Idempotency-Keys with the comma-joined keys in chunk order.
//     Downstream receivers deduplicate re-delivered batches by these keys.
//
//   - DLV-03 (No internal retry): On a transform/encoding error Deliver returns
//     a non-nil error immediately; on an HTTP error FlushBatch returns a
//     non-nil error. Neither retries internally — retry (with NextDelay
//     backoff for FlushBatch failures) is the Router's responsibility. The
//     only time knob is the per-request timeout (default 30s).
//
//     Batch poison isolation (max-events > 1): when an ARRAY chunk receives a
//     3xx/other-4xx response, FlushBatch re-sends that chunk one-request-per-
//     event in order within the same call so the poison seq can be identified.
//     This is error isolation, NOT retry — after a transient individual failure
//     during isolation, FlushBatch aborts immediately and must NEVER continue
//     re-sending the remaining events of that chunk.
//
//   - WHK-01 (Response classification): 2xx advances the cursor. 408, 429, 5xx,
//     network errors, timeouts, and context deadlines are transient (plain
//     error). 3xx and other 4xx are poison (&router.PermanentFlushError).
//
//   - WHK-02 (No redirects): http.Client.CheckRedirect returns
//     http.ErrUseLastResponse so 3xx responses are never followed and hit the
//     poison classification path.
//
//   - WHK-03 (Signature over exact body bytes): When signing.secret is set,
//     X-Kaptanto-Signature is HMAC-SHA256 over t + "." + the exact request
//     body bytes on the wire (post-transform, after batching/array join).
//
//   - TRF-01 / TRF-02: payload-template and transform.* compile at startup via
//     transform.Compile; drop results are not buffered (cursor advances with
//     the next successful flush).
//
//   - CGO-free for the sink itself (gojq lives in internal/transform);
//     CGO_ENABLED=0 is safe.
package webhooksink

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/olucasandrade/kaptanto/internal/transform"
)

// Compile-time assertion: WebhookSinkConsumer must implement router.Consumer.
var _ router.Consumer = (*WebhookSinkConsumer)(nil)

// Compile-time assertion: WebhookSinkConsumer implements router.BatchFlusher.
var _ router.BatchFlusher = (*WebhookSinkConsumer)(nil)

// WebhookSinkConsumer is a router.Consumer that delivers CDC events as HTTP
// requests to a configured webhook endpoint.
//
// When used with the Router's BatchFlusher interface, Deliver enqueues
// pendingReq values into a per-partition buffer and FlushBatch issues the
// HTTP requests. CHK-01 is preserved: the router only advances the cursor
// after FlushBatch returns nil.
//
// Use NewWebhookSinkConsumer to construct — do not create directly.
type WebhookSinkConsumer struct {
	id       string
	client   *http.Client       // shared; Transport carries TLS from buildTLSConfig
	url      string             // static URL (env-expanded); used when urlT is nil
	urlT     *template.Template // nil when url-template unset
	engine   transform.Engine   // nil when no transform / payload-template
	method   string
	headers  map[string]string // already env-expanded
	authHdr  string            // precomputed "Bearer …" or "Basic …"; "" if none
	secret   []byte            // signing secret; nil disables signing
	batchMax int
	timeout  time.Duration
	mu       sync.Mutex
	pending  map[uint32][]pendingReq // keyed by entry.PartitionID
	m        *observability.KaptantoMetrics
}

// pendingReq holds one buffered event ready for FlushBatch.
type pendingReq struct {
	url            string // resolved at Deliver time (url-template is per-event)
	body           []byte
	idempotencyKey string
	seq            uint64
}

// NewWebhookSinkConsumer creates a WebhookSinkConsumer from cfg.
//
// It applies ${VAR} env expansion to secret-bearing fields, validates the
// configuration (fail-fast), and builds a shared http.Client. The caller is
// responsible for calling Close() when done.
func NewWebhookSinkConsumer(id string, cfg config.WebhookSinkConfig) (*WebhookSinkConsumer, error) {
	cfg = expandWebhookConfig(cfg)

	norm, err := validateWebhookConfig(cfg)
	if err != nil {
		return nil, err
	}

	tlsCfg, err := buildTLSConfig(cfg.TLS)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	headers := cfg.Headers
	if headers == nil {
		headers = map[string]string{}
	}

	return &WebhookSinkConsumer{
		id:       id,
		client:   client,
		url:      cfg.URL,
		urlT:     norm.urlT,
		engine:   norm.engine,
		method:   norm.method,
		headers:  headers,
		authHdr:  norm.authHdr,
		secret:   norm.secret,
		batchMax: norm.batchMax,
		timeout:  norm.timeout,
		pending:  make(map[uint32][]pendingReq),
	}, nil
}

// webhookNorm holds validated constructor outputs.
type webhookNorm struct {
	method   string
	authHdr  string
	secret   []byte
	batchMax int
	timeout  time.Duration
	urlT     *template.Template
	engine   transform.Engine
}

// expandWebhookConfig applies ${VAR} env expansion to secret-bearing fields.
func expandWebhookConfig(cfg config.WebhookSinkConfig) config.WebhookSinkConfig {
	expand := func(s string) string {
		return os.Expand(s, os.Getenv)
	}
	cfg.URL = expand(cfg.URL)
	cfg.URLTemplate = expand(cfg.URLTemplate)
	cfg.Auth.BearerToken = expand(cfg.Auth.BearerToken)
	cfg.Auth.Basic.Password = expand(cfg.Auth.Basic.Password)
	cfg.Signing.Secret = expand(cfg.Signing.Secret)
	if cfg.Headers != nil {
		expanded := make(map[string]string, len(cfg.Headers))
		for k, v := range cfg.Headers {
			expanded[k] = expand(v)
		}
		cfg.Headers = expanded
	}
	return cfg
}

// validateWebhookConfig enforces startup rules 1–13 and returns normalized fields.
func validateWebhookConfig(cfg config.WebhookSinkConfig) (webhookNorm, error) {
	var zero webhookNorm

	// 1. url or url-template required.
	if cfg.URL == "" && cfg.URLTemplate == "" {
		return zero, fmt.Errorf("webhook sink: url or url-template is required")
	}

	// 2. Method allowlist; empty → POST.
	method := cfg.Method
	if method == "" {
		method = http.MethodPost
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return zero, fmt.Errorf("webhook sink: method %q not allowed — must be one of POST, PUT, PATCH", cfg.Method)
	}

	// 3. Bearer XOR basic.
	hasBearer := cfg.Auth.BearerToken != ""
	hasBasic := cfg.Auth.Basic.Username != ""
	if hasBearer && hasBasic {
		return zero, fmt.Errorf("webhook sink: auth.bearer-token and auth.basic are mutually exclusive")
	}

	// 8. batch.max-events < 0 → error.
	if cfg.Batch.MaxEvents < 0 {
		return zero, fmt.Errorf("webhook sink: batch.max-events must be >= 0")
	}

	// 4. payload-template requires batch.max-events == 1 (0/1 both mean single-event).
	if cfg.PayloadTemplate != "" && cfg.Batch.MaxEvents > 1 {
		return zero, fmt.Errorf("webhook sink: payload-template requires batch.max-events=1")
	}

	// 5. Authorization header conflict with auth.*.
	if hasBearer || hasBasic {
		for k := range cfg.Headers {
			if strings.EqualFold(k, "Authorization") {
				return zero, fmt.Errorf("webhook sink: headers must not set Authorization when auth is configured")
			}
		}
	}

	// 6. Timeout: empty → 30s; unparsable or ≤0 → error.
	timeout := 30 * time.Second
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return zero, fmt.Errorf("webhook sink: timeout: %w", err)
		}
		if d <= 0 {
			return zero, fmt.Errorf("webhook sink: timeout must be > 0")
		}
		timeout = d
	}

	// 7. Parse url-template at construction.
	var urlT *template.Template
	if cfg.URLTemplate != "" {
		t, err := template.New("url").Parse(cfg.URLTemplate)
		if err != nil {
			return zero, fmt.Errorf("webhook sink: url-template parse error: %w", err)
		}
		urlT = t
	}

	engine, err := compileWebhookEngine(cfg)
	if err != nil {
		return zero, err
	}

	var authHdr string
	switch {
	case hasBearer:
		authHdr = "Bearer " + cfg.Auth.BearerToken
	case hasBasic:
		cred := base64.StdEncoding.EncodeToString(
			[]byte(cfg.Auth.Basic.Username + ":" + cfg.Auth.Basic.Password),
		)
		authHdr = "Basic " + cred
	}

	batchMax := cfg.Batch.MaxEvents
	if batchMax == 0 {
		batchMax = 1
	}

	var secret []byte
	if cfg.Signing.Secret != "" {
		secret = []byte(cfg.Signing.Secret)
	}

	return webhookNorm{
		method:   method,
		authHdr:  authHdr,
		secret:   secret,
		batchMax: batchMax,
		timeout:  timeout,
		urlT:     urlT,
		engine:   engine,
	}, nil
}

// compileWebhookEngine enforces validations 9–13 and compiles the transform
// engine (payload-template sugar or transform.*). Returns nil engine when unset.
func compileWebhookEngine(cfg config.WebhookSinkConfig) (transform.Engine, error) {
	lang := cfg.Transform.Language
	expr := cfg.Transform.Expression
	hasPayload := cfg.PayloadTemplate != ""
	hasTransformField := lang != "" || expr != ""

	// 9. payload-template AND transform.* both set → error.
	if hasPayload && hasTransformField {
		return nil, fmt.Errorf("webhook sink: payload-template is shorthand for transform; set only one")
	}

	// 11. language allowlist (empty is OK when both language and expression empty).
	if lang != "" && lang != transform.LangJQ && lang != transform.LangGoTemplate {
		return nil, fmt.Errorf("webhook sink: transform.language %q not allowed — must be one of %q, %q",
			lang, transform.LangJQ, transform.LangGoTemplate)
	}

	// 12. empty language xor expression → error.
	if (lang == "") != (expr == "") {
		return nil, fmt.Errorf("webhook sink: transform.language and transform.expression must both be set or both be empty")
	}

	// 10. go-template language + batch.max-events > 1 → error (jq exempt).
	if lang == transform.LangGoTemplate && cfg.Batch.MaxEvents > 1 {
		return nil, fmt.Errorf("webhook sink: transform language %q requires batch.max-events=1", transform.LangGoTemplate)
	}

	// 13. Compile transform / payload-template (TRF-01).
	switch {
	case hasPayload:
		eng, err := transform.Compile(transform.LangGoTemplate, cfg.PayloadTemplate)
		if err != nil {
			return nil, fmt.Errorf("webhook sink: payload-template: %w", err)
		}
		return eng, nil
	case lang != "":
		eng, err := transform.Compile(lang, expr)
		if err != nil {
			return nil, fmt.Errorf("webhook sink: transform: %w", err)
		}
		return eng, nil
	}
	return nil, nil
}

// ID returns the stable, unique identifier for this consumer instance.
func (c *WebhookSinkConsumer) ID() string {
	return c.id
}

// SetMetrics injects a KaptantoMetrics reference so the consumer reports
// QueuePublishTotal, QueuePublishErrors, QueuePublishLatency, and
// TransformDroppedTotal.
// Call after construction, before Deliver.
func (c *WebhookSinkConsumer) SetMetrics(m *observability.KaptantoMetrics) {
	c.m = m
}

// Deliver enqueues entry into the consumer's pending buffer for batch
// publishing. No network I/O happens here; the actual HTTP call is performed
// by FlushBatch.
//
// On transform/encoding error Deliver returns a non-nil error immediately; the
// RetryScheduler will block the key (DLV-03). A transform drop returns nil
// without buffering (TRF-02).
func (c *WebhookSinkConsumer) Deliver(ctx context.Context, entry eventlog.LogEntry) error {
	_ = ctx

	urlStr, err := c.resolveURL(entry.Event)
	if err != nil {
		return err
	}

	body, drop, err := c.buildBody(entry)
	if err != nil {
		return err
	}
	if drop {
		if c.m != nil {
			c.m.TransformDroppedTotal.WithLabelValues(c.id).Inc()
		}
		return nil
	}

	c.mu.Lock()
	c.pending[entry.PartitionID] = append(c.pending[entry.PartitionID], pendingReq{
		url:            urlStr,
		body:           body,
		idempotencyKey: entry.Event.IdempotencyKey,
		seq:            entry.Seq,
	})
	c.mu.Unlock()
	return nil
}

// resolveURL returns the request URL for ev: url-template if set, else static url.
func (c *WebhookSinkConsumer) resolveURL(ev *event.ChangeEvent) (string, error) {
	if c.urlT != nil {
		var buf bytes.Buffer
		if err := c.urlT.Execute(&buf, ev); err != nil {
			return "", fmt.Errorf("webhook sink: url-template execution: %w", err)
		}
		u := strings.TrimSpace(buf.String())
		if u == "" {
			return "", fmt.Errorf("webhook sink: url-template rendered to an empty string — check url-template config")
		}
		return u, nil
	}
	u := strings.TrimSpace(c.url)
	if u == "" {
		return "", fmt.Errorf("webhook sink: url is empty")
	}
	return u, nil
}

// buildBody returns the HTTP body for entry.
// When engine is set, raw is transformed; drop=true means do not buffer (TRF-02).
func (c *WebhookSinkConsumer) buildBody(entry eventlog.LogEntry) ([]byte, bool, error) {
	var raw []byte
	if len(entry.Raw) > 0 {
		raw = entry.Raw
	} else {
		data, err := json.Marshal(entry.Event)
		if err != nil {
			return nil, false, fmt.Errorf("webhook sink: marshal event: %w", err)
		}
		raw = data
	}
	if c.engine == nil {
		return raw, false, nil
	}
	out, drop, err := c.engine.Apply(raw, entry.Event)
	if err != nil {
		return nil, false, err
	}
	if drop {
		return nil, true, nil
	}
	return out, false, nil
}

// httpReq is one outbound HTTP request produced by grouping pendingReq values.
type httpReq struct {
	url             string
	body            []byte
	idempotencyKey  string // single-event mode
	idempotencyKeys string // batch mode (comma-joined)
	seq             uint64 // single-event mode
	single          bool
	items           []pendingReq // batch mode: originals for poison isolation
}

// FlushBatch sends all buffered requests for partitionID. It pops the pending
// buffer before any network I/O (Kafka pattern). On error the router re-reads
// and re-Delivers; receivers dedup via the idempotency key (DLV-04).
func (c *WebhookSinkConsumer) FlushBatch(ctx context.Context, partitionID uint32) error {
	c.mu.Lock()
	if len(c.pending[partitionID]) == 0 {
		c.mu.Unlock()
		return nil
	}
	batch := c.pending[partitionID]
	delete(c.pending, partitionID)
	c.mu.Unlock()

	reqs := groupRequests(batch, c.batchMax)

	start := time.Now()
	var successCount, errorCount int
	var firstErr error

	for _, req := range reqs {
		if err := c.sendGrouped(ctx, req); err != nil {
			errorCount++
			firstErr = err
			break
		}
		successCount++
	}

	if c.m != nil {
		c.m.QueuePublishLatency.WithLabelValues("webhook").Observe(time.Since(start).Seconds())
		if successCount > 0 {
			c.m.QueuePublishTotal.WithLabelValues("webhook").Add(float64(successCount))
		}
		if errorCount > 0 {
			c.m.QueuePublishErrors.WithLabelValues("webhook").Add(float64(errorCount))
		}
	}
	return firstErr
}

// sendGrouped sends one grouped request. For batch ARRAY responses that are
// poison (3xx/other 4xx), it isolates by re-sending one-per-event (DLV-03).
func (c *WebhookSinkConsumer) sendGrouped(ctx context.Context, req httpReq) error {
	status, snippet, err := c.doRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("webhook sink: %s %s: %w", c.method, req.url, err)
	}
	if status >= 200 && status < 300 {
		return nil
	}
	cause := fmt.Errorf("webhook sink: %s %s returned %d: %.256s", c.method, req.url, status, snippet)
	if isTransientStatus(status) {
		return cause
	}
	// Poison: 3xx or other 4xx.
	if req.single || len(req.items) <= 1 {
		seq := req.seq
		if !req.single && len(req.items) == 1 {
			seq = req.items[0].seq
		}
		return &router.PermanentFlushError{Seq: seq, Cause: cause}
	}
	// Batch poison isolation — NOT retry (DLV-03).
	return c.isolatePoisonChunk(ctx, req.items)
}

// isolatePoisonChunk re-sends each event in items as an individual request, in
// order. Individual 2xx continues; transient aborts with a plain error (no
// further resend); individual poison returns PermanentFlushError for that seq.
func (c *WebhookSinkConsumer) isolatePoisonChunk(ctx context.Context, items []pendingReq) error {
	for _, item := range items {
		single := httpReq{
			url:            item.url,
			body:           item.body,
			idempotencyKey: item.idempotencyKey,
			seq:            item.seq,
			single:         true,
		}
		status, snippet, err := c.doRequest(ctx, single)
		if err != nil {
			// Transient network/timeout — abort isolation; do not resend further.
			return fmt.Errorf("webhook sink: %s %s: %w", c.method, item.url, err)
		}
		if status >= 200 && status < 300 {
			continue
		}
		cause := fmt.Errorf("webhook sink: %s %s returned %d: %.256s", c.method, item.url, status, snippet)
		if isTransientStatus(status) {
			return cause
		}
		return &router.PermanentFlushError{Seq: item.seq, Cause: cause}
	}
	return nil
}

// isTransientStatus reports whether an HTTP status is a transient failure
// (408, 429, 5xx). All other non-2xx codes are poison.
func isTransientStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

// groupRequests chunks pendingReq values into outbound HTTP requests.
// batchMax == 1 → one request per entry.
// batchMax > 1 → consecutive same-URL entries into JSON arrays of ≤ batchMax;
// a URL change starts a new chunk even if the current one is not full.
func groupRequests(batch []pendingReq, batchMax int) []httpReq {
	if batchMax <= 1 {
		out := make([]httpReq, 0, len(batch))
		for _, p := range batch {
			out = append(out, httpReq{
				url:            p.url,
				body:           p.body,
				idempotencyKey: p.idempotencyKey,
				seq:            p.seq,
				single:         true,
			})
		}
		return out
	}

	var out []httpReq
	i := 0
	for i < len(batch) {
		url := batch[i].url
		var items []pendingReq
		for i < len(batch) && batch[i].url == url && len(items) < batchMax {
			items = append(items, batch[i])
			i++
		}
		var buf bytes.Buffer
		buf.WriteByte('[')
		keys := make([]string, 0, len(items))
		for j, it := range items {
			if j > 0 {
				buf.WriteByte(',')
			}
			buf.Write(it.body)
			keys = append(keys, it.idempotencyKey)
		}
		buf.WriteByte(']')
		out = append(out, httpReq{
			url:             url,
			body:            buf.Bytes(),
			idempotencyKeys: strings.Join(keys, ","),
			single:          false,
			items:           items,
		})
	}
	return out
}

// doRequest issues a single HTTP request with timeout, headers, and optional HMAC.
// It returns the status code and a body snippet on HTTP responses; err is set
// only for transport / request-construction failures.
func (c *WebhookSinkConsumer) doRequest(ctx context.Context, req httpReq) (status int, snippet []byte, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, c.method, req.url, bytes.NewReader(req.body))
	if err != nil {
		return 0, nil, fmt.Errorf("create request: %w", err)
	}

	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "kaptanto")
	if req.single {
		httpReq.Header.Set("X-Kaptanto-Idempotency-Key", req.idempotencyKey)
	} else {
		httpReq.Header.Set("X-Kaptanto-Idempotency-Keys", req.idempotencyKeys)
	}
	if c.authHdr != "" {
		httpReq.Header.Set("Authorization", c.authHdr)
	}
	if len(c.secret) > 0 {
		t := time.Now().Unix()
		httpReq.Header.Set("X-Kaptanto-Signature", signature(c.secret, t, req.body))
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	snippet, _ = io.ReadAll(io.LimitReader(resp.Body, 1024))
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, snippet, nil
}

// Ping verifies the webhook host is reachable via a TCP (or TLS) dial with a
// 5-second timeout. No HTTP request is sent — receivers may have side effects.
//
// Host is taken from the static URL, or from url-template rendered against a
// zero ChangeEvent. If that render fails, Ping returns nil (template needs
// real events).
func (c *WebhookSinkConsumer) Ping() error {
	raw := c.url
	if c.urlT != nil {
		var buf bytes.Buffer
		if err := c.urlT.Execute(&buf, &event.ChangeEvent{}); err != nil {
			return nil
		}
		raw = strings.TrimSpace(buf.String())
		if raw == "" {
			return nil
		}
	}
	if raw == "" {
		return nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("webhook sink: ping: parse url: %w", err)
	}
	host := u.Host
	if host == "" {
		return fmt.Errorf("webhook sink: ping: url has no host")
	}
	if u.Port() == "" {
		switch u.Scheme {
		case "https":
			host = net.JoinHostPort(host, "443")
		case "http":
			host = net.JoinHostPort(host, "80")
		}
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if u.Scheme == "https" {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if t, ok := c.client.Transport.(*http.Transport); ok && t.TLSClientConfig != nil {
			tlsCfg = t.TLSClientConfig.Clone()
			if tlsCfg.MinVersion == 0 {
				tlsCfg.MinVersion = tls.VersionTLS12
			}
			if tlsCfg.ServerName == "" {
				h, _, splitErr := net.SplitHostPort(host)
				if splitErr == nil {
					tlsCfg.ServerName = h
				} else {
					tlsCfg.ServerName = host
				}
			}
		}
		conn, err := tls.DialWithDialer(dialer, "tcp", host, tlsCfg)
		if err != nil {
			return fmt.Errorf("webhook sink: ping: %w", err)
		}
		_ = conn.Close()
		return nil
	}

	conn, err := dialer.Dial("tcp", host)
	if err != nil {
		return fmt.Errorf("webhook sink: ping: %w", err)
	}
	_ = conn.Close()
	return nil
}

// Close closes idle HTTP connections. It is safe to call Close multiple times.
func (c *WebhookSinkConsumer) Close() {
	c.client.CloseIdleConnections()
}

// buildTLSConfig constructs a *tls.Config from cfg:
//   - MinVersion is always TLS 1.2.
//   - If CAFile is set, loads the CA certificate pool from the file.
//   - If CertFile and KeyFile are both set, loads the client key pair for mTLS.
func buildTLSConfig(tlsCfg config.TLSConfig) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if tlsCfg.CAFile != "" {
		pem, err := os.ReadFile(tlsCfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("webhook sink: read ca-file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("webhook sink: no valid certs in ca-file %q", tlsCfg.CAFile)
		}
		cfg.RootCAs = pool
	}

	if tlsCfg.CertFile != "" && tlsCfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("webhook sink: load client cert: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}
