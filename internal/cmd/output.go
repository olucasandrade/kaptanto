package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/olucasandrade/kaptanto/internal/auth"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/openapi"
	"github.com/olucasandrade/kaptanto/internal/output"
	grpcoutput "github.com/olucasandrade/kaptanto/internal/output/grpc"
	kafkasink "github.com/olucasandrade/kaptanto/internal/output/kafka"
	natssink "github.com/olucasandrade/kaptanto/internal/output/nats"
	pubsubsink "github.com/olucasandrade/kaptanto/internal/output/pubsub"
	rabbitmqsink "github.com/olucasandrade/kaptanto/internal/output/rabbitmq"
	sqssink "github.com/olucasandrade/kaptanto/internal/output/sqs"
	"github.com/olucasandrade/kaptanto/internal/output/sse"
	"github.com/olucasandrade/kaptanto/internal/output/stdout"
	vectorsink "github.com/olucasandrade/kaptanto/internal/output/vector"
	webhooksink "github.com/olucasandrade/kaptanto/internal/output/webhook"
	"github.com/olucasandrade/kaptanto/internal/router"
)

// httpServerReadHeaderTimeout bounds how long a client may take to send request
// headers. It defends the SSE and observability endpoints against Slowloris-style
// slow-header attacks that would otherwise hold connections (and, for SSE, router
// subscriptions) open indefinitely.
const httpServerReadHeaderTimeout = 10 * time.Second

// httpServerIdleTimeout closes idle keep-alive connections so abandoned clients
// do not accumulate. It does not affect an active SSE stream, which is
// continuously writing.
const httpServerIdleTimeout = 120 * time.Second

// newHTTPServer builds an *http.Server with hardened timeouts shared by every
// network endpoint kaptanto exposes. WriteTimeout is intentionally left at 0:
// the SSE handler holds a single long-lived response open for the life of the
// stream, and a WriteTimeout would terminate it.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpServerReadHeaderTimeout,
		IdleTimeout:       httpServerIdleTimeout,
	}
}

// buildServerTLSConfig builds a *tls.Config from ServerTLSConfig fields.
// Returns nil, nil when no cert/key are configured (plaintext mode).
// Returns an error when cert/key paths are set but cannot be loaded.
// When clientCAFile is set, mTLS is enabled: client certificates are required
// and verified against the given CA. MinVersion is always TLS 1.2.
func buildServerTLSConfig(cfg config.ServerTLSConfig) (*tls.Config, error) {
	if cfg.CertFile == "" && cfg.KeyFile == "" && cfg.ClientCAFile == "" {
		return nil, nil
	}
	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return nil, fmt.Errorf("server TLS: both --tls-cert and --tls-key must be set together; --tls-client-ca also requires them")
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("server TLS: load cert/key: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if cfg.ClientCAFile != "" {
		caPEM, err := os.ReadFile(cfg.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("server TLS: read client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("server TLS: no valid certificates found in client CA file %q", cfg.ClientCAFile)
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return tlsCfg, nil
}

// requireServerTLS validates the TLS policy for network outputs (sse, grpc).
// Returns an error when no TLS is configured and --insecure is not set.
// Logs a loud warning when insecure mode is explicitly requested.
func requireServerTLS(output string, tlsCfg *tls.Config, insecure bool) error {
	if tlsCfg != nil {
		return nil // TLS is configured — all good
	}
	if insecure {
		slog.Warn("SECURITY WARNING: running "+output+" output in plaintext (--insecure). "+
			"Change stream data is transmitted without encryption. "+
			"Use --tls-cert / --tls-key to enable TLS.",
			"output", output)
		return nil
	}
	return fmt.Errorf(
		"%s output requires TLS: provide --tls-cert and --tls-key, or pass --insecure to opt out (not recommended for production)",
		output,
	)
}

// messageSink is the common interface shared by all external-broker sinks.
type messageSink interface {
	router.Consumer
	SetMetrics(*observability.KaptantoMetrics)
	Ping() error
	Close()
}

// buildOutputServer wires the configured output and returns the server function
// plus the updated health probes slice (sinks append their own probe).
func buildOutputServer(
	cfg *config.Config,
	rtr *router.Router,
	cursorStore router.ConsumerCursorStore,
	metrics *observability.KaptantoMetrics,
	healthHandler http.Handler,
	healthProbes []observability.HealthProbe,
	rowFilters map[string]*output.RowFilter,
	colFilters map[string][]string,
) (func(context.Context) error, error) {
	// Startup auth policy: refuse network outputs without an auth token unless
	// --insecure is explicitly set. This covers both data-plane outputs (sse,
	// grpc, webhook) and broker sink outputs (nats, sqs, kafka, pubsub, rabbitmq)
	// whose observability endpoints (/metrics, /healthz) would otherwise leak
	// topology information to unauthenticated callers.
	networkOutputs := map[string]bool{
		"sse": true, "grpc": true, "webhook": true,
		"nats": true, "sqs": true, "kafka": true, "pubsub": true, "rabbitmq": true,
		"vector": true,
	}
	requiresAuth := networkOutputs[cfg.Output] || (cfg.Output == "none" && cfg.MCP.Enabled)
	if requiresAuth && cfg.AuthToken == "" {
		if !cfg.Insecure {
			return nil, fmt.Errorf(
				"output %q requires --auth-token (or KAPTANTO_AUTH_TOKEN env var) to protect the data stream; "+
					"use --insecure to disable this check (not recommended for production)",
				cfg.Output,
			)
		}
		slog.Warn("SECURITY WARNING: running without authentication — the change stream is accessible to any client that can reach the port",
			"output", cfg.Output,
			"port", cfg.Port,
		)
	}

	// Short-circuit stdout before generating OpenAPI: it exposes no network
	// endpoints, so a marshal failure should not prevent it from starting.
	if cfg.Output == "stdout" {
		w := stdout.NewStdoutWriter(os.Stdout)
		w.SetMetrics(metrics)
		rtr.Register(w)
		return func(ctx context.Context) error { <-ctx.Done(); return nil }, nil
	}

	// Generate the OpenAPI spec once at startup (OAS-01: deterministic, cached).
	oaOpts := openapi.NewGenerateOptions(cfg)
	oaDoc := openapi.Generate(oaOpts)
	oaBytes, err := openapi.MarshalDocument(oaDoc)
	if err != nil {
		return nil, fmt.Errorf("openapi: marshal: %w", err)
	}
	oaHandler := openapi.NewHandler(oaBytes)

	switch cfg.Output {
	case "none":
		return buildObservabilityServer(cfg, metrics, healthHandler, healthProbes, oaHandler)
	case "sse":
		return buildSSEServer(cfg, rtr, metrics, healthHandler, oaHandler, rowFilters, colFilters)
	case "grpc":
		return buildGRPCServer(cfg, rtr, cursorStore, metrics, healthHandler, oaHandler, rowFilters, colFilters, nil)
	case "nats", "sqs", "kafka", "pubsub", "rabbitmq", "webhook", "vector":
		return buildBrokerOutputServer(cfg, rtr, metrics, healthProbes, oaHandler)
	default:
		return nil, fmt.Errorf("unknown output mode %q: valid modes are none, stdout, sse, grpc, nats, sqs, kafka, pubsub, rabbitmq, webhook, vector", cfg.Output)
	}
}

// buildBrokerOutputServer constructs the external-broker sinks (nats, sqs,
// kafka, pubsub, rabbitmq, webhook, vector) and their shared observability server.
func buildBrokerOutputServer(
	cfg *config.Config,
	rtr *router.Router,
	metrics *observability.KaptantoMetrics,
	healthProbes []observability.HealthProbe,
	oaHandler http.Handler,
) (func(context.Context) error, error) {
	switch cfg.Output {
	case "nats":
		if cfg.Sinks.NATS == nil {
			return nil, fmt.Errorf("--output nats requires a sinks.nats block in config (url, subject-template)")
		}
		sink, err := natssink.NewNATSSinkConsumer("nats", *cfg.Sinks.NATS)
		if err != nil {
			return nil, fmt.Errorf("nats sink: init: %w", err)
		}
		return buildSinkServer(cfg, "nats", sink, rtr, metrics, healthProbes, oaHandler, nil)
	case "sqs":
		if cfg.Sinks.SQS == nil {
			return nil, fmt.Errorf("--output sqs requires a sinks.sqs block in config (queue-url, region)")
		}
		sink, err := sqssink.NewSQSSinkConsumer("sqs", *cfg.Sinks.SQS)
		if err != nil {
			return nil, fmt.Errorf("sqs sink: init: %w", err)
		}
		return buildSinkServer(cfg, "sqs", sink, rtr, metrics, healthProbes, oaHandler, nil)
	case "kafka":
		if cfg.Sinks.Kafka == nil {
			return nil, fmt.Errorf("--output kafka requires a sinks.kafka block in config (bootstrap-servers, topic-template)")
		}
		sink, err := kafkasink.NewKafkaSinkConsumer("kafka", *cfg.Sinks.Kafka)
		if err != nil {
			return nil, fmt.Errorf("kafka sink: init: %w", err)
		}
		return buildSinkServer(cfg, "kafka", sink, rtr, metrics, healthProbes, oaHandler, nil)
	case "pubsub":
		if cfg.Sinks.PubSub == nil {
			return nil, fmt.Errorf("--output pubsub requires a sinks.pubsub block in config (project-id, topic-id)")
		}
		sink, err := pubsubsink.NewPubSubSinkConsumer("pubsub", *cfg.Sinks.PubSub)
		if err != nil {
			return nil, fmt.Errorf("pubsub sink: init: %w", err)
		}
		return buildSinkServer(cfg, "pubsub", sink, rtr, metrics, healthProbes, oaHandler, nil)
	case "rabbitmq":
		if cfg.Sinks.RabbitMQ == nil {
			return nil, fmt.Errorf("--output rabbitmq requires a sinks.rabbitmq block in config (url, exchange)")
		}
		sink, err := rabbitmqsink.NewRabbitMQSinkConsumer("rabbitmq", *cfg.Sinks.RabbitMQ)
		if err != nil {
			return nil, fmt.Errorf("rabbitmq sink: init: %w", err)
		}
		return buildSinkServer(cfg, "rabbitmq", sink, rtr, metrics, healthProbes, oaHandler, nil)
	case "webhook":
		if cfg.Sinks.Webhook == nil {
			return nil, fmt.Errorf("--output webhook requires a sinks.webhook block in config (url)")
		}
		sink, err := webhooksink.NewWebhookSinkConsumer("webhook", *cfg.Sinks.Webhook)
		if err != nil {
			return nil, fmt.Errorf("webhook sink: init: %w", err)
		}
		return buildSinkServer(cfg, "webhook", sink, rtr, metrics, healthProbes, oaHandler, nil)
	case "vector":
		if cfg.Sinks.Vector == nil {
			return nil, fmt.Errorf("--output vector requires a sinks.vector block in config (source, embedder, store)")
		}
		sink, err := vectorsink.NewVectorSinkConsumer("vector", *cfg.Sinks.Vector, cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("vector sink: init: %w", err)
		}
		return buildSinkServer(cfg, "vector", sink, rtr, metrics, healthProbes, oaHandler, nil)
	default:
		return nil, fmt.Errorf("unknown broker output mode %q", cfg.Output)
	}
}

func buildSSEServer(
	cfg *config.Config,
	rtr *router.Router,
	metrics *observability.KaptantoMetrics,
	healthHandler http.Handler,
	oaHandler http.Handler,
	rowFilters map[string]*output.RowFilter,
	colFilters map[string][]string,
) (func(context.Context) error, error) {
	tlsCfg, err := buildServerTLSConfig(cfg.ServerTLS)
	if err != nil {
		return nil, err
	}
	if err := requireServerTLS("sse", tlsCfg, cfg.Insecure); err != nil {
		return nil, err
	}
	var consumerScope string
	if cfg.AuthToken != "" && !cfg.Insecure {
		consumerScope = auth.ConsumerScopeID(cfg.AuthToken)
	}
	sseServer := sse.NewSSEServer(rtr, metrics, cfg.CORSOrigin, 15*time.Second, rowFilters, colFilters, consumerScope)
	mux := http.NewServeMux()
	if cfg.AuthToken != "" {
		mux.Handle("/events", auth.Middleware(cfg.AuthToken, sseServer))
		mux.Handle("/metrics", auth.Middleware(cfg.AuthToken, metrics.Handler()))
		mux.Handle("/healthz", auth.Middleware(cfg.AuthToken, healthHandler))
		mux.Handle("/openapi.json", auth.Middleware(cfg.AuthToken, oaHandler))
	} else {
		mux.Handle("/events", sseServer)
		mux.Handle("/metrics", metrics.Handler())
		mux.Handle("/healthz", healthHandler)
		mux.Handle("/openapi.json", oaHandler)
	}
	srv := newHTTPServer(fmt.Sprintf(":%d", cfg.Port), mux)
	if tlsCfg != nil {
		srv.TLSConfig = tlsCfg
	}
	return func(ctx context.Context) error {
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		if tlsCfg != nil {
			if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("sse server: %w", err)
			}
		} else {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("sse server: %w", err)
			}
		}
		return nil
	}, nil
}

func buildGRPCServer(
	cfg *config.Config,
	rtr *router.Router,
	cursorStore router.ConsumerCursorStore,
	metrics *observability.KaptantoMetrics,
	healthHandler http.Handler,
	oaHandler http.Handler,
	rowFilters map[string]*output.RowFilter,
	colFilters map[string][]string,
	obsLis net.Listener,
) (func(context.Context) error, error) {
	tlsCfg, err := buildServerTLSConfig(cfg.ServerTLS)
	if err != nil {
		return nil, err
	}
	if err := requireServerTLS("grpc", tlsCfg, cfg.Insecure); err != nil {
		return nil, err
	}
	var consumerScope string
	if cfg.AuthToken != "" && !cfg.Insecure {
		consumerScope = auth.ConsumerScopeID(cfg.AuthToken)
	}
	grpcSvc := grpcoutput.NewGRPCServer(rtr, cursorStore, metrics, rowFilters, colFilters, consumerScope)
	var grpcSrv interface {
		Serve(net.Listener) error
		GracefulStop()
	}
	if cfg.AuthToken != "" {
		grpcSrv = grpcoutput.NewGRPCNetServerWithAuth(grpcSvc, cfg.AuthToken, tlsCfg)
	} else {
		grpcSrv = grpcoutput.NewGRPCNetServer(grpcSvc, tlsCfg)
	}
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("grpc listen: %w", err)
	}
	obsMux := http.NewServeMux()
	if cfg.AuthToken != "" {
		obsMux.Handle("/metrics", auth.Middleware(cfg.AuthToken, metrics.Handler()))
		obsMux.Handle("/healthz", auth.Middleware(cfg.AuthToken, healthHandler))
		obsMux.Handle("/openapi.json", auth.Middleware(cfg.AuthToken, oaHandler))
	} else {
		obsMux.Handle("/metrics", metrics.Handler())
		obsMux.Handle("/healthz", healthHandler)
		obsMux.Handle("/openapi.json", oaHandler)
	}
	obsSrv := newHTTPServer(fmt.Sprintf(":%d", cfg.Port+1), obsMux)
	if tlsCfg != nil {
		obsSrv.TLSConfig = tlsCfg
	}
	return func(ctx context.Context) error {
		go func() {
			<-ctx.Done()
			grpcSrv.GracefulStop()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = obsSrv.Shutdown(shutdownCtx)
		}()
		go func() {
			if obsLis == nil {
				var err error
				obsLis, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port+1))
				if err != nil {
					slog.Error("grpc observability server: listen failed", "port", cfg.Port+1, "error", err)
					return
				}
			}
			if tlsCfg != nil {
				if err := obsSrv.ServeTLS(obsLis, "", ""); err != nil && err != http.ErrServerClosed {
					slog.Error("grpc observability server: serve TLS failed", "error", err)
				}
			} else {
				if err := obsSrv.Serve(obsLis); err != nil && err != http.ErrServerClosed {
					slog.Error("grpc observability server: serve failed", "error", err)
				}
			}
		}()
		if err := grpcSrv.Serve(lis); err != nil {
			return fmt.Errorf("grpc server: %w", err)
		}
		return nil
	}, nil
}

// buildObservabilityServer runs only the observability endpoints
// (/metrics, /healthz, /openapi.json) for output modes that have no data-plane
// server of their own. It applies the same TLS policy as buildSinkServer.
func buildObservabilityServer(
	cfg *config.Config,
	metrics *observability.KaptantoMetrics,
	healthHandler http.Handler,
	healthProbes []observability.HealthProbe,
	oaHandler http.Handler,
) (func(context.Context) error, error) {
	tlsCfg, err := buildServerTLSConfig(cfg.ServerTLS)
	if err != nil {
		return nil, err
	}
	if err := requireServerTLS("none", tlsCfg, cfg.Insecure); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	var metricsH, healthH, oaH http.Handler
	metricsH = metrics.Handler()
	healthH = observability.NewHealthHandler(healthProbes)
	oaH = oaHandler
	if cfg.AuthToken != "" {
		metricsH = auth.Middleware(cfg.AuthToken, metricsH)
		healthH = auth.Middleware(cfg.AuthToken, healthH)
		oaH = auth.Middleware(cfg.AuthToken, oaH)
	}
	mux.Handle("/metrics", metricsH)
	mux.Handle("/healthz", healthH)
	mux.Handle("/openapi.json", oaH)
	srv := newHTTPServer(fmt.Sprintf(":%d", cfg.Port), mux)
	if tlsCfg != nil {
		srv.TLSConfig = tlsCfg
	}
	return func(ctx context.Context) error {
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		if tlsCfg != nil {
			if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("none obs server: %w", err)
			}
		} else {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("none obs server: %w", err)
			}
		}
		return nil
	}, nil
}

// buildSinkServer registers an external-broker sink, appends its health probe,
// and returns a server function that runs an observability HTTP endpoint.
// The observability server follows the same TLS policy as the SSE/gRPC data
// planes: TLS is required unless --insecure is explicitly set.
func buildSinkServer(
	cfg *config.Config,
	name string,
	sink messageSink,
	rtr *router.Router,
	metrics *observability.KaptantoMetrics,
	healthProbes []observability.HealthProbe,
	oaHandler http.Handler,
	lis net.Listener,
) (func(context.Context) error, error) {
	tlsCfg, err := buildServerTLSConfig(cfg.ServerTLS)
	if err != nil {
		return nil, err
	}
	if err := requireServerTLS(name, tlsCfg, cfg.Insecure); err != nil {
		return nil, err
	}

	sink.SetMetrics(metrics)
	rtr.Register(sink)
	probes := append(healthProbes, observability.HealthProbe{Name: name, Check: sink.Ping})
	mux := http.NewServeMux()
	var metricsH, healthH, oaH http.Handler
	metricsH = metrics.Handler()
	healthH = observability.NewHealthHandler(probes)
	oaH = oaHandler
	if cfg.AuthToken != "" {
		metricsH = auth.Middleware(cfg.AuthToken, metricsH)
		healthH = auth.Middleware(cfg.AuthToken, healthH)
		oaH = auth.Middleware(cfg.AuthToken, oaH)
	}
	mux.Handle("/metrics", metricsH)
	mux.Handle("/healthz", healthH)
	mux.Handle("/openapi.json", oaH)
	srv := newHTTPServer(fmt.Sprintf(":%d", cfg.Port), mux)
	if tlsCfg != nil {
		srv.TLSConfig = tlsCfg
	}
	return func(ctx context.Context) error {
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		defer sink.Close()
		if lis == nil {
			var err error
			lis, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
			if err != nil {
				return fmt.Errorf("%s obs server: listen: %w", name, err)
			}
		}
		if tlsCfg != nil {
			if err := srv.ServeTLS(lis, "", ""); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("%s obs server: %w", name, err)
			}
		} else {
			if err := srv.Serve(lis); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("%s obs server: %w", name, err)
			}
		}
		return nil
	}, nil
}
