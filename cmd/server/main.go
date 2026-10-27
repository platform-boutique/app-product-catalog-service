package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"product-catalog-service/data"
	"product-catalog-service/internal/catalog"
	pb "product-catalog-service/proto"

	"github.com/grafana/pyroscope-go"
	grpcprom "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

var log *logrus.Logger

func init() {
	log = logrus.New()
	log.Formatter = &logrus.JSONFormatter{
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "timestamp",
			logrus.FieldKeyLevel: "severity",
			logrus.FieldKeyMsg:   "message",
		},
		TimestampFormat: time.RFC3339Nano,
	}
	log.Out = os.Stdout
}

func main() {
	// --- Telemetry: traces, metrics and logs pushed over OTLP/gRPC (Alloy → Tempo/Mimir/Loki) ---
	shutdownTelemetry, err := initTelemetry(context.Background(), "product-catalog-service", log, catalog.Logger())
	if err != nil {
		log.Warnf("telemetry disabled, continuing without it: %v", err)
	} else {
		log.Info("telemetry enabled (OTLP/gRPC)")
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownTelemetry(ctx)
		}()
	}

	// --- Profiling (Pyroscope push) ---
	if os.Getenv("ENABLE_PROFILING") == "1" {
		initProfiling()
	} else {
		log.Info("profiling disabled — set ENABLE_PROFILING=1 to enable")
	}

	// --- Extra latency injection for chaos/load testing ---
	var extraLatency time.Duration
	if s := os.Getenv("EXTRA_LATENCY"); s != "" {
		v, err := time.ParseDuration(s)
		if err != nil {
			log.Fatalf("invalid EXTRA_LATENCY %q: %v", s, err)
		}
		extraLatency = v
		log.Infof("extra latency: %v", extraLatency)
	}

	// --- Hot-reload toggle via Unix signals ---
	var reloadCatalog atomic.Bool
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range sigs {
			if sig == syscall.SIGUSR1 {
				reloadCatalog.Store(true)
				log.Info("catalog reload enabled")
			} else {
				reloadCatalog.Store(false)
				log.Info("catalog reload disabled")
			}
		}
	}()

	port := "3550"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	log.Infof("starting grpc server on :%s", port)
	if err := run(port, extraLatency, &reloadCatalog); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func run(port string, extraLatency time.Duration, reloadFlag *atomic.Bool) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", port, err)
	}

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	// gRPC server with OTel tracing + Prometheus metrics interceptors
	srv := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.UnaryInterceptor(grpcprom.UnaryServerInterceptor),
		grpc.StreamInterceptor(grpcprom.StreamServerInterceptor),
	)

	ctx := context.Background()
	svc, err := catalog.NewProductCatalog(ctx, extraLatency, reloadFlag, data.Seed())
	if err != nil {
		return fmt.Errorf("failed to init product catalog: %w", err)
	}

	pb.RegisterProductCatalogServiceServer(srv, svc)

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(srv, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	reflection.Register(srv)

	grpcprom.Register(srv)
	grpcprom.EnableHandlingTimeHistogram()

	// --- Metrics (+ optional pprof) on a dedicated HTTP server with timeouts ---
	go func() {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.Handler())

		// G108 fix: only register pprof handlers when profiling is explicitly enabled
		if os.Getenv("ENABLE_PROFILING") == "1" {
			registerPprofHandlers(metricsMux)
			log.Info("pprof endpoints registered on metrics server")
		}

		metricsPort := "9090"
		if p := os.Getenv("METRICS_PORT"); p != "" {
			metricsPort = p
		}

		// G114 fix: use http.Server with explicit timeouts instead of http.ListenAndServe
		metricsSrv := &http.Server{
			Addr:              ":" + metricsPort,
			Handler:           metricsMux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
		}

		log.Infof("metrics endpoint on :%s", metricsPort)
		if err := metricsSrv.ListenAndServe(); err != nil {
			log.Warnf("metrics server error: %v", err)
		}
	}()

	log.Infof("listening on %s", listener.Addr().String())
	return srv.Serve(listener)
}

// registerPprofHandlers adds the standard pprof handlers to the given mux.
// This avoids the blank import of net/http/pprof which unconditionally
// registers handlers on DefaultServeMux (gosec G108).
func registerPprofHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

// initProfiling starts the Pyroscope push-based profiler.
func initProfiling() {
	pyroscopeAddr := os.Getenv("PYROSCOPE_ADDR")
	if pyroscopeAddr == "" {
		pyroscopeAddr = "http://pyroscope:4040"
	}

	_, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: "product-catalog-service",
		ServerAddress:   pyroscopeAddr,
		Logger:          pyroscope.StandardLogger,
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileInuseSpace,
		},
	})
	if err != nil {
		log.Warnf("pyroscope init failed, continuing without it: %v", err)
		return
	}
	log.Info("profiling enabled → pushing to " + pyroscopeAddr)
}
