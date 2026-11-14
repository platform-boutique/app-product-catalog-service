package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"product-catalog-service/internal/catalog"
	"product-catalog-service/internal/obs"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func env(m map[string]string) obs.Getenv { return func(k string) string { return m[k] } }

// TestEnvContract is this service's view of the telemetry contract (OBS-01, OBS-04). The full matrix is in
// internal/obs/env_test.go; this table pins the variable names main relies on and the legacy ones it ignores.
func TestEnvContract(t *testing.T) {
	const ep, py = "http://alloy:4317", "http://alloy:4040"
	cases := []struct {
		name      string
		env       map[string]string
		telemetry string // expected OTLP endpoint, "" = off
		profiling string // expected Pyroscope address, "" = off
	}{
		{"nothing set", nil, "", ""},
		{"endpoint", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep}, ep, ""},
		{"blank endpoint", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "  "}, "", ""},
		{"endpoint without scheme is off, not a crash", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "alloy:4317"}, "", ""},
		{"legacy addr alone does not switch it on", map[string]string{"COLLECTOR_SERVICE_ADDR": "alloy:4317"}, "", ""},
		{"legacy addr does not replace the endpoint", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "COLLECTOR_SERVICE_ADDR": "other:4317"}, ep, ""},
		{"sdk disabled beats endpoint and pyroscope", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "PYROSCOPE_ADDR": py, "OTEL_SDK_DISABLED": "true"}, "", ""},
		{"sdk disabled, upper case and padded", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "OTEL_SDK_DISABLED": " TRUE "}, "", ""},
		{"sdk disabled false", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "OTEL_SDK_DISABLED": "false"}, ep, ""},
		{"ENABLE_TRACING=0 is no longer read", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "ENABLE_TRACING": "0"}, ep, ""},
		{"ENABLE_PROFILING=1 alone starts nothing", map[string]string{"ENABLE_PROFILING": "1"}, "", ""},
		{"pyroscope", map[string]string{"PYROSCOPE_ADDR": py}, "", py},
		{"pyroscope, ENABLE_PROFILING=0", map[string]string{"PYROSCOPE_ADDR": py, "ENABLE_PROFILING": "0"}, "", ""},
		{"blank pyroscope addr", map[string]string{"PYROSCOPE_ADDR": " "}, "", ""},
		{"pyroscope without scheme", map[string]string{"PYROSCOPE_ADDR": "alloy:4040"}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := obs.LoadSettings("product-catalog-service", env(c.env))
			if s.OTLPEndpoint != c.telemetry || s.TelemetryOn() != (c.telemetry != "") {
				t.Errorf("telemetry endpoint = %q (on=%v), want %q", s.OTLPEndpoint, s.TelemetryOn(), c.telemetry)
			}
			if s.PyroscopeAddr != c.profiling || s.ProfilingOn() != (c.profiling != "") {
				t.Errorf("profiling addr = %q (on=%v), want %q", s.PyroscopeAddr, s.ProfilingOn(), c.profiling)
			}
		})
	}
}

func TestEnvDefaultsAndResource(t *testing.T) {
	s := obs.LoadSettings("product-catalog-service", env(map[string]string{"HOSTNAME": "catalog-0"}))
	if s.Service != "product-catalog-service" || s.Environment != "local" || s.Instance != "catalog-0" || s.MetricsAddr != ":9090" || s.PprofAddr != "" {
		t.Fatalf("defaults: %+v", s)
	}
	s = obs.LoadSettings("product-catalog-service", env(map[string]string{"DEPLOYMENT_ENVIRONMENT": "dev", "METRICS_PORT": "9191", "PPROF_ADDR": "127.0.0.1:6060", "SERVICE_VERSION": "1.2.3"}))
	if s.Environment != "dev" || s.MetricsAddr != ":9191" || s.PprofAddr != "127.0.0.1:6060" || s.Version != "1.2.3" {
		t.Fatalf("overrides: %+v", s)
	}
}

// Start the kit the way main does (same loggers, no listener): the process logger and the catalog package's
// logger (seed, migrations, loader) must both write the contract's shape, not logrus' default keys.
func TestStartWiringGivesBothLoggersTheContractShape(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SDK_DISABLED", "PYROSCOPE_ADDR", "ENABLE_PROFILING", "LOG_LEVEL", "SERVICE_VERSION"} {
		t.Setenv(k, "")
	}
	var out bytes.Buffer
	x := obs.Start(context.Background(), obs.Options{
		Service: "product-catalog-service", Loggers: []*logrus.Logger{log, catalog.Logger()}, Out: &out, NoMetricsServer: true,
		Registerer: prometheus.NewRegistry(), Gatherer: prometheus.NewRegistry(),
	})
	defer x.Shutdown()
	catalog.Logger().Info("schema ready") // what migrate.go / seed.go do

	var lines []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("not JSON: %v: %s", err, ln)
		}
		lines = append(lines, m)
	}
	if lines[0]["message"] != "starting" || lines[0]["commit"] == nil {
		t.Fatalf("first line: %v", lines[0])
	}
	last := lines[len(lines)-1]
	if last["message"] != "schema ready" {
		t.Fatalf("last line: %v", last)
	}
	for _, m := range lines {
		for _, k := range []string{"timestamp", "severity", "message", "service", "version"} {
			if m[k] == nil {
				t.Errorf("line lacks %q: %v", k, m)
			}
		}
		if m["service"] != "product-catalog-service" || m["time"] != nil || m["level"] != nil || m["msg"] != nil {
			t.Errorf("wrong shape: %v", m)
		}
	}
}

// startServe runs serve() on a loopback port and returns what a test needs to talk to and stop it.
func startServe(t *testing.T, drain time.Duration) (cancel context.CancelFunc, result <-chan error, h *health.Server, conn *grpc.ClientConn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	h = health.NewServer()
	healthpb.RegisterHealthServer(srv, h)
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { res <- serve(ctx, srv, l, h, drain) }()
	conn, err = grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); cancel() })
	return cancel, res, h, conn
}

func wait(t *testing.T, res <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-res:
		return err
	case <-time.After(within):
		t.Fatalf("serve did not return within %v", within)
		return nil
	}
}

// SIGTERM (= cancelled context) with nothing in flight: serve returns nil quickly, so main exits 0.
func TestServeStopsGracefullyOnShutdown(t *testing.T) {
	cancel, res, _, conn := startServe(t, 5*time.Second)
	hc := healthpb.NewHealthClient(conn)
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if r, err := hc.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil || r.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("check before shutdown: %v %v", r, err)
	}
	start := time.Now()
	cancel()
	if err := wait(t, res, 3*time.Second); err != nil {
		t.Fatalf("serve returned %v, want nil (exit 0)", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("graceful stop took %v with nothing in flight", d)
	}
}

// An RPC that never finishes (an open health Watch stream) must not hold the process hostage: after
// the drain timeout the remaining connections are closed and serve still returns nil. The stream
// first sees NOT_SERVING, which is what makes probes fail before the stop.
func TestServeDrainTimeoutForcesStop(t *testing.T) {
	cancel, res, _, conn := startServe(t, 300*time.Millisecond)
	hc := healthpb.NewHealthClient(conn)
	wctx, wc := context.WithTimeout(context.Background(), 10*time.Second)
	defer wc()
	stream, err := hc.Watch(wctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if m, err := stream.Recv(); err != nil || m.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("first watch message: %v %v", m, err)
	}

	start := time.Now()
	cancel()
	if m, err := stream.Recv(); err != nil || m.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("watch after shutdown signal: %v %v, want NOT_SERVING", m, err)
	}
	if err := wait(t, res, 5*time.Second); err != nil {
		t.Fatalf("serve returned %v, want nil", err)
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 3*time.Second {
		t.Errorf("stop took %v, want about the 300ms drain timeout", d)
	}
}
