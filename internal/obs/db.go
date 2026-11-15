package obs

import (
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
)

// InstrumentPool makes every query, batch and connect of the pool a CLIENT span (OBS-10). Call it on the parsed
// config before pgxpool.NewWithConfig and after Start, so the tracer provider Start installed is the one used
// (tp == nil means the global provider; without telemetry that is a no-op provider and costs nothing).
//
// Span names are the SQL operation ("SELECT", "INSERT"), never the statement text; query parameters are not recorded
// (they can hold emails or names); the statement text is an attribute (placeholders only). Acquire spans are off:
// the wait for a connection is visible in the pool metrics (RegisterPoolMetrics).
func InstrumentPool(cfg *pgxpool.Config, tp trace.TracerProvider) {
	opts := []otelpgx.Option{otelpgx.WithDisableAcquireTracer()}
	if tp != nil {
		opts = append(opts, otelpgx.WithTracerProvider(tp))
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer(opts...)
}

// PoolStat is the part of pgxpool.Stat that is exported as metrics.
type PoolStat struct {
	Acquired, Idle, Constructing, Max int32
	AcquireCount                      int64
	EmptyAcquireCount                 int64
	CanceledAcquireCount              int64
	AcquireDuration                   time.Duration
	NewConns                          int64
	MaxLifetimeDestroy                int64
	MaxIdleDestroy                    int64
}

// PoolStatOf reads the statistics of a pool.
func PoolStatOf(p *pgxpool.Pool) PoolStat {
	s := p.Stat()
	return PoolStat{
		Acquired:             s.AcquiredConns(),
		Idle:                 s.IdleConns(),
		Constructing:         s.ConstructingConns(),
		Max:                  s.MaxConns(),
		AcquireCount:         s.AcquireCount(),
		EmptyAcquireCount:    s.EmptyAcquireCount(),
		CanceledAcquireCount: s.CanceledAcquireCount(),
		AcquireDuration:      s.AcquireDuration(),
		NewConns:             s.NewConnsCount(),
		MaxLifetimeDestroy:   s.MaxLifetimeDestroyCount(),
		MaxIdleDestroy:       s.MaxIdleDestroyCount(),
	}
}

var (
	descConns = prometheus.NewDesc("db_pool_connections",
		"Connections of the database pool by state (acquired = in use, idle, constructing = being opened).", []string{"state"}, nil)
	descMax = prometheus.NewDesc("db_pool_connections_max",
		"Configured maximum number of connections of the database pool.", nil, nil)
	descAcquires = prometheus.NewDesc("db_pool_acquires_total",
		"Connection acquisitions since start.", nil, nil)
	descEmpty = prometheus.NewDesc("db_pool_empty_acquires_total",
		"Acquisitions that had to wait because no idle connection was available (pool pressure).", nil, nil)
	descCanceled = prometheus.NewDesc("db_pool_canceled_acquires_total",
		"Acquisitions abandoned because the caller's context ended while waiting.", nil, nil)
	descAcquireSeconds = prometheus.NewDesc("db_pool_acquire_seconds_total",
		"Total time spent waiting to acquire a connection; divide its rate by the rate of db_pool_acquires_total.", nil, nil)
	descNew = prometheus.NewDesc("db_pool_new_connections_total",
		"Connections opened since start.", nil, nil)
	descDestroyed = prometheus.NewDesc("db_pool_destroyed_connections_total",
		"Connections closed by the pool because of its limits.", []string{"reason"}, nil)
)

type poolCollector struct{ stat func() PoolStat }

// NewPoolCollector exports the pool statistics (OBS-20, DB pool stats) as db_pool_* metrics. The values are read
// when /metrics is scraped; stat is called once per scrape.
func NewPoolCollector(stat func() PoolStat) prometheus.Collector { return poolCollector{stat} }

func (poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descConns, descMax, descAcquires, descEmpty, descCanceled, descAcquireSeconds, descNew, descDestroyed} {
		ch <- d
	}
}

func (c poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stat()
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	counter := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, labels...)
	}
	gauge(descConns, float64(s.Acquired), "acquired")
	gauge(descConns, float64(s.Idle), "idle")
	gauge(descConns, float64(s.Constructing), "constructing")
	gauge(descMax, float64(s.Max))
	counter(descAcquires, float64(s.AcquireCount))
	counter(descEmpty, float64(s.EmptyAcquireCount))
	counter(descCanceled, float64(s.CanceledAcquireCount))
	counter(descAcquireSeconds, s.AcquireDuration.Seconds())
	counter(descNew, float64(s.NewConns))
	counter(descDestroyed, float64(s.MaxLifetimeDestroy), "max_lifetime")
	counter(descDestroyed, float64(s.MaxIdleDestroy), "max_idle")
}

// RegisterPoolMetrics registers the db_pool_* metrics of pool on reg.
func RegisterPoolMetrics(reg prometheus.Registerer, pool *pgxpool.Pool) error {
	return reg.Register(NewPoolCollector(func() PoolStat { return PoolStatOf(pool) }))
}
