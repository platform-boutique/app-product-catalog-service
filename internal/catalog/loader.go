package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"product-catalog-service/internal/obs"
	pb "product-catalog-service/proto"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// queryTimeout bounds one catalog query (CODE-05). A variable so tests can shrink it. It is the
// earlier of this and the deadline of the context the caller passes.
var queryTimeout = 10 * time.Second

// connectTimeout is the pgx connect timeout when the DSN sets none.
const connectTimeout = 5 * time.Second

var (
	log          *logrus.Logger
	catalogMutex sync.Mutex
	pool         *pgxpool.Pool
)

func init() {
	log = logrus.New()
	log.Formatter = &logrus.JSONFormatter{}
	log.Out = os.Stdout
}

// InitDB creates the shared connection pool. Call once at startup.
func InitDB(ctx context.Context) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is not set") // main logs it as one JSON error line and exits 1
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("DATABASE_URL: %w", err) // the error never contains the password
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = connectTimeout
	}
	obs.InstrumentPool(cfg, nil) // every query and connect is a CLIENT span, child of the request span (OBS-10)
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	pingCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	if err := p.Ping(pingCtx); err != nil {
		p.Close()
		return err
	}
	if err := obs.RegisterPoolMetrics(prometheus.DefaultRegisterer, p); err != nil {
		log.WithError(err).Warn("database pool metrics not registered") // db_pool_* (OBS-20); the service runs without them
	}

	pool = p
	log.Info("connected to postgres")
	return nil
}

// Pool returns the shared connection pool (nil before InitDB).
func Pool() *pgxpool.Pool { return pool }

// LoadCatalog queries Postgres using the shared pool and populates the catalog.
// It is safe to call from multiple goroutines.
func LoadCatalog(ctx context.Context, catalog *pb.ListProductsResponse) error {
	if pool == nil {
		return fmt.Errorf("database pool not initialized")
	}
	catalogMutex.Lock()
	defer catalogMutex.Unlock()
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	rows, err := pool.Query(ctx, `
		SELECT id, name, description, picture,
		       price_usd_currency_code, price_usd_units, price_usd_nanos,
		       categories,
		       brand, COALESCE(department, ''), tags, images, thumbnail,
		       rating, review_count, stock, weight_grams
		FROM products
		ORDER BY id
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Build a fresh slice: readers may still hold the previous one.
	var products []*pb.Product
	for rows.Next() {
		p := &pb.Product{PriceUsd: &pb.Money{}}
		var categories string
		if err := rows.Scan(
			&p.Id, &p.Name, &p.Description, &p.Picture,
			&p.PriceUsd.CurrencyCode, &p.PriceUsd.Units, &p.PriceUsd.Nanos,
			&categories,
			&p.Brand, &p.Department, &p.Tags, &p.Images, &p.Thumbnail,
			&p.Rating, &p.ReviewCount, &p.Stock, &p.WeightGrams,
		); err != nil {
			return err
		}
		if categories != "" {
			p.Categories = strings.Split(strings.ToLower(categories), ",")
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	catalog.Products = products

	log.Infof("loaded %d products from postgres", len(catalog.Products))
	return nil
}

// LoadDepartments returns the departments in display order (product_count is left 0).
func LoadDepartments(ctx context.Context) ([]*pb.Category, error) {
	if pool == nil {
		return nil, fmt.Errorf("database pool not initialized")
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := pool.Query(ctx,
		`SELECT id, name, description FROM departments ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pb.Category
	for rows.Next() {
		c := &pb.Category{}
		if err := rows.Scan(&c.Id, &c.Name, &c.Description); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Logger exposes the package logger so the entrypoint can attach telemetry hooks to it.
func Logger() *logrus.Logger { return log }
