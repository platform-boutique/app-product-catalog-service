//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"

	pb "product-catalog-service/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// grpcHost returns the service address from env or defaults to localhost.
// In the pipeline this will be set to the kind service address.
func grpcHost() string {
	if h := os.Getenv("GRPC_HOST"); h != "" {
		return h
	}
	return "localhost:3550"
}

// newClient creates a gRPC client connection for the test suite.
func newClient(t *testing.T) (pb.ProductCatalogServiceClient, func()) {
	t.Helper()

	conn, err := grpc.NewClient(
		grpcHost(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to connect to %s: %v", grpcHost(), err)
	}

	client := pb.NewProductCatalogServiceClient(conn)
	return client, func() { conn.Close() }
}

// ── ListProducts ──────────────────────────────────────────────────────────────

func TestListProducts_ReturnsAllProducts(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	resp, err := client.ListProducts(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("ListProducts failed: %v", err)
	}
	if len(resp.Products) == 0 {
		t.Fatal("expected products, got empty list")
	}
	t.Logf("ListProducts returned %d products", len(resp.Products))
}

func TestListProducts_EachProductHasRequiredFields(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	resp, err := client.ListProducts(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("ListProducts failed: %v", err)
	}

	for _, p := range resp.Products {
		if p.Id == "" {
			t.Errorf("product has empty ID: %+v", p)
		}
		if p.Name == "" {
			t.Errorf("product %s has empty name", p.Id)
		}
		if p.PriceUsd == nil {
			t.Errorf("product %s has nil price", p.Id)
		}
	}
}

// ── GetProduct ────────────────────────────────────────────────────────────────

// firstProduct returns some product of the running catalog, so the tests do not depend on seed ids.
func firstProduct(t *testing.T, client pb.ProductCatalogServiceClient) *pb.Product {
	t.Helper()
	resp, err := client.ListProducts(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("ListProducts failed: %v", err)
	}
	if len(resp.Products) == 0 {
		t.Fatal("catalog is empty")
	}
	return resp.Products[0]
}

func TestGetProduct_ExistingID_ReturnsProduct(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	want := firstProduct(t, client)
	product, err := client.GetProduct(context.Background(), &pb.GetProductRequest{Id: want.Id})
	if err != nil {
		t.Fatalf("GetProduct failed: %v", err)
	}
	if product.Id != want.Id || product.Name != want.Name {
		t.Errorf("got %s/%s, want %s/%s", product.Id, product.Name, want.Id, want.Name)
	}
}

func TestGetProduct_UnknownID_ReturnsNotFound(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	_, err := client.GetProduct(context.Background(), &pb.GetProductRequest{Id: "doesnotexist"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got, want := status.Code(err), codes.NotFound; got != want {
		t.Errorf("got status %s, want %s", got, want)
	}
}

func TestGetProduct_EmptyID_ReturnsNotFound(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	_, err := client.GetProduct(context.Background(), &pb.GetProductRequest{Id: ""})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got, want := status.Code(err), codes.NotFound; got != want {
		t.Errorf("got status %s, want %s", got, want)
	}
}

// ── SearchProducts ────────────────────────────────────────────────────────────

func TestSearchProducts_MatchingQuery_ReturnsResults(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	query := strings.Fields(firstProduct(t, client).Name)[0]
	resp, err := client.SearchProducts(context.Background(), &pb.SearchProductsRequest{Query: query})
	if err != nil {
		t.Fatalf("SearchProducts failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("expected results for %q, got empty", query)
	}
	t.Logf("SearchProducts(%q) returned %d results", query, len(resp.Results))
}

func TestSearchProducts_CaseInsensitive(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	query := strings.Fields(firstProduct(t, client).Name)[0]
	lower, err := client.SearchProducts(context.Background(), &pb.SearchProductsRequest{Query: strings.ToLower(query)})
	if err != nil {
		t.Fatalf("SearchProducts failed: %v", err)
	}

	upper, err := client.SearchProducts(context.Background(), &pb.SearchProductsRequest{Query: strings.ToUpper(query)})
	if err != nil {
		t.Fatalf("SearchProducts failed: %v", err)
	}

	if len(lower.Results) != len(upper.Results) {
		t.Errorf("case sensitivity issue: lower returned %d, upper returned %d",
			len(lower.Results), len(upper.Results))
	}
}

func TestSearchProducts_NoMatch_ReturnsEmpty(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	resp, err := client.SearchProducts(context.Background(), &pb.SearchProductsRequest{Query: "zzznomatch"})
	if err != nil {
		t.Fatalf("SearchProducts failed: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(resp.Results))
	}
}

// ── ListProductsFiltered / ListCategories ─────────────────────────────────────

func TestListProductsFiltered_PagesCoverTheCatalog(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()
	ctx := context.Background()

	all, err := client.ListProducts(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("ListProducts failed: %v", err)
	}
	seen := map[string]bool{}
	for page := int32(1); ; page++ {
		resp, err := client.ListProductsFiltered(ctx, &pb.ListProductsRequest{Page: page, PageSize: 10})
		if err != nil {
			t.Fatalf("ListProductsFiltered page %d failed: %v", page, err)
		}
		if int(resp.Total) != len(all.Products) {
			t.Fatalf("total %d, ListProducts has %d", resp.Total, len(all.Products))
		}
		if len(resp.Products) == 0 {
			break
		}
		for _, p := range resp.Products {
			seen[p.Id] = true
		}
	}
	if len(seen) != len(all.Products) {
		t.Errorf("pages returned %d distinct products, want %d", len(seen), len(all.Products))
	}
}

func TestListProductsFiltered_BadPageSize(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	_, err := client.ListProductsFiltered(context.Background(), &pb.ListProductsRequest{PageSize: -1})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("got %s, want InvalidArgument", got)
	}
}

func TestListCategories_CountsMatchFilter(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()
	ctx := context.Background()

	resp, err := client.ListCategories(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("ListCategories failed: %v", err)
	}
	for _, c := range resp.Categories {
		page, err := client.ListProductsFiltered(ctx, &pb.ListProductsRequest{Department: c.Id, PageSize: 1})
		if err != nil {
			t.Fatalf("ListProductsFiltered(%s) failed: %v", c.Id, err)
		}
		if page.Total != c.ProductCount {
			t.Errorf("department %s: product_count %d, filter total %d", c.Id, c.ProductCount, page.Total)
		}
	}
}
