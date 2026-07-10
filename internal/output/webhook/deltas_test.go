package webhooksink_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
	webhooksink "github.com/olucasandrade/kaptanto/internal/output/webhook"
	"github.com/olucasandrade/kaptanto/internal/router"
	"github.com/olucasandrade/kaptanto/internal/transform"
)

func TestNewWebhookSinkConsumer_Validations9to13(t *testing.T) {
	t.Run("9 payload-template and transform mutually exclusive", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:             "http://example.com",
			PayloadTemplate: "{{.Table}}",
			Transform:       config.TransformConfig{Language: "jq", Expression: "."},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "payload-template is shorthand for transform; set only one")
	})

	t.Run("10 go-template with batch>1", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL: "http://example.com",
			Transform: config.TransformConfig{
				Language:   "go-template",
				Expression: "{{.Table}}",
			},
			Batch: config.WebhookBatch{MaxEvents: 3},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "batch.max-events=1")
	})

	t.Run("10 jq with batch>1 allowed", func(t *testing.T) {
		c, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL: "http://example.com",
			Transform: config.TransformConfig{
				Language:   "jq",
				Expression: ".",
			},
			Batch: config.WebhookBatch{MaxEvents: 3},
		})
		require.NoError(t, err)
		c.Close()
	})

	t.Run("11 unknown language", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL: "http://example.com",
			Transform: config.TransformConfig{
				Language:   "python",
				Expression: "print",
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jq")
		assert.Contains(t, err.Error(), "go-template")
	})

	t.Run("12 language without expression", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:       "http://example.com",
			Transform: config.TransformConfig{Language: "jq"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "both be set or both be empty")
	})

	t.Run("12 expression without language", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL:       "http://example.com",
			Transform: config.TransformConfig{Expression: "."},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "both be set or both be empty")
	})

	t.Run("13 compile error", func(t *testing.T) {
		_, err := webhooksink.NewWebhookSinkConsumer("w", config.WebhookSinkConfig{
			URL: "http://example.com",
			Transform: config.TransformConfig{
				Language:   "jq",
				Expression: "((((",
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "transform:")
	})
}

func TestDeliver_PayloadTemplateSugarEquivalence(t *testing.T) {
	expr := `{"table":"{{.Table}}","idem":"{{.IdempotencyKey}}"}`
	var bodyPT, bodyTF []byte

	srvPT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyPT, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	t.Cleanup(srvPT.Close)
	cPT := newConsumer(t, config.WebhookSinkConfig{URL: srvPT.URL, PayloadTemplate: expr})

	srvTF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyTF, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	t.Cleanup(srvTF.Close)
	cTF := newConsumer(t, config.WebhookSinkConfig{
		URL: srvTF.URL,
		Transform: config.TransformConfig{
			Language:   "go-template",
			Expression: expr,
		},
	})

	entry := makeEntry(0, "public", "orders", "idem-sugar", []byte(`ignored`))
	require.NoError(t, cPT.Deliver(context.Background(), entry))
	require.NoError(t, cPT.FlushBatch(context.Background(), 0))
	require.NoError(t, cTF.Deliver(context.Background(), entry))
	require.NoError(t, cTF.FlushBatch(context.Background(), 0))
	assert.Equal(t, bodyPT, bodyTF, "payload-template and transform go-template must be byte-identical")
}

func TestDeliver_TransformDrop(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := newConsumer(t, config.WebhookSinkConfig{
		URL: srv.URL,
		Transform: config.TransformConfig{
			Language:   "go-template",
			Expression: `{{if eq .Table "dropme"}}{{else}}{"ok":true}{{end}}`,
		},
	})
	m := observability.NewKaptantoMetrics()
	c.SetMetrics(m)

	for i := 0; i < 3; i++ {
		e := makeEntry(0, "public", "dropme", fmt.Sprintf("d-%d", i), []byte(`{}`))
		require.NoError(t, c.Deliver(context.Background(), e))
	}
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, int32(0), atomic.LoadInt32(&hits))
	assert.Equal(t, float64(3), testutil.ToFloat64(m.TransformDroppedTotal.WithLabelValues("webhook")))

	keep := makeEntry(0, "public", "orders", "keep", []byte(`{}`))
	require.NoError(t, c.Deliver(context.Background(), keep))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

func TestDeliver_TransformRuntimePermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))
	t.Cleanup(srv.Close)
	c := newConsumer(t, config.WebhookSinkConfig{
		URL: srv.URL,
		Transform: config.TransformConfig{
			Language:   "go-template",
			Expression: `{{.Missing.Field}}`,
		},
	})
	err := c.Deliver(context.Background(), makeEntry(0, "public", "orders", "x", nil))
	require.Error(t, err)
	var re *transform.RuntimeError
	require.True(t, errors.As(err, &re))
}

func TestDeliver_JQBareValue(t *testing.T) {
	var gotBody []byte
	var ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		ct = r.Header.Get("Content-Type")
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	c := newConsumer(t, config.WebhookSinkConfig{
		URL: srv.URL,
		Transform: config.TransformConfig{
			Language:   "jq",
			Expression: ".n",
		},
	})
	entry := makeEntry(0, "public", "orders", "jq1", []byte(`{"n":42}`))
	require.NoError(t, c.Deliver(context.Background(), entry))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.Equal(t, "42", string(gotBody))
	assert.Equal(t, "application/json", ct)
}

func TestFlushBatch_ResponseClassification(t *testing.T) {
	cases := []struct {
		name      string
		code      int
		wantPerm  bool
		wantPlain bool
	}{
		{"200", 200, false, false},
		{"204", 204, false, false},
		{"301", 301, true, false},
		{"400", 400, true, false},
		{"422", 422, true, false},
		{"408", 408, false, true},
		{"429", 429, false, true},
		{"500", 500, false, true},
		{"503", 503, false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte("body"))
			}))
			t.Cleanup(srv.Close)
			c := newConsumer(t, config.WebhookSinkConfig{URL: srv.URL})
			e := makeEntrySeq(0, 99, "public", "orders", "cls", []byte(`{}`))
			require.NoError(t, c.Deliver(context.Background(), e))
			err := c.FlushBatch(context.Background(), 0)
			if !tc.wantPerm && !tc.wantPlain {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var pfe *router.PermanentFlushError
			isPerm := errors.As(err, &pfe)
			if tc.wantPerm {
				require.True(t, isPerm, "expected PermanentFlushError, got %T: %v", err, err)
				assert.Equal(t, uint64(99), pfe.Seq)
			} else {
				require.False(t, isPerm, "expected plain transient error, got PermanentFlushError")
			}
		})
	}
}

func TestFlushBatch_BatchPoisonIsolation(t *testing.T) {
	type hit struct {
		isArray bool
		n       int
		keys    string
	}
	var hits []hit
	var mu sync.Mutex
	var phase int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		keys := r.Header.Get("X-Kaptanto-Idempotency-Keys")
		key := r.Header.Get("X-Kaptanto-Idempotency-Key")
		mu.Lock()
		isArray := len(b) > 0 && b[0] == '['
		h := hit{isArray: isArray, keys: keys}
		if isArray {
			var arr []json.RawMessage
			_ = json.Unmarshal(b, &arr)
			h.n = len(arr)
		} else {
			h.n = 1
			h.keys = key
		}
		hits = append(hits, h)
		mu.Unlock()

		p := atomic.AddInt32(&phase, 1)
		if p == 1 {
			w.WriteHeader(422)
			_, _ = w.Write([]byte("unprocessable"))
			return
		}
		if key == "idem-1" {
			w.WriteHeader(422)
			_, _ = w.Write([]byte("bad-event"))
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := newConsumer(t, config.WebhookSinkConfig{
		URL:   srv.URL,
		Batch: config.WebhookBatch{MaxEvents: 3},
	})
	for i := 0; i < 7; i++ {
		e := makeEntrySeq(0, uint64(i), "public", "orders", fmt.Sprintf("idem-%d", i), []byte(fmt.Sprintf(`{"n":%d}`, i)))
		require.NoError(t, c.Deliver(context.Background(), e))
	}
	err := c.FlushBatch(context.Background(), 0)
	require.Error(t, err)
	var pfe *router.PermanentFlushError
	require.True(t, errors.As(err, &pfe))
	assert.Equal(t, uint64(1), pfe.Seq)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(hits), 3)
	assert.True(t, hits[0].isArray)
	assert.Equal(t, 3, hits[0].n)
	assert.Equal(t, "idem-0,idem-1,idem-2", hits[0].keys)
	assert.False(t, hits[1].isArray)
	assert.Equal(t, "idem-0", hits[1].keys)
	assert.False(t, hits[2].isArray)
	assert.Equal(t, "idem-1", hits[2].keys)
	for _, h := range hits[3:] {
		assert.NotEqual(t, "idem-2", h.keys)
		assert.False(t, h.isArray, "must not continue to next array chunk after poison")
	}
}

func TestFlushBatch_BatchIsolationTransientAborts(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(400)
			return
		}
		if n == 2 {
			w.WriteHeader(503)
			return
		}
		t.Errorf("unexpected extra request #%d body=%s", n, b)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := newConsumer(t, config.WebhookSinkConfig{
		URL:   srv.URL,
		Batch: config.WebhookBatch{MaxEvents: 3},
	})
	for i := 0; i < 3; i++ {
		e := makeEntrySeq(0, uint64(i+10), "public", "orders", fmt.Sprintf("t-%d", i), []byte(fmt.Sprintf(`{"n":%d}`, i)))
		require.NoError(t, c.Deliver(context.Background(), e))
	}
	err := c.FlushBatch(context.Background(), 0)
	require.Error(t, err)
	var pfe *router.PermanentFlushError
	assert.False(t, errors.As(err, &pfe), "transient mid-isolation must be plain error")
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits), "must not resend after transient")
}

func TestFlushBatch_BatchIdempotencyKeysHeader(t *testing.T) {
	var gotKeys []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKeys = append(gotKeys, r.Header.Get("X-Kaptanto-Idempotency-Keys"))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	c := newConsumer(t, config.WebhookSinkConfig{
		URL:   srv.URL,
		Batch: config.WebhookBatch{MaxEvents: 3},
	})
	for i := 0; i < 4; i++ {
		e := makeEntry(0, "public", "orders", fmt.Sprintf("k%d", i), []byte(fmt.Sprintf(`{"n":%d}`, i)))
		require.NoError(t, c.Deliver(context.Background(), e))
	}
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, gotKeys, 2)
	assert.Equal(t, "k0,k1,k2", gotKeys[0])
	assert.Equal(t, "k3", gotKeys[1])
}

func TestFlushBatch_SignatureOverTransformedBody(t *testing.T) {
	secret := "whsec_xform"
	var ok bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.Equal(t, `{"t":"orders"}`, string(body))
		sig := r.Header.Get("X-Kaptanto-Signature")
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
		Transform: config.TransformConfig{
			Language:   "go-template",
			Expression: `{"t":"{{.Table}}"}`,
		},
	})
	entry := makeEntry(0, "public", "orders", "sig-x", []byte(`{"raw":true}`))
	require.NoError(t, c.Deliver(context.Background(), entry))
	require.NoError(t, c.FlushBatch(context.Background(), 0))
	assert.True(t, ok)
}

func BenchmarkFlushBatch_WithTransform(b *testing.B) {
	for _, maxEvents := range []int{1, 50} {
		maxEvents := maxEvents
		b.Run(fmt.Sprintf("jq_maxEvents_%d", maxEvents), func(b *testing.B) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(204)
			}))
			b.Cleanup(srv.Close)

			c, err := webhooksink.NewWebhookSinkConsumer("webhook", config.WebhookSinkConfig{
				URL:   srv.URL,
				Batch: config.WebhookBatch{MaxEvents: maxEvents},
				Transform: config.TransformConfig{
					Language:   "jq",
					Expression: ".",
				},
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
