package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/olucasandrade/kaptanto/internal/auth"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/observability"
	"github.com/olucasandrade/kaptanto/internal/output"
	grpcoutput "github.com/olucasandrade/kaptanto/internal/output/grpc"
	kafkasink "github.com/olucasandrade/kaptanto/internal/output/kafka"
	natssink "github.com/olucasandrade/kaptanto/internal/output/nats"
	pubsubsink "github.com/olucasandrade/kaptanto/internal/output/pubsub"
	rabbitmqsink "github.com/olucasandrade/kaptanto/internal/output/rabbitmq"
	sqssink "github.com/olucasandrade/kaptanto/internal/output/sqs"
	"github.com/olucasandrade/kaptanto/internal/output/sse"
	"github.com/olucasandrade/kaptanto/internal/output/stdout"
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
	// --insecure is explicitly set. This prevents accidentally exposing raw row
	// data (PII, secrets, financial records) to unauthenticated callers.
	if (cfg.Output == "sse" || cfg.Output == "grpc") && cfg.AuthToken == "" {
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

	switch cfg.Output {
	case "stdout":
		w := stdout.NewStdoutWriter(os.Stdout)
		w.SetMetrics(metrics)
		rtr.Register(w)
		return func(ctx context.Context) error { <-ctx.Done(); return nil }, nil

	case "sse":
		sseServer := sse.NewSSEServer(rtr, cursorStore, metrics, cfg.CORSOrigin, 15*time.Second, rowFilters, colFilters)
		mux := http.NewServeMux()
		if cfg.AuthToken != "" {
			mux.Handle("/events", auth.Middleware(cfg.AuthToken, sseServer))
			mux.Handle("/metrics", auth.Middleware(cfg.AuthToken, metrics.Handler()))
			mux.Handle("/healthz", auth.Middleware(cfg.AuthToken, healthHandler))
		} else {
			mux.Handle("/events", sseServer)
			mux.Handle("/metrics", metrics.Handler())
			mux.Handle("/healthz", healthHandler)
		}
		srv := newHTTPServer(fmt.Sprintf(":%d", cfg.Port), mux)
		return func(ctx context.Context) error {
			go func() {
				<-ctx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("sse server: %w", err)
			}
			return nil
		}, nil

	case "grpc":
		grpcSvc := grpcoutput.NewGRPCServer(rtr, cursorStore, metrics, rowFilters, colFilters)
		var grpcSrv interface{ Serve(net.Listener) error; GracefulStop() }
		if cfg.AuthToken != "" {
			grpcSrv = grpcoutput.NewGRPCNetServerWithAuth(grpcSvc, cfg.AuthToken)
		} else {
			grpcSrv = grpcoutput.NewGRPCNetServer(grpcSvc)
		}
		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
		if err != nil {
			return nil, fmt.Errorf("grpc listen: %w", err)
		}
		obsMux := http.NewServeMux()
		if cfg.AuthToken != "" {
			obsMux.Handle("/metrics", auth.Middleware(cfg.AuthToken, metrics.Handler()))
			obsMux.Handle("/healthz", auth.Middleware(cfg.AuthToken, healthHandler))
		} else {
			obsMux.Handle("/metrics", metrics.Handler())
			obsMux.Handle("/healthz", healthHandler)
		}
		obsSrv := newHTTPServer(fmt.Sprintf(":%d", cfg.Port+1), obsMux)
		return func(ctx context.Context) error {
			go func() {
				<-ctx.Done()
				grpcSrv.GracefulStop()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = obsSrv.Shutdown(shutdownCtx)
			}()
			go func() {
				if err := obsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					// non-fatal — main gRPC server will surface real errors
					_ = err
				}
			}()
			if err := grpcSrv.Serve(lis); err != nil {
				return fmt.Errorf("grpc server: %w", err)
			}
			return nil
		}, nil

	case "nats":
		if cfg.Sinks.NATS == nil {
			return nil, fmt.Errorf("--output nats requires a sinks.nats block in config (url, subject-template)")
		}
		sink, err := natssink.NewNATSSinkConsumer("nats", *cfg.Sinks.NATS)
		if err != nil {
			return nil, fmt.Errorf("nats sink: init: %w", err)
		}
		return buildSinkServer(cfg.Port, "nats", sink, rtr, metrics, healthProbes), nil

	case "sqs":
		if cfg.Sinks.SQS == nil {
			return nil, fmt.Errorf("--output sqs requires a sinks.sqs block in config (queue-url, region)")
		}
		sink, err := sqssink.NewSQSSinkConsumer("sqs", *cfg.Sinks.SQS)
		if err != nil {
			return nil, fmt.Errorf("sqs sink: init: %w", err)
		}
		return buildSinkServer(cfg.Port, "sqs", sink, rtr, metrics, healthProbes), nil

	case "kafka":
		if cfg.Sinks.Kafka == nil {
			return nil, fmt.Errorf("--output kafka requires a sinks.kafka block in config (bootstrap-servers, topic-template)")
		}
		sink, err := kafkasink.NewKafkaSinkConsumer("kafka", *cfg.Sinks.Kafka)
		if err != nil {
			return nil, fmt.Errorf("kafka sink: init: %w", err)
		}
		return buildSinkServer(cfg.Port, "kafka", sink, rtr, metrics, healthProbes), nil

	case "pubsub":
		if cfg.Sinks.PubSub == nil {
			return nil, fmt.Errorf("--output pubsub requires a sinks.pubsub block in config (project-id, topic-id)")
		}
		sink, err := pubsubsink.NewPubSubSinkConsumer("pubsub", *cfg.Sinks.PubSub)
		if err != nil {
			return nil, fmt.Errorf("pubsub sink: init: %w", err)
		}
		return buildSinkServer(cfg.Port, "pubsub", sink, rtr, metrics, healthProbes), nil

	case "rabbitmq":
		if cfg.Sinks.RabbitMQ == nil {
			return nil, fmt.Errorf("--output rabbitmq requires a sinks.rabbitmq block in config (url, exchange)")
		}
		sink, err := rabbitmqsink.NewRabbitMQSinkConsumer("rabbitmq", *cfg.Sinks.RabbitMQ)
		if err != nil {
			return nil, fmt.Errorf("rabbitmq sink: init: %w", err)
		}
		return buildSinkServer(cfg.Port, "rabbitmq", sink, rtr, metrics, healthProbes), nil

	default:
		return nil, fmt.Errorf("unknown output mode %q: valid modes are stdout, sse, grpc, nats, sqs, kafka, pubsub, rabbitmq", cfg.Output)
	}
}

// buildSinkServer registers an external-broker sink, appends its health probe,
// and returns a server function that runs an observability HTTP endpoint.
func buildSinkServer(
	port int,
	name string,
	sink messageSink,
	rtr *router.Router,
	metrics *observability.KaptantoMetrics,
	healthProbes []observability.HealthProbe,
) func(context.Context) error {
	sink.SetMetrics(metrics)
	rtr.Register(sink)
	probes := append(healthProbes, observability.HealthProbe{Name: name, Check: sink.Ping})
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/healthz", observability.NewHealthHandler(probes))
	srv := newHTTPServer(fmt.Sprintf(":%d", port), mux)
	return func(ctx context.Context) error {
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		defer sink.Close()
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("%s obs server: %w", name, err)
		}
		return nil
	}
}
