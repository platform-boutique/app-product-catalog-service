package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"product-catalog-service/data"
	"product-catalog-service/internal/catalog"
	"product-catalog-service/internal/obs"
	pb "product-catalog-service/proto"

	grpcprom "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// log is the process logger; obs.Start gives it (and the catalog package's logger) the contract's JSON
// shape, the level from LOG_LEVEL and, with an OTLP endpoint, the OTLP log hook.
var log = logrus.New()

// drainTimeout caps how long a SIGTERM waits for in-flight RPCs before the connections are closed.
// With the telemetry flush (obs.DefaultShutdownTimeout, 4 s) the exit stays inside the 10 s of CODE-04.
const drainTimeout = 5 * time.Second

func main() {
	// Env contract (OTEL_*, PYROSCOPE_ADDR), JSON logs, OTLP pipelines, Pyroscope, build_info, /metrics on
	// :9090 (METRICS_PORT) and pprof only with PPROF_ADDR all live in internal/obs.
	o := obs.Start(context.Background(), obs.Options{
		Service: "product-catalog-service",
		Loggers: []*logrus.Logger{log, catalog.Logger()},
	})
	defer o.Recover()
	if err := realMain(o); err != nil {
		o.Fatal("server error", err)
	}
	// Normal return = exit code 0 (SIGTERM/SIGINT drained cleanly).
	o.Shutdown()
}

func realMain(o *obs.Obs) error {
	// --- Extra latency injection for chaos/load testing ---
	var extraLatency time.Duration
	if s := os.Getenv("EXTRA_LATENCY"); s != "" {
		v, err := time.ParseDuration(s)
		if err != nil {
			o.Fatal("invalid configuration", fmt.Errorf("EXTRA_LATENCY %q: %w", s, err))
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
	return run(ctx, port, extraLatency, &reloadCatalog)
}

func run(ctx context.Context, port string, extraLatency time.Duration, reloadFlag *atomic.Bool) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", port, err)
	}

	// gRPC server with OTel tracing + Prometheus metrics interceptors
	srv := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(obs.UnaryServerInterceptor(), rpcLogInterceptor(log), grpcprom.UnaryServerInterceptor),
		grpc.ChainStreamInterceptor(obs.StreamServerInterceptor(), grpcprom.StreamServerInterceptor),
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

	log.Infof("listening on %s", listener.Addr().String())
	return serve(ctx, srv, listener, healthSrv, drainTimeout)
}

// serve runs srv until it fails or ctx is cancelled (SIGTERM/SIGINT). On cancel it marks every
// health service NOT_SERVING, stops accepting new RPCs, waits up to drain for in-flight ones
// (GracefulStop) and then closes what is left (Stop). It returns nil after a requested shutdown,
// so the process exits 0.
func serve(ctx context.Context, srv *grpc.Server, l net.Listener, h *health.Server, drain time.Duration) error {
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
	log.Info("shutdown complete")
	return nil
}
