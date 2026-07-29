package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
)

const (
	defaultCircuitThreshold = 5
	defaultMaxIdleConns     = 10
	defaultMaxIdlePerHost   = 2
	defaultIdleConnTimeout  = 90 * time.Second
)

// httpEnrichClient POSTs ChangeEvents to the enricher endpoint.
type httpEnrichClient struct {
	url     string
	token   string
	timeout time.Duration
	client  *http.Client

	circuitMu            sync.Mutex
	consecutiveTimeouts  int
	circuitThreshold     int
}

func newHTTPEnrichClient(rawURL string, timeout time.Duration, authToken string, policy *urlPolicy) (*httpEnrichClient, error) {
	if _, err := validateEnricherURL(rawURL, policy); err != nil {
		return nil, err
	}

	transport := &http.Transport{
		MaxIdleConns:        defaultMaxIdleConns,
		MaxIdleConnsPerHost: defaultMaxIdlePerHost,
		IdleConnTimeout:     defaultIdleConnTimeout,
	}
	if policy != nil {
		transport.DialContext = policy.dialContext
	}

	return &httpEnrichClient{
		url:              rawURL,
		token:            authToken,
		timeout:          timeout,
		circuitThreshold: defaultCircuitThreshold,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// post returns (ai_context, "", nil) on success.
// On 204, returns (nil, "", nil) meaning no context.
// On failure, returns (nil, reason, err) for fail-open handling.
func (c *httpEnrichClient) post(ctx context.Context, ev *event.ChangeEvent) (json.RawMessage, string, error) {
	if reason, err := c.checkCircuit(); reason != "" {
		return nil, reason, err
	}

	body, err := json.Marshal(ev)
	if err != nil {
		return nil, ReasonError, fmt.Errorf("marshal event: %w", err)
	}

	// Bound every call with an explicit deadline so fail-open cannot hang
	// the pipeline (AIC-01).
	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, ReasonError, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil || isTimeout(err) {
			c.recordTimeout()
			return nil, ReasonTimeout, err
		}
		return nil, ReasonError, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNoContent:
		c.recordSuccess()
		// Drain and discard; 204 = no context.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return nil, "", nil
	case http.StatusOK:
		aiCtx, reason, readErr := readAIContext(resp.Body)
		if reason != "" {
			return nil, reason, readErr
		}
		c.recordSuccess()
		return aiCtx, "", nil
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return nil, ReasonStatus, fmt.Errorf("enricher HTTP %d", resp.StatusCode)
	}
}

func (c *httpEnrichClient) checkCircuit() (string, error) {
	c.circuitMu.Lock()
	defer c.circuitMu.Unlock()
	if c.consecutiveTimeouts >= c.circuitThreshold {
		return ReasonCircuitOpen, fmt.Errorf("enrichment circuit open after %d consecutive timeouts", c.consecutiveTimeouts)
	}
	return "", nil
}

func (c *httpEnrichClient) recordTimeout() {
	c.circuitMu.Lock()
	c.consecutiveTimeouts++
	c.circuitMu.Unlock()
}

func (c *httpEnrichClient) recordSuccess() {
	c.circuitMu.Lock()
	c.consecutiveTimeouts = 0
	c.circuitMu.Unlock()
}

func readAIContext(r io.Reader) (json.RawMessage, string, error) {
	limited := io.LimitReader(r, maxAIContextBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, ReasonError, err
	}
	if len(data) > maxAIContextBytes {
		return nil, ReasonOversize, fmt.Errorf("ai_context exceeds %d bytes (AIC-02)", maxAIContextBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, ReasonInvalid, fmt.Errorf("empty ai_context body")
	}
	if !json.Valid(data) {
		return nil, ReasonInvalid, fmt.Errorf("ai_context is not valid JSON")
	}
	// Must be a JSON object (not array/string/number/null).
	trim := bytes.TrimSpace(data)
	if len(trim) == 0 || trim[0] != '{' {
		return nil, ReasonNonObject, fmt.Errorf("ai_context must be a JSON object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, ReasonNonObject, fmt.Errorf("ai_context must be a JSON object: %w", err)
	}
	// Re-marshal compact form for stable storage; preserve as RawMessage.
	out := make(json.RawMessage, len(data))
	copy(out, data)
	return out, "", nil
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		return true
	}
	return false
}
