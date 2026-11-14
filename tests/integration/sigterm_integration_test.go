//go:build integration

package integration

// Builds the real server binary and checks the process contract against a throwaway Postgres
// (PG_ADMIN_URL, see migrate_integration_test.go): SIGTERM -> graceful stop -> exit code 0, and the
// telemetry switches (legacy COLLECTOR_SERVICE_ADDR alone, OTEL_SDK_DISABLED=true) and the exit bound with a
// collector that never answers.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	return p
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func serverBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pcs-bin-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "server")
		out, err := exec.Command("go", "build", "-o", binPath, "../../cmd/server").CombinedOutput()
		if err != nil {
			binErr = errors.New(string(out))
		}
	})
	if binErr != nil {
		t.Fatalf("go build: %v", binErr)
	}
	return binPath
}

// runServer starts the binary, waits until the gRPC port accepts connections and returns the process.
func runServer(t *testing.T, env ...string) (*exec.Cmd, *lockedBuf, <-chan error, string) {
	t.Helper()
	dsn := newDB(t)
	port, metrics := freePort(t), freePort(t)
	cmd := exec.Command(serverBinary(t))
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dsn, "PORT="+port, "METRICS_PORT="+metrics,
		"OTEL_EXPORTER_OTLP_ENDPOINT=", "OTEL_SDK_DISABLED=", "COLLECTOR_SERVICE_ADDR=", "PYROSCOPE_ADDR=", "ENABLE_PROFILING=")
	cmd.Env = append(cmd.Env, env...)
	out := &lockedBuf{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	deadline := time.Now().Add(30 * time.Second)
	conn, err := grpc.NewClient("127.0.0.1:"+port, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hc := healthpb.NewHealthClient(conn)
	for {
		// The port opens before migrations and the seed are done: wait for SERVING.
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		r, err := hc.Check(ctx, &healthpb.HealthCheckRequest{})
		cancel()
		if err == nil && r.Status == healthpb.HealthCheckResponse_SERVING {
			return cmd, out, exited, port
		}
		select {
		case err := <-exited:
			t.Fatalf("server exited early: %v\n%s", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not listen in time:\n%s", out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestServer_SIGTERMExitsZero(t *testing.T) {
	// Legacy variable alone must not switch telemetry on (the address is unreachable on purpose).
	cmd, out, exited, _ := runServer(t, "COLLECTOR_SERVICE_ADDR=127.0.0.1:1")
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v (want exit code 0)\n%s", err, out.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no exit within 20s of SIGTERM\n%s", out.String())
	}
	t.Logf("exited 0, %v after SIGTERM", time.Since(start))
	log := out.String()
	for _, want := range []string{"shutdown signal received", "shutdown complete", "telemetry disabled (OTEL_EXPORTER_OTLP_ENDPOINT is not set)"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "telemetry enabled") || strings.Contains(log, "COLLECTOR_SERVICE_ADDR") {
		t.Errorf("telemetry was switched on by COLLECTOR_SERVICE_ADDR, or the legacy variable is still mentioned:\n%s", log)
	}
}

func TestServer_OtelSdkDisabledBeatsEndpoint(t *testing.T) {
	cmd, out, exited, _ := runServer(t, "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:1", "OTEL_SDK_DISABLED=true")
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("exit: %v\n%s", err, out.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no exit after SIGTERM")
	}
	if log := out.String(); strings.Contains(log, "telemetry enabled") || !strings.Contains(log, "telemetry disabled (OTEL_SDK_DISABLED=true)") {
		t.Errorf("OTEL_SDK_DISABLED=true did not force telemetry off:\n%s", log)
	}
}

// A collector and a Pyroscope that accept the connection and never answer must not hold the process
// (CODE-04, OBS-04): start does not wait for them and SIGTERM exits 0 inside 10 s (drain 5 s + flush cut at 4 s).
func TestServer_BlackholedCollectorDoesNotBlockExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c) // never read, never answered
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})
	hole := "http://" + ln.Addr().String()
	cmd, out, exited, _ := runServer(t, "OTEL_EXPORTER_OTLP_ENDPOINT="+hole, "PYROSCOPE_ADDR="+hole)
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v (want exit code 0)\n%s", err, out.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no exit within 20s of SIGTERM\n%s", out.String())
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("exit took %v with a blackholed collector, want under 10s (CODE-04)", d)
	} else {
		t.Logf("exited 0, %v after SIGTERM with a blackholed collector", d)
	}
	if log := out.String(); !strings.Contains(log, "telemetry enabled") {
		t.Errorf("telemetry should have been enabled for this test:\n%s", log)
	}
}
