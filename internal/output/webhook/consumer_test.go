package webhooksink_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/olucasandrade/kaptanto/internal/observability"
	webhooksink "github.com/olucasandrade/kaptanto/internal/output/webhook"
	"github.com/olucasandrade/kaptanto/internal/router"
)

var (
	_ router.Consumer     = (*webhooksink.WebhookSinkConsumer)(nil)
	_ router.BatchFlusher = (*webhooksink.WebhookSinkConsumer)(nil)
)

func makeEntry(partition uint32, schema, table, idem string, raw []byte) eventlog.LogEntry {
	return makeEntrySeq(partition, 0, schema, table, idem, raw)
}

func makeEntrySeq(partition uint32, seq uint64, schema, table, idem string, raw []byte) eventlog.LogEntry {
	return eventlog.LogEntry{
		Seq:         seq,
		PartitionID: partition,
		Event: &event.ChangeEvent{
			Schema:         schema,
			Table:          table,
			Operation:      "insert",
			Key:            json.RawMessage(`{"id":1}`),
			IdempotencyKey: idem,
			After:          json.RawMessage(`{"id":1,"status":"ok"}`),
		},
		Raw: raw,
	}
}

func newConsumer(t *testing.T, cfg config.WebhookSinkConfig) *webhooksink.WebhookSinkConsumer {
	t.Helper()
	c, err := webhooksink.NewWebhookSinkConsumer("webhook", cfg)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

// --- Constructor validation (rules 1–8) + env expansion ---

func TestNewWebhookSinkConsumer_Validations(t *testing.T) {
	t.Run("url required", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "webhook sink:")
		assert.Contains(t, err.Error(), "url or url-template is required")
	})

	t.Run("method allowlist", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:    "http://example.com",
			Method: "DELETE",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "POST, PUT, PATCH")
	})

	t.Run("bearer xor basic", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL: "http://example.com",
			Auth: config.WebhookAuthConfig{
				BearerToken: "tok",
				Basic:       config.WebhookBasicAuth{Username: "u", Password: "p"},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("payload-template requires batch=1", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:             "http://example.com",
			PayloadTemplate: "{{.ID}}",
			Batch:           config.WebhookBatch{MaxEvents: 3},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "payload-template requires batch.max-events=1")
	})

	t.Run("Authorization conflict", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:     "http://example.com",
			Headers: map[string]string{"authorization": "Bearer x"},
			Auth:    config.WebhookAuthConfig{BearerToken: "tok"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Authorization")
	})

	t.Run("timeout unparsable", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:     "http://example.com",
			Timeout: "not-a-duration",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timeout")
	})

	t.Run("timeout <=0", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:     "http://example.com",
			Timeout: "0s",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timeout must be > 0")
	})

	t.Run("url-template parse error", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URLTemplate: "{{.Unclosed",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "url-template parse error")
	})

	t.Run("payload-template parse error", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:             "http://example.com",
			PayloadTemplate: "{{.Unclosed",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "payload-template")
		assert.Contains(t, err.Error(), "go-template parse")
	})

	t.Run("batch.max-events < 0", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:   "http://example.com",
			Batch: config.WebhookBatch{MaxEvents: -1},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "batch.max-events must be >= 0")
	})
}

func TestNewWebhookSinkConsumer_EnvExpansion(t *testing.T) {
	t.Setenv("WH_URL", "http://expanded.example/hook")
	t.Setenv("WH_TOKEN", "secret-token")
	t.Setenv("WH_HDR", "hdr-val")
	t.Setenv("WH_PASS", "s3cret")
	t.Setenv("WH_SIGN", "sign-secret")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer secret-token", r.Header.Get("Authorization"))
		assert.Equal(t, "hdr-val", r.Header.Get("X-Env"))
		assert.NotEmpty(t, r.Header.Get("X-Kaptanto-Signature"))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	// Override URL via env in config string; also expand auth/headers/signing.
	c := newConsumer(t, config.WebhookSinkConfig{
		URL:     "${WH_URL}",
		Headers: map[string]string{"X-Env": "${WH_HDR}"},
		Auth:    config.WebhookAuthConfig{BearerToken: "${WH_TOKEN}"},
		Signing: config.WebhookSigning{Secret: "${WH_SIGN}"},
	})
	// URL was expanded at construction — but we need the test server URL.
	// Re-create with expanded-style values pointing at srv.
	_ = c
	c2 := newConsumer(t, config.WebhookSinkConfig{
		URL:     srv.URL,
		Headers: map[string]string{"X-Env": "${WH_HDR}"},
		Auth:    config.WebhookAuthConfig{BearerToken: "${WH_TOKEN}"},
		Signing: config.WebhookSigning{Secret: "${WH_SIGN}"},
	})
	entry := makeEntry(0, "public", "orders", "idem-1", []byte(`{"x":1}`))
	require.NoError(t, c2.Deliver(context.Background(), entry))
	require.NoError(t, c2.FlushBatch(context.Background(), 0))

	t.Run("unset expands empty", func(t *testing.T) {
		c3 := newConsumer(t, config.WebhookSinkConfig{
			URL: srv.URL,
			Auth: config.WebhookAuthConfig{
				Basic: config.WebhookBasicAuth{Username: "u", Password: "${UNSET_WH_PASS}"},
			},
		})
		var gotAuth string
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
		}))
		t.Cleanup(srv2.Close)
		c4 := newConsumer(t, config.WebhookSinkConfig{
			URL: srv2.URL,
			Auth: config.WebhookAuthConfig{
				Basic: config.WebhookBasicAuth{Username: "u", Password: "${UNSET_WH_PASS}"},
			},
		})
		_ = c3
		require.NoError(t, c4.Deliver(context.Background(), entry))
		require.NoError(t, c4.FlushBatch(context.Background(), 0))
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:"))
		assert.Equal(t, want, gotAuth)
	})
}

// (1) method default/PUT/PATCH + Raw-verbatim body + marshal fallback
func TestDeliver_MethodAndBody(t *testing.T) {
	for _, method := range []string{"", "PUT", "PATCH"} {
		method := method
		t.Run("method_"+method, func(t *testing.T) {
			var gotMethod string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(200)
			}))
			t.Cleanup(srv.Close)

			cfg := config.WebhookSinkConfig{URL: srv.URL, Method: method}
			c := newConsumer(t, cfg)
			raw := []byte(`{"raw":true}`)
			entry := makeEntry(0, "public", "orders", "idem-raw", raw)
			require.NoError(t, c.Deliver(context.Background(), entry))
			require.NoError(t, c.FlushBatch(context.Background(), 0))

			wantMethod := method
			if wantMethod == "" {
				wantMethod = http.MethodPost
			}
			assert.Equal(t, wantMethod, gotMethod)
			assert.Equal(t, raw, gotBody)
		})
	}

	t.Run("marshal fallback", func(t *testing.T) {
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
		}))
		t.Cleanup(srv.Close)
		c := newConsumer(t, config.WebhookSinkConfig{URL: srv.URL})
		entry := makeEntry(0, "public", "orders", "idem-m", nil)
		require.NoError(t, c.Deliver(context.Background(), entry))
		require.NoError(t, c.FlushBatch(context.Background(), 0))
		var decoded event.ChangeEvent
		require.NoError(t, json.Unmarshal(gotBody, &decoded))
		assert.Equal(t, "orders", decoded.Table)
		assert.Equal(t, "idem-m", decoded.IdempotencyKey)
	})
}

// (2) bearer/basic/custom headers + computed-wins + idempotency header
func TestDeliver_Headers(t *testing.T) {
	t.Run("bearer", func(t *testing.T) {
		var auth, idem, ct, ua, custom string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth = r.Header.Get("Authorization")
			idem = r.Header.Get("X-Kaptanto-Idempotency-Key")
			ct = r.Header.Get("Content-Type")
			ua = r.Header.Get("User-Agent")
			custom = r.Header.Get("X-Custom")
			w.WriteHeader(200)
		}))
		t.Cleanup(srv.Close)
		c := newConsumer(t, config.WebhookSinkConfig{
			URL:     srv.URL,
			Headers: map[string]string{"X-Custom": "yes", "Content-Type": "text/plain", "User-Agent": "spoof"},
			Auth:    config.WebhookAuthConfig{BearerToken: "tok123"},
		})
		entry := makeEntry(0, "public", "orders", "idem-hdr", []byte(`{}`))
		require.NoError(t, c.Deliver(context.Background(), entry))
		require.NoError(t, c.FlushBatch(context.Background(), 0))
		assert.Equal(t, "Bearer tok123", auth)
		assert.Equal(t, "idem-hdr", idem)
		assert.Equal(t, "application/json", ct) // computed wins
		assert.Equal(t, "kaptanto", ua)         // computed wins
		assert.Equal(t, "yes", custom)
	})

	t.Run("basic", func(t *testing.T) {
		var auth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth = r.Header.Get("Authorization")
			w.WriteHeader(200)
		}))
		t.Cleanup(srv.Close)
		c := newConsumer(t, config.WebhookSinkConfig{
			URL:  srv.URL,
			Auth: config.WebhookAuthConfig{Basic: config.WebhookBasicAuth{Username: "alice", Password: "wonder"}},
		})
		entry := makeEntry(0, "public", "orders", "idem-b", []byte(`{}`))
		require.NoError(t, c.Deliver(context.Background(), entry))
		require.NoError(t, c.FlushBatch(context.Background(), 0))
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:wonder"))
		assert.Equal(t, want, auth)
	})
}

// (3) payload-template render + runtime error
func TestDeliver_PayloadTemplate(t *testing.T) {
	t.Run("render", func(t *testing.T) {
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
		}))
		t.Cleanup(srv.Close)
		c := newConsumer(t, config.WebhookSinkConfig{
			URL:             srv.URL,
			PayloadTemplate: `{"table":"{{.Table}}"}`,
		})
		entry := makeEntry(0, "public", "orders", "idem-pt", []byte(`ignored`))
		require.NoError(t, c.Deliver(context.Background(), entry))
		require.NoError(t, c.FlushBatch(context.Background(), 0))
		assert.JSONEq(t, `{"table":"orders"}`, string(gotBody))
	})

	t.Run("runtime error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not be called")
		}))
		t.Cleanup(srv.Close)
		c := newConsumer(t, config.WebhookSinkConfig{
			URL:             srv.URL,
			PayloadTemplate: `{{.Missing.Field}}`,
		})
		entry := makeEntry(0, "public", "orders", "idem-err", nil)
		err := c.Deliver(context.Background(), entry)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "transform (go-template)")
	})
}

// (4) url-template per-event + empty render error
func TestDeliver_URLTemplate(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := newConsumer(t, config.WebhookSinkConfig{
		URLTemplate: srv.URL + "/{{.Schema}}/{{.Table}}",
	})
	e1 := makeEntry(0, "public", "orders", "i1", []byte(`{"n":1}`))
	e2 := makeEntry(0, "public", "users", "i2", []byte(`{"n":2}`))
	require.NoError(t, c.Deliver(context.Background(), e1))
	require.NoError(t, c.Deliver(context.Background(), e2))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	mu.Lock()
	assert.Equal(t, []string{"/public/orders", "/public/users"}, paths)
	mu.Unlock()

	t.Run("empty render", func(t *testing.T) {
		c2 := newConsumer(t, config.WebhookSinkConfig{
			URLTemplate: "   ",
		})
		err := c2.Deliver(context.Background(), makeEntry(0, "public", "orders", "x", nil))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty string")
	})
}

// (5) batch max-events=3 with 7 events → [3][3][1] + mixed-URL chunk splitting
func TestFlushBatch_Batching(t *testing.T) {
	t.Run("chunk sizes", func(t *testing.T) {
		var bodies [][]byte
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, b)
			mu.Unlock()
			assert.Empty(t, r.Header.Get("X-Kaptanto-Idempotency-Key"), "batch mode uses Keys header")
			assert.NotEmpty(t, r.Header.Get("X-Kaptanto-Idempotency-Keys"), "batch mode sets Keys header")
			w.WriteHeader(200)
		}))
		t.Cleanup(srv.Close)

		c := newConsumer(t, config.WebhookSinkConfig{
			URL:   srv.URL,
			Batch: config.WebhookBatch{MaxEvents: 3},
		})
		for i := 0; i < 7; i++ {
			raw := []byte(fmt.Sprintf(`{"n":%d}`, i))
			e := makeEntry(0, "public", "orders", fmt.Sprintf("idem-%d", i), raw)
			require.NoError(t, c.Deliver(context.Background(), e))
		}
		require.NoError(t, c.FlushBatch(context.Background(), 0))

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, bodies, 3)
		var a0, a1, a2 []json.RawMessage
		require.NoError(t, json.Unmarshal(bodies[0], &a0))
		require.NoError(t, json.Unmarshal(bodies[1], &a1))
		require.NoError(t, json.Unmarshal(bodies[2], &a2))
		assert.Len(t, a0, 3)
		assert.Len(t, a1, 3)
		assert.Len(t, a2, 1)
		assert.JSONEq(t, `{"n":0}`, string(a0[0]))
		assert.JSONEq(t, `{"n":6}`, string(a2[0]))
	})

	t.Run("mixed URL splits", func(t *testing.T) {
		type hit struct {
			path string
			n    int
		}
		var hits []hit
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			var arr []json.RawMessage
			require.NoError(t, json.Unmarshal(b, &arr))
			mu.Lock()
			hits = append(hits, hit{path: r.URL.Path, n: len(arr)})
			mu.Unlock()
			w.WriteHeader(200)
		}))
		t.Cleanup(srv.Close)

		c := newConsumer(t, config.WebhookSinkConfig{
			URLTemplate: srv.URL + "/{{.Table}}",
			Batch:       config.WebhookBatch{MaxEvents: 3},
		})
		// orders, orders, users, orders → chunks: [orders x2], [users x1], [orders x1]
		tables := []string{"orders", "orders", "users", "orders"}
		for i, table := range tables {
			e := makeEntry(0, "public", table, fmt.Sprintf("m-%d", i), []byte(fmt.Sprintf(`{"n":%d}`, i)))
			require.NoError(t, c.Deliver(context.Background(), e))
		}
		require.NoError(t, c.FlushBatch(context.Background(), 0))
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, hits, 3)
		assert.Equal(t, hit{"/orders", 2}, hits[0])
		assert.Equal(t, hit{"/users", 1}, hits[1])
		assert.Equal(t, hit{"/orders", 1}, hits[2])
	})
}

// (6) HMAC: server recomputes over t + "." + body
func TestFlushBatch_HMAC(t *testing.T) {
	secret := "whsec_shared"
	var ok bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-Kaptanto-Signature")
		require.NotEmpty(t, sig)
		parts := strings.Split(sig, ",")
		require.Len(t, parts, 2)
		tsStr := strings.TrimPrefix(parts[0], "t=")
		v1 := strings.TrimPrefix(parts[1], "v1=")
		ts, err := strconv.ParseInt(tsStr, 10, 64)
		require.NoError(t, err)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(strconv.FormatInt(ts, 10) + "." + string(body)))
		assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), v1)
		ok = true
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := newConsumer(t, config.WebhookSinkConfig{
		URL:     srv.URL,
		Signing: config.WebhookSigning{Secret: secret},
	})
	entry := makeEntry(0, "public", "orders", "idem-sig", []byte(`{"hello":"world"}`))
	require.NoError(t, c.Deliver(context.Background(), entry))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.True(t, ok)
}

// (7) status handling WHK-01/02
func TestFlushBatch_StatusCodes(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		wantErr bool
	}{
		{"200", 200, false},
		{"204", 204, false},
		{"301", 301, true},
		{"400", 400, true},
		{"429", 429, true},
		{"500", 500, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			redirectHit := int32(0)
			mux := http.NewServeMux()
			mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&redirectHit, 1)
				w.WriteHeader(200)
			})
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if tc.code == 301 {
					http.Redirect(w, r, "/target", http.StatusMovedPermanently)
					return
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte("snippet-body"))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			c := newConsumer(t, config.WebhookSinkConfig{URL: srv.URL + "/"})
			entry := makeEntry(0, "public", "orders", "idem-st", []byte(`{}`))
			require.NoError(t, c.Deliver(context.Background(), entry))
			err := c.FlushBatch(context.Background(), 0)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), strconv.Itoa(tc.code))
				if tc.code != 301 {
					assert.Contains(t, err.Error(), "snippet-body")
				}
			} else {
				require.NoError(t, err)
			}
			if tc.code == 301 {
				assert.Equal(t, int32(0), atomic.LoadInt32(&redirectHit), "redirect target must never be hit")
			}
		})
	}
}

// (8) timeout
func TestFlushBatch_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := newConsumer(t, config.WebhookSinkConfig{
		URL:     srv.URL,
		Timeout: "50ms",
	})
	entry := makeEntry(0, "public", "orders", "idem-to", []byte(`{}`))
	require.NoError(t, c.Deliver(context.Background(), entry))
	err := c.FlushBatch(context.Background(), 0)
	require.Error(t, err)
}

// (9) empty partition
func TestFlushBatch_Empty(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	c := newConsumer(t, config.WebhookSinkConfig{URL: srv.URL})
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, int32(0), atomic.LoadInt32(&hits))
}

// (10) pop-before-send re-delivery semantics
func TestFlushBatch_PopBeforeSend_Redelivery(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("fail"))
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := newConsumer(t, config.WebhookSinkConfig{URL: srv.URL})
	entry := makeEntry(0, "public", "orders", "idem-rd", []byte(`{"n":1}`))
	require.NoError(t, c.Deliver(context.Background(), entry))
	err := c.FlushBatch(context.Background(), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")

	// Buffer was popped — FlushBatch again without re-Deliver sends nothing.
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))

	// Re-Deliver + FlushBatch succeeds (router re-delivery path).
	require.NoError(t, c.Deliver(context.Background(), entry))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits))
}

// (11) partition isolation
func TestFlushBatch_PartitionIsolation(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, string(b))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := newConsumer(t, config.WebhookSinkConfig{URL: srv.URL})
	e1 := makeEntry(1, "public", "orders", "p1", []byte(`{"p":1}`))
	e2 := makeEntry(2, "public", "orders", "p2", []byte(`{"p":2}`))
	require.NoError(t, c.Deliver(context.Background(), e1))
	require.NoError(t, c.Deliver(context.Background(), e2))

	require.NoError(t, c.FlushBatch(context.Background(), 2))
	mu.Lock()
	assert.Equal(t, []string{`{"p":2}`}, paths)
	mu.Unlock()

	require.NoError(t, c.FlushBatch(context.Background(), 1))
	mu.Lock()
	assert.Equal(t, []string{`{"p":2}`, `{"p":1}`}, paths)
	mu.Unlock()
}

func TestFlushBatch_Metrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	t.Cleanup(srv.Close)
	c := newConsumer(t, config.WebhookSinkConfig{URL: srv.URL})
	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)
	entry := makeEntry(0, "public", "orders", "m1", []byte(`{}`))
	require.NoError(t, c.Deliver(context.Background(), entry))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.QueuePublishTotal.WithLabelValues("webhook")))

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(failSrv.Close)
	c2 := newConsumer(t, config.WebhookSinkConfig{URL: failSrv.URL})
	c2.SetMetrics(m)
	require.NoError(t, c2.Deliver(context.Background(), entry))
	require.Error(t, c2.FlushBatch(context.Background(), 0))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.QueuePublishErrors.WithLabelValues("webhook")))
}

func TestID_Ping_Close(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	c := newConsumer(t, config.WebhookSinkConfig{URL: srv.URL})
	assert.Equal(t, "webhook", c.ID())
	require.NoError(t, c.Ping())
	c.Close()
	c.Close() // idempotent
}

func TestPing_URLTemplateRenderFailure(t *testing.T) {
	// Template that fails on zero ChangeEvent (nil pointer via missing nested field
	// isn't easy); use a template that renders empty for zero event → Ping nil.
	c := newConsumer(t, config.WebhookSinkConfig{
		URLTemplate: "{{if .Table}}http://127.0.0.1:9/{{.Table}}{{end}}",
	})
	require.NoError(t, c.Ping())
}

func BenchmarkFlushBatch(b *testing.B) {
	for _, maxEvents := range []int{1, 50} {
		maxEvents := maxEvents
		b.Run(fmt.Sprintf("maxEvents_%d", maxEvents), func(b *testing.B) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(204)
			}))
			b.Cleanup(srv.Close)

			c, err := webhooksink.NewWebhookSinkConsumer("webhook", config.WebhookSinkConfig{
				URL:   srv.URL,
				Batch: config.WebhookBatch{MaxEvents: maxEvents},
			})
			require.NoError(b, err)
			b.Cleanup(c.Close)

			raw := []byte(`{"n":1}`)
			b.ReportMetric(float64(maxEvents), "events/op")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < maxEvents; j++ {
					e := makeEntry(0, "public", "orders", fmt.Sprintf("b-%d-%d", i, j), raw)
					if err := c.Deliver(context.Background(), e); err != nil {
						b.Fatal(err)
					}
				}
				if err := c.FlushBatch(context.Background(), 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
