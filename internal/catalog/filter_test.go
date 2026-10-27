package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"testing/fstest"

	pb "product-catalog-service/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func money(units int64, nanos int32) *pb.Money {
	return &pb.Money{CurrencyCode: "USD", Units: units, Nanos: nanos}
}

// newStoreCatalog: 5 products over 2 departments plus one legacy product without department.
func newStoreCatalog() *ProductCatalog {
	return &ProductCatalog{
		catalog: pb.ListProductsResponse{Products: []*pb.Product{
			{Id: "p1", Name: "Summer Dress", Description: "Light cotton", Brand: "Acme", Department: "women", Tags: []string{"dresses"}, PriceUsd: money(49, 990000000), Rating: 4.5},
			{Id: "p2", Name: "frying pan", Description: "Non-stick", Brand: "Chef", Department: "kitchen", Tags: []string{"cookware"}, PriceUsd: money(24, 0), Rating: 3.9},
			{Id: "p3", Name: "Mug", Description: "Stoneware", Brand: "Chef", Department: "kitchen", Tags: []string{"tableware", "Coffee"}, PriceUsd: money(8, 500000000), Rating: 4.8},
			{Id: "p4", Name: "Ankle Boots", Description: "Leather boots", Brand: "Acme", Department: "women", PriceUsd: money(89, 0), Rating: 4.5},
			{Id: "p5", Name: "Bowl", Description: "Ceramic", Brand: "Potter", Department: "kitchen", PriceUsd: money(8, 500000000), Rating: 2.0},
			{Id: "old", Name: "Candle Holder", Description: "Gift", PriceUsd: money(18, 990000000)},
		}},
		departments: []*pb.Category{
			{Id: "women", Name: "Women"},
			{Id: "kitchen", Name: "Kitchen"},
			{Id: "sports", Name: "Sports"},
		},
	}
}

func ids(ps []*pb.Product) string {
	s := ""
	for i, p := range ps {
		if i > 0 {
			s += ","
		}
		s += p.Id
	}
	return s
}

func TestListProductsFiltered(t *testing.T) {
	tests := []struct {
		name  string
		req   *pb.ListProductsRequest
		want  string
		total int32
	}{
		{"default sort is name asc, case-insensitive", &pb.ListProductsRequest{}, "p4,p5,old,p2,p3,p1", 6},
		{"explicit name asc", &pb.ListProductsRequest{Sort: pb.ProductSort_PRODUCT_SORT_NAME_ASC}, "p4,p5,old,p2,p3,p1", 6},
		{"department", &pb.ListProductsRequest{Department: "kitchen"}, "p5,p2,p3", 3},
		{"department is case-insensitive", &pb.ListProductsRequest{Department: "KITCHEN"}, "p5,p2,p3", 3},
		{"unknown department", &pb.ListProductsRequest{Department: "cars"}, "", 0},
		{"query name", &pb.ListProductsRequest{Query: "DRESS"}, "p1", 1},
		{"query description", &pb.ListProductsRequest{Query: "ceramic"}, "p5", 1},
		{"query brand", &pb.ListProductsRequest{Query: "acme"}, "p4,p1", 2},
		{"query tag", &pb.ListProductsRequest{Query: "coffee"}, "p3", 1},
		{"query is trimmed", &pb.ListProductsRequest{Query: "  mug "}, "p3", 1},
		{"query and department", &pb.ListProductsRequest{Query: "chef", Department: "kitchen"}, "p2,p3", 2},
		{"query no match", &pb.ListProductsRequest{Query: "zzz"}, "", 0},
		{"price asc, tie by name", &pb.ListProductsRequest{Sort: pb.ProductSort_PRODUCT_SORT_PRICE_ASC}, "p5,p3,old,p2,p1,p4", 6},
		{"price desc, tie by name", &pb.ListProductsRequest{Sort: pb.ProductSort_PRODUCT_SORT_PRICE_DESC}, "p4,p1,p2,old,p5,p3", 6},
		{"rating desc, tie by name", &pb.ListProductsRequest{Sort: pb.ProductSort_PRODUCT_SORT_RATING_DESC}, "p3,p4,p1,p2,p5,old", 6},
		{"page 1 of 2", &pb.ListProductsRequest{PageSize: 4}, "p4,p5,old,p2", 6},
		{"page 2 of 2", &pb.ListProductsRequest{PageSize: 4, Page: 2}, "p3,p1", 6},
		{"page past the end", &pb.ListProductsRequest{PageSize: 4, Page: 3}, "", 6},
		{"huge page number", &pb.ListProductsRequest{PageSize: 100, Page: 2147483647}, "", 6},
	}
	svc := newStoreCatalog()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := svc.ListProductsFiltered(context.Background(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(resp.Products); got != tc.want {
				t.Errorf("products = %q, want %q", got, tc.want)
			}
			if resp.Total != tc.total {
				t.Errorf("total = %d, want %d", resp.Total, tc.total)
			}
		})
	}
}

func TestListProductsFilteredPageDefaults(t *testing.T) {
	var products []*pb.Product
	for i := 0; i < 130; i++ {
		products = append(products, &pb.Product{Id: fmt.Sprintf("id%03d", i), Name: fmt.Sprintf("Product %03d", i), PriceUsd: money(1, 0)})
	}
	svc := &ProductCatalog{catalog: pb.ListProductsResponse{Products: products}}

	resp, err := svc.ListProductsFiltered(context.Background(), &pb.ListProductsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Products) != DefaultPageSize || resp.Page != 1 || resp.PageSize != DefaultPageSize || resp.Total != 130 {
		t.Errorf("defaults: got %d products, page %d, size %d, total %d", len(resp.Products), resp.Page, resp.PageSize, resp.Total)
	}

	resp, err = svc.ListProductsFiltered(context.Background(), &pb.ListProductsRequest{PageSize: 500, Page: 2})
	if err != nil {
		t.Fatal(err)
	}
	if resp.PageSize != MaxPageSize || len(resp.Products) != 30 || resp.Products[0].Id != "id100" {
		t.Errorf("capped: got size %d, %d products, first %s", resp.PageSize, len(resp.Products), resp.Products[0].Id)
	}
}

func TestListProductsFilteredInvalidArguments(t *testing.T) {
	svc := newStoreCatalog()
	for name, req := range map[string]*pb.ListProductsRequest{
		"negative page size": {PageSize: -1},
		"negative page":      {Page: -2},
		"unknown sort":       {Sort: pb.ProductSort(99)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.ListProductsFiltered(context.Background(), req)
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("code = %s, want InvalidArgument (err %v)", got, err)
			}
		})
	}
}

func TestListProductsFilteredDoesNotReorderCache(t *testing.T) {
	svc := newStoreCatalog()
	before := ids(svc.catalog.Products)
	if _, err := svc.ListProductsFiltered(context.Background(), &pb.ListProductsRequest{Sort: pb.ProductSort_PRODUCT_SORT_PRICE_DESC}); err != nil {
		t.Fatal(err)
	}
	if after := ids(svc.catalog.Products); after != before {
		t.Errorf("cache order changed: %s -> %s", before, after)
	}
}

func TestListCategories(t *testing.T) {
	svc := newStoreCatalog()
	resp, err := svc.ListCategories(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, c := range resp.Categories {
		got += fmt.Sprintf("%s=%d ", c.Id, c.ProductCount)
	}
	if want := "women=2 kitchen=3 sports=0 "; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestListCategoriesNoDepartments(t *testing.T) {
	svc := newMockCatalog()
	resp, err := svc.ListCategories(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Categories) != 0 {
		t.Errorf("got %d categories, want 0", len(resp.Categories))
	}
}

func TestReadSeedFixture(t *testing.T) {
	s, err := ReadSeed(os.DirFS("testdata/seed"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Products) != 3 || len(s.Departments) != 2 || len(s.SHA256) != 64 {
		t.Fatalf("got %d products, %d departments, sha %q", len(s.Products), len(s.Departments), s.SHA256)
	}
	p := s.Products[0]
	if p.ID != "FIXTURE-DRESS" || p.Category != "women" || p.PriceUSD.Units != 49 || p.PriceUSD.Nanos != 990000000 ||
		p.Rating != 4.5 || p.Stock != 12 || p.WeightGrams != 300 || len(p.Tags) != 2 || len(p.Images) != 2 {
		t.Errorf("unexpected first product: %+v", p)
	}
	if got := p.picture(); got != "/static/img/products/fixture-dress-1.webp" {
		t.Errorf("picture = %q", got)
	}
	if got := s.Products[1].picture(); got != "/static/img/products/fixture-pan-thumb.webp" {
		t.Errorf("picture without images = %q, want the thumbnail", got)
	}
}

func TestReadSeedErrors(t *testing.T) {
	cats := `[{"id":"home","name":"Home"}]`
	prod := func(extra string) string {
		return `[{"id":"a","name":"A","category":"home","price_usd":{"currency_code":"USD","units":1}` + extra + `}]`
	}
	tests := map[string]fstest.MapFS{
		"missing products":    {"categories.json": {Data: []byte(cats)}},
		"empty products":      {"categories.json": {Data: []byte(cats)}, "products.json": {Data: []byte(`[]`)}},
		"bad json":            {"categories.json": {Data: []byte(cats)}, "products.json": {Data: []byte(`{`)}},
		"unknown department":  {"categories.json": {Data: []byte(`[]`)}, "products.json": {Data: []byte(prod(""))}},
		"rating out of range": {"categories.json": {Data: []byte(cats)}, "products.json": {Data: []byte(prod(`,"rating":7`))}},
		"negative stock":      {"categories.json": {Data: []byte(cats)}, "products.json": {Data: []byte(prod(`,"stock":-1`))}},
		"duplicate id": {"categories.json": {Data: []byte(cats)}, "products.json": {Data: []byte(
			`[{"id":"a","name":"A","price_usd":{"currency_code":"USD"}},{"id":"a","name":"B","price_usd":{"currency_code":"USD"}}]`)}},
	}
	for name, fsys := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadSeed(fsys); err == nil {
				t.Error("expected an error")
			}
		})
	}
	if _, err := ReadSeed(fstest.MapFS{}); err != ErrNoSeed {
		t.Errorf("empty FS: got %v, want ErrNoSeed", err)
	}
}
