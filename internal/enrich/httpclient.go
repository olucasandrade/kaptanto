package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/olucasandrade/kaptanto/internal/event"
)

// httpEnrichClient POSTs ChangeEvents to the enricher endpoint.
type httpEnrichClient struct {
	url     string
	token   string
	timeout time.Duration
	client  *http.Client
}

func newHTTPEnrichClient(rawURL string, timeout time.Duration, authToken string) (*httpEnrichClient, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("url: invalid enricher endpoint %q", rawURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("url: unsupported scheme %q (want http or https)", u.Scheme)
	}

	return &httpEnrichClient{
		url:     rawURL,
		token:   authToken,
		timeout: timeout,
		client: &http.Client{
			Transport: &http.Transport{DisableKeepAlives: true},
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
			return nil, ReasonTimeout, err
		}
		return nil, ReasonError, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNoContent:
		// Drain and discard; 204 = no context.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return nil, "", nil
	case http.StatusOK:
		return readAIContext(resp.Body)
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return nil, ReasonStatus, fmt.Errorf("enricher HTTP %d", resp.StatusCode)
	}
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
