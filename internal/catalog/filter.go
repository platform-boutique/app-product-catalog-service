package catalog

import (
	"fmt"
	"sort"
	"strings"

	pb "product-catalog-service/proto"
)

const (
	DefaultPageSize = 24
	MaxPageSize     = 100
)

// pageParams normalises page and page size: 0 means the default, negative is an error,
// a page size above MaxPageSize is capped.
func pageParams(page, size int32) (int32, int32, error) {
	if page < 0 {
		return 0, 0, fmt.Errorf("page must be >= 1 (0 = first page), got %d", page)
	}
	if size < 0 {
		return 0, 0, fmt.Errorf("page_size must be >= 1 (0 = %d), got %d", DefaultPageSize, size)
	}
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	return page, size, nil
}

// matchesQuery reports whether q (already lower-cased, non-empty) is a substring of the
// product's name, description, brand or one of its tags.
func matchesQuery(p *pb.Product, q string) bool {
	if strings.Contains(strings.ToLower(p.Name), q) ||
		strings.Contains(strings.ToLower(p.Description), q) ||
		strings.Contains(strings.ToLower(p.Brand), q) {
		return true
	}
	for _, t := range p.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

func priceNanos(p *pb.Product) int64 {
	if p.PriceUsd == nil {
		return 0
	}
	return p.PriceUsd.Units*1_000_000_000 + int64(p.PriceUsd.Nanos)
}

// byName orders by lower-cased name, then id, so results are deterministic.
func byName(a, b *pb.Product) bool {
	an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
	if an != bn {
		return an < bn
	}
	return a.Id < b.Id
}

func lessFunc(s pb.ProductSort) (func(a, b *pb.Product) bool, error) {
	switch s {
	case pb.ProductSort_PRODUCT_SORT_UNSPECIFIED, pb.ProductSort_PRODUCT_SORT_NAME_ASC:
		return byName, nil
	case pb.ProductSort_PRODUCT_SORT_PRICE_ASC:
		return func(a, b *pb.Product) bool {
			if x, y := priceNanos(a), priceNanos(b); x != y {
				return x < y
			}
			return byName(a, b)
		}, nil
	case pb.ProductSort_PRODUCT_SORT_PRICE_DESC:
		return func(a, b *pb.Product) bool {
			if x, y := priceNanos(a), priceNanos(b); x != y {
				return x > y
			}
			return byName(a, b)
		}, nil
	case pb.ProductSort_PRODUCT_SORT_RATING_DESC:
		return func(a, b *pb.Product) bool {
			if a.Rating != b.Rating {
				return a.Rating > b.Rating
			}
			return byName(a, b)
		}, nil
	}
	return nil, fmt.Errorf("unknown sort %d", int32(s))
}

// filterProducts applies department, query, sort and paging to all (which is not modified).
// It returns an error only for invalid arguments.
func filterProducts(all []*pb.Product, req *pb.ListProductsRequest) (*pb.ListProductsResponse, error) {
	page, size, err := pageParams(req.GetPage(), req.GetPageSize())
	if err != nil {
		return nil, err
	}
	less, err := lessFunc(req.GetSort())
	if err != nil {
		return nil, err
	}
	dept := strings.TrimSpace(req.GetDepartment())
	q := strings.ToLower(strings.TrimSpace(req.GetQuery()))

	matches := make([]*pb.Product, 0, len(all))
	for _, p := range all {
		if dept != "" && !strings.EqualFold(p.Department, dept) {
			continue
		}
		if q != "" && !matchesQuery(p, q) {
			continue
		}
		matches = append(matches, p)
	}
	sort.SliceStable(matches, func(i, j int) bool { return less(matches[i], matches[j]) })

	resp := &pb.ListProductsResponse{Total: int32(len(matches)), Page: page, PageSize: size}
	start := int64(page-1) * int64(size)
	if start < int64(len(matches)) {
		end := start + int64(size)
		if end > int64(len(matches)) {
			end = int64(len(matches))
		}
		resp.Products = matches[start:end]
	}
	return resp, nil
}
