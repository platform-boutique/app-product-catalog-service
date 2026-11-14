//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"product-catalog-service/internal/catalog"
	pb "product-catalog-service/proto"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// OBS-10 / OBS-20 against a real Postgres: InitDB makes every query a CLIENT span (a child of the
// span of the caller) and registers the db_pool_* metrics of the pool.
func TestInitDB_TracesQueriesAndExportsPoolMetrics(t *testing.T) {
	dsn := newDB(t)
	p := newPool(t, dsn)
	if !migrateAndSeed(t, p, fixture(t)) {
		t.Fatal("seed not applied")
	}

	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Setenv("DATABASE_URL", dsn)
	if err := catalog.InitDB(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, root := otel.Tracer("test").Start(context.Background(), "ListProducts")
	var resp pb.ListProductsResponse
	if err := catalog.LoadCatalog(ctx, &resp); err != nil {
		t.Fatal(err)
	}
	root.End()

	var direct bool
	for _, s := range rec.Ended() {
		// Only the spans of this request: the ping of InitDB and the seed also run queries, in other traces.
		if s.SpanContext().TraceID() != root.SpanContext().TraceID() || s.SpanKind() != trace.SpanKindClient || !strings.HasPrefix(s.Name(), "SELECT") {
			continue
		}
		// pgx prepares the statement under the query span, so one SELECT is a direct child and
		// another sits below it; both are in the request's trace.
		direct = direct || s.Parent().SpanID() == root.SpanContext().SpanID()
		for _, kv := range s.Attributes() {
			if strings.Contains(kv.Value.String(), "FIXTURE-") {
				t.Errorf("a product value reached the span attribute %s", kv.Key)
			}
		}
	}
	if !direct {
		var names []string
		for _, s := range rec.Ended() {
			names = append(names, s.Name())
		}
		t.Fatalf("no CLIENT span SELECT that is a child of the request span, spans: %v", names)
	}

	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, mf := range mfs {
		have[mf.GetName()] = true
	}
	for _, want := range []string{"db_pool_connections", "db_pool_connections_max", "db_pool_acquires_total"} {
		if !have[want] {
			t.Errorf("metric %s is not exported", want)
		}
	}
}
