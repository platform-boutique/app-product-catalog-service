package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoSeed is returned by ApplySeed when the seed files are not present in the given FS.
var ErrNoSeed = errors.New("no seed files (products.json, categories.json)")

// LegacyProductIDs are the 9 Online Boutique products that the product-catalog-db image's init
// script inserts. ApplySeed deletes them once the store seed is in.
var LegacyProductIDs = []string{
	"OLJCESPC7Z", "66VCHSJNUP", "1YMWWN1N4O", "L9ECAV7KIM", "2ZYFJ3GM2N",
	"0PUK6V6EV0", "LS4PSXUNUM", "9SIQT8TOJO", "6E92ZMYYFZ",
}

// SeedProduct is one entry of products.json. Unknown fields (reviews, source_id, ...) are ignored.
type SeedProduct struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Brand          string   `json:"brand"`
	Category       string   `json:"category"` // department id
	SourceCategory string   `json:"source_category"`
	Categories     []string `json:"categories"`
	Tags           []string `json:"tags"`
	PriceUSD       struct {
		CurrencyCode string `json:"currency_code"`
		Units        int64  `json:"units"`
		Nanos        int32  `json:"nanos"`
	} `json:"price_usd"`
	Rating      float64  `json:"rating"`
	Stock       int32    `json:"stock"`
	WeightGrams int32    `json:"weight_grams"`
	Images      []string `json:"images"`
	Thumbnail   string   `json:"thumbnail"`
}

// SeedDepartment is one entry of categories.json. Its position in the file is the sort order.
type SeedDepartment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Seed is the parsed seed data plus the checksum of the raw files.
type Seed struct {
	Departments []SeedDepartment
	Products    []SeedProduct
	SHA256      string
}

// ReadSeed parses and validates products.json and categories.json from fsys.
func ReadSeed(fsys fs.FS) (*Seed, error) {
	catRaw, err := fs.ReadFile(fsys, "categories.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoSeed
	} else if err != nil {
		return nil, err
	}
	prodRaw, err := fs.ReadFile(fsys, "products.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoSeed
	} else if err != nil {
		return nil, err
	}

	s := &Seed{}
	if err := json.Unmarshal(catRaw, &s.Departments); err != nil {
		return nil, fmt.Errorf("categories.json: %w", err)
	}
	if err := json.Unmarshal(prodRaw, &s.Products); err != nil {
		return nil, fmt.Errorf("products.json: %w", err)
	}
	h := sha256.New()
	h.Write(catRaw)
	h.Write([]byte{0})
	h.Write(prodRaw)
	s.SHA256 = hex.EncodeToString(h.Sum(nil))
	return s, s.validate()
}

func (s *Seed) validate() error {
	if len(s.Products) == 0 {
		return errors.New("seed: products.json is empty")
	}
	depts := map[string]bool{}
	for _, d := range s.Departments {
		if d.ID == "" || d.Name == "" {
			return fmt.Errorf("seed: department without id or name: %+v", d)
		}
		if depts[d.ID] {
			return fmt.Errorf("seed: duplicate department %q", d.ID)
		}
		depts[d.ID] = true
	}
	ids := map[string]bool{}
	for _, p := range s.Products {
		switch {
		case p.ID == "" || p.Name == "":
			return fmt.Errorf("seed: product without id or name (id %q)", p.ID)
		case ids[p.ID]:
			return fmt.Errorf("seed: duplicate product id %q", p.ID)
		case p.Category != "" && !depts[p.Category]:
			return fmt.Errorf("seed: product %s has unknown department %q", p.ID, p.Category)
		case p.PriceUSD.CurrencyCode == "":
			return fmt.Errorf("seed: product %s has no currency", p.ID)
		case p.PriceUSD.Units < 0 || p.PriceUSD.Nanos < 0 || p.PriceUSD.Nanos > 999999999:
			return fmt.Errorf("seed: product %s has an invalid price", p.ID)
		case p.Rating < 0 || p.Rating > 5:
			return fmt.Errorf("seed: product %s has rating %v outside 0-5", p.ID, p.Rating)
		case p.Stock < 0 || p.WeightGrams < 0:
			return fmt.Errorf("seed: product %s has negative stock or weight", p.ID)
		}
		ids[p.ID] = true
	}
	return nil
}

// picture is the legacy single-image field: the first full image, else the thumbnail.
func (p SeedProduct) picture() string {
	if len(p.Images) > 0 {
		return p.Images[0]
	}
	return p.Thumbnail
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ApplySeed upserts the seed into Postgres in one transaction under the start-up advisory lock.
// If the checksum stored in catalog_seed equals the seed's checksum nothing is written and
// applied is false. Products that are no longer in the seed are kept, except the legacy
// Online Boutique rows (LegacyProductIDs), which are deleted.
func ApplySeed(ctx context.Context, p *pgxpool.Pool, s *Seed) (applied bool, err error) {
	tx, err := p.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockID); err != nil {
		return false, fmt.Errorf("seed lock: %w", err)
	}
	var current string
	err = tx.QueryRow(ctx, `SELECT sha256 FROM catalog_seed WHERE id = 1`).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("read seed checksum: %w", err)
	}
	if current == s.SHA256 {
		return false, tx.Commit(ctx)
	}

	b := &pgx.Batch{}
	for i, d := range s.Departments {
		b.Queue(`
			INSERT INTO departments (id, name, description, sort_order) VALUES ($1, $2, $3, $4)
			ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name, description = EXCLUDED.description, sort_order = EXCLUDED.sort_order`,
			d.ID, d.Name, d.Description, i)
	}
	for _, sp := range s.Products {
		var dept *string
		if sp.Category != "" {
			dept = &sp.Category
		}
		b.Queue(`
			INSERT INTO products (id, name, description, picture,
				price_usd_currency_code, price_usd_units, price_usd_nanos, categories,
				brand, department, source_category, tags, images, thumbnail,
				rating, stock, weight_grams)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name, description = EXCLUDED.description, picture = EXCLUDED.picture,
				price_usd_currency_code = EXCLUDED.price_usd_currency_code,
				price_usd_units = EXCLUDED.price_usd_units, price_usd_nanos = EXCLUDED.price_usd_nanos,
				categories = EXCLUDED.categories, brand = EXCLUDED.brand, department = EXCLUDED.department,
				source_category = EXCLUDED.source_category, tags = EXCLUDED.tags, images = EXCLUDED.images,
				thumbnail = EXCLUDED.thumbnail, rating = EXCLUDED.rating, stock = EXCLUDED.stock,
				weight_grams = EXCLUDED.weight_grams`,
			sp.ID, sp.Name, sp.Description, sp.picture(),
			sp.PriceUSD.CurrencyCode, sp.PriceUSD.Units, sp.PriceUSD.Nanos, strings.Join(sp.Categories, ","),
			sp.Brand, dept, sp.SourceCategory, nonNil(sp.Tags), nonNil(sp.Images), sp.Thumbnail,
			sp.Rating, sp.Stock, sp.WeightGrams)
	}
	seedIDs := make([]string, 0, len(s.Products))
	for _, sp := range s.Products {
		seedIDs = append(seedIDs, sp.ID)
	}
	b.Queue(`DELETE FROM products WHERE id = ANY($1) AND NOT (id = ANY($2))`, LegacyProductIDs, seedIDs)
	b.Queue(`
		INSERT INTO catalog_seed (id, sha256, product_count, applied_at) VALUES (1, $1, $2, now())
		ON CONFLICT (id) DO UPDATE SET
			sha256 = EXCLUDED.sha256, product_count = EXCLUDED.product_count, applied_at = EXCLUDED.applied_at`,
		s.SHA256, len(s.Products))

	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return false, fmt.Errorf("apply seed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// MigrateAndSeed is the start-up sequence: schema migrations, then the seed from fsys.
// Missing seed files are logged and skipped (the service then serves whatever is in the table).
func MigrateAndSeed(ctx context.Context, p *pgxpool.Pool, fsys fs.FS) error {
	if err := Migrate(ctx, p); err != nil {
		return err
	}
	if fsys == nil {
		log.Warn("no seed filesystem given, skipping seed")
		return nil
	}
	s, err := ReadSeed(fsys)
	if errors.Is(err, ErrNoSeed) {
		log.Warn("seed files not embedded in this build, skipping seed")
		return nil
	} else if err != nil {
		return err
	}
	applied, err := ApplySeed(ctx, p, s)
	if err != nil {
		return err
	}
	if applied {
		log.Infof("seed applied: %d departments, %d products (sha256 %s)", len(s.Departments), len(s.Products), s.SHA256[:12])
	} else {
		log.Infof("seed unchanged (sha256 %s), nothing written", s.SHA256[:12])
	}
	return nil
}
