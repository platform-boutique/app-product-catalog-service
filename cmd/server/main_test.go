package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func clearTelemetryEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "COLLECTOR_SERVICE_ADDR", "OTEL_SDK_DISABLED", "ENABLE_TRACING", "PYROSCOPE_ADDR", "ENABLE_PROFILING"} {
		t.Setenv(k, "")
	}
}

func TestTelemetryOffByDefault(t *testing.T) {
	clearTelemetryEnv(t)
	if ep := otlpEndpoint(); ep != "" {
		t.Fatalf("endpoint %q with everything unset", ep)
	}
	shutdown, err := initTelemetry(context.Background(), "x", log)
	if !errors.Is(err, errTelemetryOff) {
		t.Fatalf("initTelemetry err = %v, want errTelemetryOff", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown: %v", err)
	}
	if _, on := profilingConfig(); on {
		t.Fatal("profiling on with everything unset")
	}
}

func TestTelemetrySwitches(t *testing.T) {
	const ep = "http://alloy:4317"
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"endpoint", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep}, ep},
		{"blank endpoint", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "  "}, ""},
		{"legacy addr alone does not switch it on", map[string]string{"COLLECTOR_SERVICE_ADDR": "alloy:4317"}, ""},
		{"legacy addr does not replace the endpoint", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "COLLECTOR_SERVICE_ADDR": "other:4317"}, ep},
		{"sdk disabled", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "OTEL_SDK_DISABLED": "true"}, ""},
		{"sdk disabled upper", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "OTEL_SDK_DISABLED": "TRUE"}, ""},
		{"sdk disabled false", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "OTEL_SDK_DISABLED": "false"}, ep},
		{"enable_tracing 0", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep, "ENABLE_TRACING": "0"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearTelemetryEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if got := otlpEndpoint(); got != c.want {
				t.Fatalf("otlpEndpoint() = %q, want %q", got, c.want)
			}
			_, err := initTelemetry(context.Background(), "x", log)
			if (c.want == "") != errors.Is(err, errTelemetryOff) {
				t.Fatalf("initTelemetry err = %v for endpoint %q", err, c.want)
			}
		})
	}
}

func TestProfilingConfig(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		wantOn bool
	}{
		{"nothing", nil, false},
		{"enable alone", map[string]string{"ENABLE_PROFILING": "1"}, false},
		{"addr", map[string]string{"PYROSCOPE_ADDR": "http://alloy:4040"}, true},
		{"addr, profiling 0", map[string]string{"PYROSCOPE_ADDR": "http://alloy:4040", "ENABLE_PROFILING": "0"}, false},
		{"blank addr", map[string]string{"PYROSCOPE_ADDR": " "}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearTelemetryEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if _, on := profilingConfig(); on != c.wantOn {
				t.Fatalf("profiling on = %v, want %v", on, c.wantOn)
			}
		})
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
	go func() { res <- serve(ctx, srv, l, h, nil, drain) }()
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
