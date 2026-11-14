package catalog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "product-catalog-service/proto"
)

// newMockCatalog builds a ProductCatalog with hardcoded products — no file I/O.
func newMockCatalog() *ProductCatalog {
	return &ProductCatalog{
		catalog: pb.ListProductsResponse{
			Products: []*pb.Product{
				{Id: "abc001", Name: "Product Alpha One"},
				{Id: "abc002", Name: "Product Delta"},
				{Id: "abc003", Name: "Product Alpha Two"},
				{Id: "abc004", Name: "Product Gamma"},
			},
		},
	}
}

func TestGetProductExists(t *testing.T) {
	svc := newMockCatalog()
	product, err := svc.GetProduct(context.Background(), &pb.GetProductRequest{Id: "abc003"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := product.Name, "Product Alpha Two"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGetProductNotFound(t *testing.T) {
	svc := newMockCatalog()
	_, err := svc.GetProduct(context.Background(), &pb.GetProductRequest{Id: "doesnotexist"})
	if got, want := status.Code(err), codes.NotFound; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestListProducts(t *testing.T) {
	svc := newMockCatalog()
	resp, err := svc.ListProducts(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(resp.Products), 4; got != want {
		t.Errorf("got %d products, want %d", got, want)
	}
}

func TestSearchProducts(t *testing.T) {
	svc := newMockCatalog()
	resp, err := svc.SearchProducts(context.Background(), &pb.SearchProductsRequest{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(resp.Results), 2; got != want {
		t.Errorf("got %d results, want %d", got, want)
	}
}

func TestSearchProductsNoResults(t *testing.T) {
	svc := newMockCatalog()
	resp, err := svc.SearchProducts(context.Background(), &pb.SearchProductsRequest{Query: "zzznomatch"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(resp.Results); got != 0 {
		t.Errorf("got %d results, want 0", got)
	}
}

func TestSearchProductsCaseInsensitive(t *testing.T) {
	svc := newMockCatalog()
	resp, err := svc.SearchProducts(context.Background(), &pb.SearchProductsRequest{Query: "ALPHA"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(resp.Results), 2; got != want {
		t.Errorf("got %d results, want %d", got, want)
	}
}

func TestGetProductEmptyID(t *testing.T) {
	svc := newMockCatalog()
	_, err := svc.GetProduct(context.Background(), &pb.GetProductRequest{Id: ""})
	if got, want := status.Code(err), codes.NotFound; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestListProductsEmpty(t *testing.T) {
	svc := &ProductCatalog{}
	_, err := svc.ListProducts(context.Background(), &pb.Empty{})
	if err == nil {
		t.Error("expected error when catalog is empty and no DB, got nil")
	}
}

// Under -race: the SIGUSR1 goroutine arms/disarms the flag while gRPC goroutines read the catalog,
// reload it and a direct reload swaps the cache. Before the flag was atomic this was a data race.
func TestConcurrentReloadAndRequests(t *testing.T) {
	var loads atomic.Int64
	var flag atomic.Bool
	svc := &ProductCatalog{
		reloadFlag: &flag,
		load: func() ([]*pb.Product, []*pb.Category, error) {
			n := loads.Add(1)
			return []*pb.Product{{Id: "p1", Name: "One", Department: "d"}, {Id: "p2", Name: "Two", Department: "d"}},
				[]*pb.Category{{Id: "d", Name: fmt.Sprint("dept ", n)}}, nil
		},
	}
	if err := svc.reload(); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // what the signal goroutine in main does
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			flag.Store(i%3 != 0)
			if i%7 == 0 {
				_ = svc.reload()
			}
		}
	}()
	ctx := context.Background()
	var readers sync.WaitGroup
	for g := 0; g < 8; g++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 300; i++ {
				if r, err := svc.ListProducts(ctx, &pb.Empty{}); err != nil || len(r.Products) != 2 {
					t.Errorf("ListProducts: %v %v", r, err)
					return
				}
				if _, err := svc.GetProduct(ctx, &pb.GetProductRequest{Id: "p2"}); err != nil {
					t.Errorf("GetProduct: %v", err)
					return
				}
				if r, err := svc.ListCategories(ctx, &pb.Empty{}); err != nil || len(r.Categories) != 1 || r.Categories[0].ProductCount != 2 {
					t.Errorf("ListCategories: %v %v", r, err)
					return
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	wg.Wait()
	if loads.Load() < 2 {
		t.Errorf("only %d loads: the reload path was not exercised", loads.Load())
	}
}

// The flag is one-shot, and a failed reload re-arms it.
func TestReloadFlagIsOneShotAndRearmedOnError(t *testing.T) {
	var flag atomic.Bool
	var loads int
	fail := true
	svc := &ProductCatalog{
		reloadFlag: &flag,
		load: func() ([]*pb.Product, []*pb.Category, error) {
			loads++
			if fail {
				return nil, nil, errors.New("db down")
			}
			return []*pb.Product{{Id: "x"}}, nil, nil
		},
	}
	svc.catalog.Products = []*pb.Product{{Id: "old"}}

	flag.Store(true)
	if _, err := svc.products(); err == nil {
		t.Fatal("want the reload error")
	}
	if !flag.Load() {
		t.Fatal("flag should be re-armed after a failed reload")
	}
	fail = false
	got, err := svc.products()
	if err != nil || len(got) != 1 || got[0].Id != "x" {
		t.Fatalf("got %v, %v", got, err)
	}
	if flag.Load() {
		t.Fatal("flag should be cleared after a successful reload")
	}
	if _, err := svc.products(); err != nil || loads != 2 {
		t.Fatalf("one-shot violated: %d loads, err %v", loads, err)
	}
}
