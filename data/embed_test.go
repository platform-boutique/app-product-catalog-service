package data_test

import (
	"testing"

	"product-catalog-service/data"
	"product-catalog-service/internal/catalog"
)

// The embedded seed must parse and pass the loader's validation, or the service fails at start-up.
func TestEmbeddedSeedIsValid(t *testing.T) {
	s, err := catalog.ReadSeed(data.Seed())
	if err != nil {
		t.Fatalf("embedded seed: %v", err)
	}
	if len(s.Products) < 100 || len(s.Departments) == 0 {
		t.Fatalf("got %d products, %d departments", len(s.Products), len(s.Departments))
	}
	perDept := map[string]int{}
	for _, p := range s.Products {
		perDept[p.Category]++
		if p.Thumbnail == "" && len(p.Images) == 0 {
			t.Errorf("product %s has no image", p.ID)
		}
	}
	for _, d := range s.Departments {
		if perDept[d.ID] == 0 {
			t.Errorf("department %s has no products", d.ID)
		}
	}
	legacy := map[string]bool{}
	for _, id := range catalog.LegacyProductIDs {
		legacy[id] = true
	}
	for _, p := range s.Products {
		if legacy[p.ID] {
			t.Errorf("seed reuses legacy id %s (it would be kept, not replaced)", p.ID)
		}
	}
	t.Logf("%d products in %d departments: %v", len(s.Products), len(s.Departments), perDept)
}
