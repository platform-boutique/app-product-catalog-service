package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
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

// drainTimeout caps how long a SIGTERM waits for in-flight RPCs before the connections are closed.
const drainTimeout = 10 * time.Second

func main() {
	if err := realMain(); err != nil {
		log.Fatalf("server error: %v", err)
	}
	// Normal return = exit code 0 (SIGTERM/SIGINT drained cleanly).
}

func realMain() error {
	// --- Telemetry: traces, metrics and logs pushed over OTLP/gRPC (Alloy → Tempo/Mimir/Loki) ---
	// On only if OTEL_EXPORTER_OTLP_ENDPOINT is set; OTEL_SDK_DISABLED=true forces it off.
	shutdownTelemetry, err := initTelemetry(context.Background(), "product-catalog-service", log, catalog.Logger())
	switch {
	case errors.Is(err, errTelemetryOff):
		log.Info("telemetry off — set OTEL_EXPORTER_OTLP_ENDPOINT to enable (OTEL_SDK_DISABLED=true forces it off)")
		if os.Getenv("COLLECTOR_SERVICE_ADDR") != "" {
			log.Warn("COLLECTOR_SERVICE_ADDR is ignored: set OTEL_EXPORTER_OTLP_ENDPOINT (with scheme) to enable telemetry")
		}
	case err != nil:
		log.Warnf("telemetry disabled, continuing without it: %v", err)
	default:
		log.Info("telemetry enabled (OTLP/gRPC)")
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownTelemetry(ctx)
		}()
	}

	// --- Profiling: Pyroscope push + pprof, only with PYROSCOPE_ADDR set (ENABLE_PROFILING=0 forces it off) ---
	pyroscopeAddr, profiling := profilingConfig()
	if profiling {
		initProfiling(pyroscopeAddr)
	} else if os.Getenv("ENABLE_PROFILING") == "1" {
		log.Info("profiling off — ENABLE_PROFILING=1 alone does nothing, set PYROSCOPE_ADDR to enable")
	} else {
		log.Info("profiling off — set PYROSCOPE_ADDR to enable")
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

	// --- Hot-reload toggle via Unix signals (the flag is atomic: handlers read it concurrently) ---
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Infof("starting grpc server on :%s", port)
	return run(ctx, port, extraLatency, &reloadCatalog, profiling)
}

// profilingConfig returns the Pyroscope address and whether profiling is on: only when
// PYROSCOPE_ADDR is set and non-empty and ENABLE_PROFILING is not "0". (Before, ENABLE_PROFILING=1
// alone started profiling against the default http://pyroscope:4040.)
func profilingConfig() (string, bool) {
	addr := strings.TrimSpace(os.Getenv("PYROSCOPE_ADDR"))
	if addr == "" || os.Getenv("ENABLE_PROFILING") == "0" {
		return "", false
	}
	return addr, true
}

func run(ctx context.Context, port string, extraLatency time.Duration, reloadFlag *atomic.Bool, profiling bool) error {
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

	svc, err := catalog.NewProductCatalog(ctx, extraLatency, reloadFlag, data.Seed())
	if err != nil {
		_ = listener.Close()
		if ctx.Err() != nil {
			log.Info("shutdown signal received during start-up, exiting")
			return nil
		}
		return fmt.Errorf("failed to init product catalog: %w", err)
	}

	pb.RegisterProductCatalogServiceServer(srv, svc)

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(srv, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	reflection.Register(srv)

	grpcprom.Register(srv)
	grpcprom.EnableHandlingTimeHistogram()

	// --- Metrics (+ pprof only when profiling is on) on a dedicated HTTP server with timeouts ---
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	// G108 fix: only register pprof handlers when profiling is explicitly enabled
	if profiling {
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
	go func() {
		log.Infof("metrics endpoint on :%s", metricsPort)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warnf("metrics server error: %v", err)
		}
	}()

	log.Infof("listening on %s", listener.Addr().String())
	return serve(ctx, srv, listener, healthSrv, metricsSrv, drainTimeout)
}

// serve runs srv until it fails or ctx is cancelled (SIGTERM/SIGINT). On cancel it marks every
// health service NOT_SERVING, stops accepting new RPCs, waits up to drain for in-flight ones
// (GracefulStop) and then closes what is left (Stop). It returns nil after a requested shutdown,
// so the process exits 0.
func serve(ctx context.Context, srv *grpc.Server, l net.Listener, h *health.Server, metrics *http.Server, drain time.Duration) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(l) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Info("shutdown signal received, draining")
	h.Shutdown() // NOT_SERVING for every service, so probes fail first
	done := make(chan struct{})
	go func() { srv.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(drain):
		log.Warnf("graceful stop exceeded %v, closing remaining connections", drain)
		srv.Stop()
		<-done
	}
	if metrics != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metrics.Shutdown(shutdownCtx)
	}
	log.Info("shutdown complete")
	return nil
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

// initProfiling starts the Pyroscope push-based profiler against pyroscopeAddr.
func initProfiling(pyroscopeAddr string) {
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
