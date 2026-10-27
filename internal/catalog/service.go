package catalog

import (
	"context"
	"io/fs"
	"strings"
	"sync"
	"time"

	pb "product-catalog-service/proto"

	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// ProductCatalog implements the gRPC ProductCatalogService.
type ProductCatalog struct {
	pb.UnimplementedProductCatalogServiceServer

	mu           sync.RWMutex // guards catalog and departments
	catalog      pb.ListProductsResponse
	departments  []*pb.Category // product_count is filled per request
	extraLatency time.Duration
	reloadFlag   *bool
}

// NewProductCatalog initialises the DB pool, applies the schema migrations and the seed from
// seed (nil = no seed), and loads the catalog into memory.
func NewProductCatalog(ctx context.Context, extraLatency time.Duration, reloadFlag *bool, seed fs.FS) (*ProductCatalog, error) {
	if err := InitDB(ctx); err != nil {
		return nil, err
	}
	if err := MigrateAndSeed(ctx, pool, seed); err != nil {
		return nil, err
	}

	svc := &ProductCatalog{
		extraLatency: extraLatency,
		reloadFlag:   reloadFlag,
	}
	if err := svc.reload(); err != nil {
		return nil, err
	}
	return svc, nil
}

// --- Health ---

func (p *ProductCatalog) Check(_ context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func (p *ProductCatalog) Watch(_ *healthpb.HealthCheckRequest, ws healthpb.Health_WatchServer) error {
	return status.Errorf(codes.Unimplemented, "health check via Watch not implemented")
}

// --- RPC methods ---

func (p *ProductCatalog) ListProducts(_ context.Context, _ *pb.Empty) (*pb.ListProductsResponse, error) {
	time.Sleep(p.extraLatency)
	products, err := p.products()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to load catalog: %v", err)
	}
	return &pb.ListProductsResponse{Products: products}, nil
}

func (p *ProductCatalog) GetProduct(_ context.Context, req *pb.GetProductRequest) (*pb.Product, error) {
	time.Sleep(p.extraLatency)
	products, err := p.products()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to load catalog: %v", err)
	}
	for _, product := range products {
		if product.Id == req.Id {
			return product, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "no product with ID %s", req.Id)
}

func (p *ProductCatalog) SearchProducts(_ context.Context, req *pb.SearchProductsRequest) (*pb.SearchProductsResponse, error) {
	time.Sleep(p.extraLatency)
	products, err := p.products()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to load catalog: %v", err)
	}
	query := strings.ToLower(req.Query)
	var results []*pb.Product
	for _, product := range products {
		if strings.Contains(strings.ToLower(product.Name), query) ||
			strings.Contains(strings.ToLower(product.Description), query) {
			results = append(results, product)
		}
	}
	return &pb.SearchProductsResponse{Results: results}, nil
}

// ListProductsFiltered returns one page of products filtered by department and query.
func (p *ProductCatalog) ListProductsFiltered(_ context.Context, req *pb.ListProductsRequest) (*pb.ListProductsResponse, error) {
	time.Sleep(p.extraLatency)
	products, err := p.products()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to load catalog: %v", err)
	}
	resp, err := filterProducts(products, req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return resp, nil
}

// ListCategories returns the departments in display order with their product counts.
func (p *ProductCatalog) ListCategories(_ context.Context, _ *pb.Empty) (*pb.ListCategoriesResponse, error) {
	time.Sleep(p.extraLatency)
	products, err := p.products()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to load catalog: %v", err)
	}
	p.mu.RLock()
	depts := p.departments
	p.mu.RUnlock()

	counts := map[string]int32{}
	for _, pr := range products {
		counts[pr.Department]++
	}
	out := make([]*pb.Category, 0, len(depts))
	for _, d := range depts {
		out = append(out, &pb.Category{Id: d.Id, Name: d.Name, Description: d.Description, ProductCount: counts[d.Id]})
	}
	return &pb.ListCategoriesResponse{Categories: out}, nil
}

// --- Internal ---

// reload reads products and departments from Postgres and swaps the cache.
func (p *ProductCatalog) reload() error {
	var fresh pb.ListProductsResponse
	if err := LoadCatalog(&fresh); err != nil {
		return err
	}
	depts, err := LoadDepartments()
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.catalog.Products = fresh.Products
	p.departments = depts
	p.mu.Unlock()
	return nil
}

// products returns the in-memory catalog, reloading from Postgres only when
// the SIGUSR1 reload flag is set (triggered externally) or the cache is empty.
func (p *ProductCatalog) products() ([]*pb.Product, error) {
	shouldReload := p.reloadFlag != nil && *p.reloadFlag
	p.mu.RLock()
	cached := p.catalog.Products
	p.mu.RUnlock()
	if shouldReload || len(cached) == 0 {
		if err := p.reload(); err != nil {
			return nil, err
		}
		if p.reloadFlag != nil {
			*p.reloadFlag = false
		}
		p.mu.RLock()
		cached = p.catalog.Products
		p.mu.RUnlock()
	}
	return cached, nil
}
