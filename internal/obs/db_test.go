package obs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func gatherMap(t *testing.T, reg *prometheus.Registry) map[string]*dto.MetricFamily {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*dto.MetricFamily{}
	for _, mf := range mfs {
		m[mf.GetName()] = mf
	}
	return m
}

func valueOf(t *testing.T, m map[string]*dto.MetricFamily, name string, labels map[string]string) float64 {
	t.Helper()
	mf, ok := m[name]
	if !ok {
		t.Fatalf("metric %s missing", name)
	}
next:
	for _, mt := range mf.GetMetric() {
		for k, v := range labels {
			found := false
			for _, l := range mt.GetLabel() {
				if l.GetName() == k && l.GetValue() == v {
					found = true
				}
			}
			if !found {
				continue next
			}
		}
		if mt.GetGauge() != nil {
			return mt.GetGauge().GetValue()
		}
		return mt.GetCounter().GetValue()
	}
	t.Fatalf("metric %s with labels %v missing", name, labels)
	return 0
}

func TestPoolCollectorExportsTheStat(t *testing.T) {
	reg := prometheus.NewRegistry()
	st := PoolStat{Acquired: 3, Idle: 2, Constructing: 1, Max: 10, AcquireCount: 100, EmptyAcquireCount: 7,
		CanceledAcquireCount: 2, AcquireDuration: 1500 * time.Millisecond, NewConns: 6, MaxLifetimeDestroy: 4, MaxIdleDestroy: 5}
	reg.MustRegister(NewPoolCollector(func() PoolStat { return st }))
	m := gatherMap(t, reg)
	for _, c := range []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"db_pool_connections", map[string]string{"state": "acquired"}, 3},
		{"db_pool_connections", map[string]string{"state": "idle"}, 2},
		{"db_pool_connections", map[string]string{"state": "constructing"}, 1},
		{"db_pool_connections_max", nil, 10},
		{"db_pool_acquires_total", nil, 100},
		{"db_pool_empty_acquires_total", nil, 7},
		{"db_pool_canceled_acquires_total", nil, 2},
		{"db_pool_acquire_seconds_total", nil, 1.5},
		{"db_pool_new_connections_total", nil, 6},
		{"db_pool_destroyed_connections_total", map[string]string{"reason": "max_lifetime"}, 4},
		{"db_pool_destroyed_connections_total", map[string]string{"reason": "max_idle"}, 5},
	} {
		if got := valueOf(t, m, c.name, c.labels); got != c.want {
			t.Errorf("%s%v = %v, want %v", c.name, c.labels, got, c.want)
		}
	}
	// the values are read per scrape, not at registration
	st.Acquired = 9
	if got := valueOf(t, gatherMap(t, reg), "db_pool_connections", map[string]string{"state": "acquired"}); got != 9 {
		t.Errorf("second scrape acquired = %v, want 9", got)
	}
}

func TestRegisterPoolMetricsOnARealPool(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/db?sslmode=disable&pool_max_conns=7")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg) // lazy: no connection yet
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	reg := prometheus.NewRegistry()
	if err := RegisterPoolMetrics(reg, pool); err != nil {
		t.Fatal(err)
	}
	if got := valueOf(t, gatherMap(t, reg), "db_pool_connections_max", nil); got != 7 {
		t.Errorf("db_pool_connections_max = %v, want 7", got)
	}
}

// A failed connect is the only database work that needs no server: it must be a span with the error on it, with a
// low-cardinality name and without the password.
func TestInstrumentPoolTracesTheConnect(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	cfg, err := pgxpool.ParseConfig("postgres://app:s3cret-pw@127.0.0.1:1/db?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	InstrumentPool(cfg, tp)
	if cfg.ConnConfig.Tracer == nil {
		t.Fatal("no tracer on the connection config")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// database spans are children: without a recording parent (a request, a job) nothing is recorded
	ctx, parent := tp.Tracer("test").Start(ctx, "parent")
	if _, err := pool.Exec(ctx, "SELECT 1"); err == nil {
		t.Fatal("port 1 should refuse the connection")
	}
	parent.End()
	var spans []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() != "parent" {
			spans = append(spans, s)
		}
	}
	if len(spans) == 0 {
		t.Fatal("no span recorded for the failed connect")
	}
	for _, s := range spans {
		if strings.Contains(s.Name(), "s3cret") {
			t.Errorf("span name %q leaks the password", s.Name())
		}
		for _, a := range s.Attributes() {
			if strings.Contains(a.Value.String(), "s3cret") {
				t.Errorf("attribute %s leaks the password", a.Key)
			}
		}
	}
	if got := spans[0].Name(); got != "connect" {
		t.Errorf("span name = %q, want connect", got)
	}
	if spans[0].Status().Code.String() != "Error" {
		t.Errorf("status = %v, want Error", spans[0].Status().Code)
	}
}
