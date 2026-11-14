package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"product-catalog-service/internal/obs"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID  = "00f067aa0ba902b7"
)

func inSpan() context.Context {
	tid, _ := trace.TraceIDFromHex(testTraceID)
	sid, _ := trace.SpanIDFromHex(testSpanID)
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))
}

// callRPC runs the interceptor around a handler returning err and returns the log lines it wrote.
func callRPC(t *testing.T, ctx context.Context, method string, err error) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	l := logrus.New()
	l.SetOutput(&buf)
	l.SetLevel(logrus.DebugLevel)
	l.SetFormatter(obs.NewFormatter("product-catalog-service", "1.2.3"))
	secret := "SECRET-PAYLOAD"
	h := func(context.Context, any) (any, error) { return "resp", err }
	if _, got := rpcLogInterceptor(l)(ctx, secret, &grpc.UnaryServerInfo{FullMethod: method}, h); got != err {
		t.Fatalf("the interceptor changed the handler's error: %v", got)
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("the request payload was logged: %s", buf.String())
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		if e := json.Unmarshal([]byte(line), &m); e != nil {
			t.Fatalf("not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func TestRPCLogOKInsideSpan(t *testing.T) {
	lines := callRPC(t, obs.WithRequestID(inSpan(), "req-7"), "/hipstershop.ProductCatalogService/ListProducts", nil)
	if len(lines) != 1 {
		t.Fatalf("want exactly one line, got %d", len(lines))
	}
	m := lines[0]
	want := map[string]any{
		"message": "rpc", "severity": "info", "method": "/hipstershop.ProductCatalogService/ListProducts", "code": "OK",
		"trace_id": testTraceID, "span_id": testSpanID, "request_id": "req-7", "service": "product-catalog-service", "version": "1.2.3",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if d, ok := m["duration_ms"].(float64); !ok || d < 0 {
		t.Errorf("duration_ms = %v", m["duration_ms"])
	}
	if _, has := m["error"]; has {
		t.Errorf("an OK line has no error field: %v", m)
	}
}

func TestRPCLogLevelsByCode(t *testing.T) {
	cases := []struct {
		code     codes.Code
		severity string
		hasError bool
	}{
		{codes.NotFound, "info", false},
		{codes.InvalidArgument, "info", false},
		{codes.Internal, "error", true},
		{codes.Unavailable, "error", true},
		{codes.Unknown, "error", true},
	}
	for _, c := range cases {
		t.Run(c.code.String(), func(t *testing.T) {
			lines := callRPC(t, inSpan(), "/hipstershop.ProductCatalogService/GetProduct", status.Error(c.code, "boom"))
			if len(lines) != 1 {
				t.Fatalf("want one line, got %d", len(lines))
			}
			m := lines[0]
			if m["severity"] != c.severity || m["code"] != c.code.String() || m["message"] != "rpc" {
				t.Errorf("line = %v", m)
			}
			if _, has := m["error"]; has != c.hasError {
				t.Errorf("error field present = %v, want %v (%v)", has, c.hasError, m)
			}
			if m["trace_id"] != testTraceID {
				t.Errorf("trace_id = %v", m["trace_id"])
			}
		})
	}
	// a plain (non-status) error is codes.Unknown
	if lines := callRPC(t, inSpan(), "/x.Y/Z", errors.New("plain")); len(lines) != 1 || lines[0]["severity"] != "error" || lines[0]["code"] != "Unknown" {
		t.Errorf("plain error line = %v", lines)
	}
}

func TestRPCLogSkipsHealthAndReflection(t *testing.T) {
	for _, m := range []string{
		"/grpc.health.v1.Health/Check",
		"/grpc.health.v1.Health/Watch",
		"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
		"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo",
	} {
		if lines := callRPC(t, inSpan(), m, nil); len(lines) != 0 {
			t.Errorf("%s was logged: %v", m, lines)
		}
	}
	// the skip is by prefix of the service, not by substring
	if lines := callRPC(t, inSpan(), "/hipstershop.ProductCatalogService/Check", nil); len(lines) != 1 {
		t.Errorf("a catalog method called Check must be logged, got %d lines", len(lines))
	}
}
