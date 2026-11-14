//go:build integration

package integration

// Migration and seed tests against a real Postgres. Each test creates its own database.
//
//	PG_ADMIN_URL=postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable \
//	  go test -tags integration ./tests/integration/
//
// PG_ADMIN_URL must point at a role that may CREATE DATABASE.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"product-catalog-service/internal/catalog"
	pb "product-catalog-service/proto"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const fixtureDir = "../../internal/catalog/testdata/seed"

// newDB creates an empty database and returns its URL; it is dropped when the test ends.
func newDB(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("PG_ADMIN_URL")
	if admin == "" {
		t.Skip("PG_ADMIN_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	name := fmt.Sprintf("pcs_test_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = conn.Close(context.Background())
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func newPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func fixture(t *testing.T) *catalog.Seed {
	t.Helper()
	s, err := catalog.ReadSeed(os.DirFS(fixtureDir))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func migrateAndSeed(t *testing.T, p *pgxpool.Pool, s *catalog.Seed) bool {
	t.Helper()
	ctx := context.Background()
	if err := catalog.Migrate(ctx, p); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	applied, err := catalog.ApplySeed(ctx, p, s)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return applied
}

func productIDs(t *testing.T, p *pgxpool.Pool) []string {
	t.Helper()
	rows, err := p.Query(context.Background(), "SELECT id FROM products ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// fingerprint captures everything a second run could change, including row versions (xmin).
func fingerprint(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	var s string
	err := p.QueryRow(context.Background(), `
		SELECT
		  (SELECT string_agg(id || ':' || xmin::text, ',' ORDER BY id) FROM products) || '|' ||
		  (SELECT string_agg(id || ':' || xmin::text, ',' ORDER BY id) FROM departments) || '|' ||
		  (SELECT string_agg(version_id::text || ':' || xmin::text, ',' ORDER BY id) FROM goose_db_version) || '|' ||
		  (SELECT sha256 || ':' || applied_at::text || ':' || xmin::text FROM catalog_seed)`).Scan(&s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func schemaVersion(t *testing.T, p *pgxpool.Pool) int64 {
	t.Helper()
	var v int64
	if err := p.QueryRow(context.Background(), "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMigrate_FreshDatabase(t *testing.T) {
	dsn := newDB(t)
	p := newPool(t, dsn)

	if !migrateAndSeed(t, p, fixture(t)) {
		t.Fatal("first seed reported not applied")
	}
	if v := schemaVersion(t, p); v != 2 {
		t.Errorf("schema version %d, want 2", v)
	}
	if got := fmt.Sprint(productIDs(t, p)); got != "[FIXTURE-DRESS FIXTURE-MUG FIXTURE-PAN]" {
		t.Errorf("products %s", got)
	}

	// The service's own loader must see the new columns.
	t.Setenv("DATABASE_URL", dsn)
	if err := catalog.InitDB(context.Background()); err != nil {
		t.Fatal(err)
	}
	var resp pb.ListProductsResponse
	if err := catalog.LoadCatalog(&resp); err != nil {
		t.Fatal(err)
	}
	var dress *pb.Product
	for _, pr := range resp.Products {
		if pr.Id == "FIXTURE-DRESS" {
			dress = pr
		}
	}
	if dress == nil {
		t.Fatal("FIXTURE-DRESS not loaded")
	}
	if dress.Brand != "Fixture Fashion" || dress.Department != "women" || dress.Rating != 4.5 || dress.Stock != 12 ||
		dress.WeightGrams != 300 || dress.ReviewCount != 0 || len(dress.Tags) != 2 || len(dress.Images) != 2 ||
		dress.Thumbnail != "/static/img/products/fixture-dress-thumb.webp" ||
		dress.Picture != "/static/img/products/fixture-dress-1.webp" ||
		dress.PriceUsd.Units != 49 || dress.PriceUsd.Nanos != 990000000 ||
		fmt.Sprint(dress.Categories) != "[women clothing womens-dresses]" {
		t.Errorf("unexpected product: %v", dress)
	}
	depts, err := catalog.LoadDepartments()
	if err != nil {
		t.Fatal(err)
	}
	if len(depts) != 2 || depts[0].Id != "women" || depts[1].Id != "kitchen" {
		t.Errorf("departments %v, want women, kitchen in file order", depts)
	}
}

func TestMigrate_LegacyDatabase(t *testing.T) {
	dsn := newDB(t)
	p := newPool(t, dsn)
	ctx := context.Background()

	// What the product-catalog-db image's init script does on an empty volume.
	dump, err := os.ReadFile("testdata/legacy_dump.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, string(dump)); err != nil {
		t.Fatalf("legacy dump: %v", err)
	}
	// A row that is not one of the 9 legacy products must survive.
	if _, err := p.Exec(ctx, `INSERT INTO products (id, name, description, picture) VALUES ('KEEPME', 'Custom', 'x', '/x.jpg')`); err != nil {
		t.Fatal(err)
	}
	if n := len(productIDs(t, p)); n != 10 {
		t.Fatalf("legacy setup has %d rows, want 10", n)
	}

	if !migrateAndSeed(t, p, fixture(t)) {
		t.Fatal("seed reported not applied")
	}
	if v := schemaVersion(t, p); v != 2 {
		t.Errorf("schema version %d, want 2", v)
	}
	if got := fmt.Sprint(productIDs(t, p)); got != "[FIXTURE-DRESS FIXTURE-MUG FIXTURE-PAN KEEPME]" {
		t.Errorf("products after migration %s", got)
	}
	var brand, dept string
	var tags []string
	if err := p.QueryRow(ctx, `SELECT brand, COALESCE(department, ''), tags FROM products WHERE id = 'KEEPME'`).Scan(&brand, &dept, &tags); err != nil {
		t.Fatal(err)
	}
	if brand != "" || dept != "" || len(tags) != 0 {
		t.Errorf("defaults for an old row: brand %q dept %q tags %v", brand, dept, tags)
	}
}

func TestMigrate_SecondRunIsNoOp(t *testing.T) {
	p := newPool(t, newDB(t))
	s := fixture(t)
	migrateAndSeed(t, p, s)
	before := fingerprint(t, p)

	if migrateAndSeed(t, p, s) {
		t.Error("second seed reported applied")
	}
	if after := fingerprint(t, p); after != before {
		t.Errorf("second run changed the database:\nbefore %s\nafter  %s", before, after)
	}
}

func TestMigrate_ChangedSeedIsReapplied(t *testing.T) {
	p := newPool(t, newDB(t))
	migrateAndSeed(t, p, fixture(t))

	cats, _ := fs.ReadFile(os.DirFS(fixtureDir), "categories.json")
	changed, err := catalog.ReadSeed(fstest.MapFS{
		"categories.json": {Data: cats},
		"products.json": {Data: []byte(`[{"id":"FIXTURE-MUG","name":"Bigger Mug","description":"Holds 500 ml.",
			"category":"kitchen","price_usd":{"currency_code":"USD","units":9,"nanos":0},"rating":4.9,"stock":3}]`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !migrateAndSeed(t, p, changed) {
		t.Fatal("changed seed reported not applied")
	}
	var name string
	var units int64
	var stock int32
	if err := p.QueryRow(context.Background(), `SELECT name, price_usd_units, stock FROM products WHERE id = 'FIXTURE-MUG'`).Scan(&name, &units, &stock); err != nil {
		t.Fatal(err)
	}
	if name != "Bigger Mug" || units != 9 || stock != 3 {
		t.Errorf("upsert did not update: %s %d %d", name, units, stock)
	}
	// Products that left the seed are kept (only the legacy ids are deleted).
	if n := len(productIDs(t, p)); n != 3 {
		t.Errorf("%d products, want 3", n)
	}
}

func TestMigrate_ConcurrentStarts(t *testing.T) {
	dsn := newDB(t)
	s := fixture(t)
	const replicas = 4

	var wg sync.WaitGroup
	errs := make(chan error, replicas)
	applied := make(chan bool, replicas)
	start := make(chan struct{})
	for i := 0; i < replicas; i++ {
		p := newPool(t, dsn) // one pool per "replica"
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx := context.Background()
			if err := catalog.Migrate(ctx, p); err != nil {
				errs <- err
				return
			}
			a, err := catalog.ApplySeed(ctx, p, s)
			if err != nil {
				errs <- err
				return
			}
			applied <- a
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(applied)
	for err := range errs {
		t.Errorf("replica failed: %v", err)
	}
	n := 0
	for a := range applied {
		if a {
			n++
		}
	}
	if n != 1 {
		t.Errorf("seed applied by %d replicas, want exactly 1", n)
	}
	p := newPool(t, dsn)
	var migrations int
	if err := p.QueryRow(context.Background(), "SELECT count(*) FROM goose_db_version WHERE version_id > 0").Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if migrations != 2 {
		t.Errorf("%d migration rows, want 2 (each migration applied once)", migrations)
	}
	if got := len(productIDs(t, p)); got != 3 {
		t.Errorf("%d products, want 3", got)
	}
}

// A re-seed with changed ratings must update the existing rows (new checksum), not duplicate them.
func TestMigrate_ReseedUpdatesRatingAndReviewCount(t *testing.T) {
	p := newPool(t, newDB(t))
	ctx := context.Background()
	first := fixture(t)
	migrateAndSeed(t, p, first)

	type row struct {
		rating  float64
		reviews int32
	}
	read := func() (map[string]row, string, int) {
		rows, err := p.Query(ctx, `SELECT id, rating, review_count FROM products ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]row{}
		n := 0
		for rows.Next() {
			var id string
			var r row
			if err := rows.Scan(&id, &r.rating, &r.reviews); err != nil {
				t.Fatal(err)
			}
			got[id] = r
			n++
		}
		var sum string
		if err := p.QueryRow(ctx, `SELECT sha256 FROM catalog_seed WHERE id = 1`).Scan(&sum); err != nil {
			t.Fatal(err)
		}
		return got, sum, n
	}
	before, sumBefore, nBefore := read()
	if before["FIXTURE-DRESS"].reviews != 0 {
		t.Fatalf("fixture should start without review_count, got %+v", before["FIXTURE-DRESS"])
	}

	// Same products, new rating and review_count for the dress only.
	cats, _ := fs.ReadFile(os.DirFS(fixtureDir), "categories.json")
	prods, _ := fs.ReadFile(os.DirFS(fixtureDir), "products.json")
	var raw []map[string]any
	if err := json.Unmarshal(prods, &raw); err != nil {
		t.Fatal(err)
	}
	for _, r := range raw {
		if r["id"] == "FIXTURE-DRESS" {
			r["rating"], r["review_count"] = 3.7, 3
		}
	}
	changedJSON, _ := json.Marshal(raw)
	changed, err := catalog.ReadSeed(fstest.MapFS{"categories.json": {Data: cats}, "products.json": {Data: changedJSON}})
	if err != nil {
		t.Fatal(err)
	}
	if !migrateAndSeed(t, p, changed) {
		t.Fatal("changed seed reported not applied")
	}
	after, sumAfter, nAfter := read()
	if sumAfter == sumBefore {
		t.Error("checksum row did not change")
	}
	if nAfter != nBefore {
		t.Errorf("%d rows after re-seed, %d before (duplicates or losses)", nAfter, nBefore)
	}
	if got := after["FIXTURE-DRESS"]; got.rating != 3.7 || got.reviews != 3 {
		t.Errorf("dress not updated: %+v", got)
	}
	if after["FIXTURE-MUG"] != before["FIXTURE-MUG"] {
		t.Errorf("untouched product changed: %+v -> %+v", before["FIXTURE-MUG"], after["FIXTURE-MUG"])
	}
	// The service's loader exposes the new count.
	t.Setenv("DATABASE_URL", p.Config().ConnString())
	if err := catalog.InitDB(ctx); err != nil {
		t.Fatal(err)
	}
	var resp pb.ListProductsResponse
	if err := catalog.LoadCatalog(&resp); err != nil {
		t.Fatal(err)
	}
	for _, pr := range resp.Products {
		if pr.Id == "FIXTURE-DRESS" && (pr.Rating != 3.7 || pr.ReviewCount != 3) {
			t.Errorf("loader returned rating %v count %d", pr.Rating, pr.ReviewCount)
		}
	}
}
