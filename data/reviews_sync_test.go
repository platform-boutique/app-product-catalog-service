package data_test

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"product-catalog-service/data"
	"product-catalog-service/internal/catalog"
)

// The catalog's rating / review_count are derived from the review-service seed by
// tools/sync_reviews.py (see data/seed/SOURCES.md). Two kinds of check live here:
//
//   - TestSeedReviewFieldsAreWellFormed always runs and needs nothing outside this repo.
//   - TestSeedMatchesReviewServiceSeed compares against apps/review-service/data/reviews.json. That file
//     belongs to another repository, so the test reads it only when it can find it: REVIEWS_JSON, or
//     ../../review-service/data/reviews.json relative to this directory (the apps/ checkout layout).
//     Otherwise it SKIPS (and says so); CI without that checkout therefore does not verify the drift,
//     run `python3 tools/sync_reviews.py --reviews <file> --check` where the file is available.

type review struct {
	ProductID string `json:"product_id"`
	Rating    int    `json:"rating"`
}

// round1 is the review-service rule (Postgres round(avg, 1): half away from zero) in exact integer math.
func round1(total, count int) float64 {
	if count == 0 {
		return 0
	}
	return float64((20*total+count)/(2*count)) / 10
}

func TestRound1(t *testing.T) {
	cases := []struct {
		total, count int
		want         float64
	}{
		{0, 0, 0},
		{12, 3, 4.0},
		{13, 3, 4.3},   // 4.333
		{14, 3, 4.7},   // 4.666
		{9, 2, 4.5},    // exact
		{223, 50, 4.5}, // 4.46
		{89, 20, 4.5},  // 4.45 is an exact tie: rounds up, not to even
		{87, 20, 4.4},  // 4.35 is an exact tie: rounds up
		{5, 1, 5.0},
	}
	for _, c := range cases {
		if got := round1(c.total, c.count); got != c.want {
			t.Errorf("round1(%d,%d) = %v, want %v", c.total, c.count, got, c.want)
		}
	}
}

func TestSeedReviewFieldsAreWellFormed(t *testing.T) {
	s, err := catalog.ReadSeed(data.Seed())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Products {
		switch {
		case p.ReviewCount == 0 && p.Rating != 0:
			t.Errorf("%s: rating %v without reviews", p.ID, p.Rating)
		case p.ReviewCount > 0 && (p.Rating < 1 || p.Rating > 5):
			t.Errorf("%s: rating %v outside 1-5 with %d reviews", p.ID, p.Rating, p.ReviewCount)
		case math.Abs(p.Rating*10-math.Round(p.Rating*10)) > 1e-9:
			t.Errorf("%s: rating %v has more than one decimal", p.ID, p.Rating)
		}
	}
}

func TestSeedMatchesReviewServiceSeed(t *testing.T) {
	path := os.Getenv("REVIEWS_JSON")
	if path == "" {
		path = filepath.Join("..", "..", "review-service", "data", "reviews.json")
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("review-service seed not found at %s (set REVIEWS_JSON); drift NOT checked", path)
	} else if err != nil {
		t.Fatal(err)
	}
	var reviews []review
	if err := json.Unmarshal(raw, &reviews); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	type agg struct{ total, count int }
	sums := map[string]*agg{}
	for _, r := range reviews {
		a := sums[r.ProductID]
		if a == nil {
			a = &agg{}
			sums[r.ProductID] = a
		}
		a.total += r.Rating
		a.count++
	}

	s, err := catalog.ReadSeed(data.Seed())
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, p := range s.Products {
		known[p.ID] = true
		a := sums[p.ID]
		if a == nil {
			a = &agg{}
		}
		if want := round1(a.total, a.count); p.Rating != want || int(p.ReviewCount) != a.count {
			t.Errorf("%s: seed has rating %v / %d reviews, review-service seed gives %v / %d (run tools/sync_reviews.py)",
				p.ID, p.Rating, p.ReviewCount, want, a.count)
		}
	}
	for id := range sums {
		if !known[id] {
			t.Errorf("review-service seed has reviews for %q, which is not a catalog product", id)
		}
	}
	t.Logf("%d reviews checked against %d products (%s)", len(reviews), len(s.Products), path)
}
