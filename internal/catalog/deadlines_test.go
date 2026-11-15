package catalog

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	pb "product-catalog-service/proto"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CODE-05: the context of the RPC that triggers a reload reaches the database call, so its
// deadline and its cancellation bound the queries (before, they ran on context.Background()).
func TestReloadUsesTheRequestContext(t *testing.T) {
	type keyT struct{}
	var seen context.Context
	svc := &ProductCatalog{load: func(ctx context.Context) ([]*pb.Product, []*pb.Category, error) {
		seen = ctx
		return []*pb.Product{{Id: "p1", Name: "One", Department: "d"}}, nil, nil
	}}

	deadline := time.Now().Add(3 * time.Minute)
	ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), keyT{}, "marker"), deadline)
	defer cancel()
	if _, err := svc.ListProducts(ctx, &pb.Empty{}); err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.Value(keyT{}) != "marker" {
		t.Fatal("load did not receive a context derived from the request context")
	}
	if dl, ok := seen.Deadline(); !ok || !dl.Equal(deadline) {
		t.Fatalf("load deadline = %v (%v), want the request deadline %v", dl, ok, deadline)
	}
}

func TestReloadStopsWhenTheRequestIsCancelled(t *testing.T) {
	svc := &ProductCatalog{load: func(ctx context.Context) ([]*pb.Product, []*pb.Category, error) {
		<-ctx.Done() // a database that never answers
		return nil, nil, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { _, err := svc.GetProduct(ctx, &pb.GetProductRequest{Id: "p1"}); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error")
		}
	case <-time.After(30 * time.Second): // guard only: a cancelled context returns at once
		t.Fatal("the reload ignored the cancelled request context")
	}
}

// hungPostgres accepts connections and never answers: what a half-dead database looks like.
func hungPostgres(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return l.Addr().String()
}

// A database that never answers must fail the load at queryTimeout, with no deadline on the
// caller's context at all (the old behaviour: wait forever).
func TestLoadersAreBoundedByQueryTimeout(t *testing.T) {
	addr := hungPostgres(t)
	p, err := pgxpool.New(context.Background(), "postgres://u:p@"+addr+"/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	prevPool, prevTimeout := pool, queryTimeout
	pool, queryTimeout = p, 200*time.Millisecond
	t.Cleanup(func() { pool, queryTimeout = prevPool, prevTimeout; p.Close() })

	for name, call := range map[string]func(context.Context) error{
		"LoadCatalog":     func(ctx context.Context) error { return LoadCatalog(ctx, &pb.ListProductsResponse{}) },
		"LoadDepartments": func(ctx context.Context) error { _, err := LoadDepartments(ctx); return err },
	} {
		done := make(chan error, 1)
		go func() { done <- call(context.Background()) }()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s: err = %v, want context deadline exceeded", name, err)
			}
		case <-time.After(30 * time.Second): // guard only; the bound under test is 200 ms
			t.Fatalf("%s did not return: the query has no deadline", name)
		}
	}
}

func TestInitDBFailsFastWithoutURLAndHidesThePassword(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := InitDB(context.Background()); err == nil {
		t.Fatal("expected an error without DATABASE_URL")
	}
	t.Setenv("DATABASE_URL", "postgres://user:s3cretpw@host:notaport/db")
	err := InitDB(context.Background())
	if err == nil {
		t.Fatal("expected an error for a malformed URL")
	}
	if msg := err.Error(); strings.Contains(msg, "s3cretpw") {
		t.Errorf("the password leaked into the error: %s", msg)
	}
}

// CODE-05: a failed load tells the caller nothing about the database (host, user, database name).
func TestLoadFailureDoesNotLeakDatabaseDetails(t *testing.T) {
	boom := errors.New(`failed to connect to host=db.internal user=catalog database=productcatalog: password authentication failed`)
	svc := &ProductCatalog{load: func(context.Context) ([]*pb.Product, []*pb.Category, error) { return nil, nil, boom }}

	_, err := svc.ListProducts(context.Background(), &pb.Empty{})

	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
	for _, leak := range []string{"db.internal", "catalog ", "productcatalog", "password"} {
		if strings.Contains(status.Convert(err).Message(), leak) {
			t.Errorf("the message leaks %q: %q", leak, status.Convert(err).Message())
		}
	}
}

func TestLoadFailureKeepsDeadlineAndCancelCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		want codes.Code
	}{
		{fmt.Errorf("query: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
		{fmt.Errorf("query: %w", context.Canceled), codes.Canceled},
	} {
		svc := &ProductCatalog{load: func(context.Context) ([]*pb.Product, []*pb.Category, error) { return nil, nil, c.err }}
		if _, err := svc.GetProduct(context.Background(), &pb.GetProductRequest{Id: "x"}); status.Code(err) != c.want {
			t.Errorf("%v -> %v, want %v", c.err, status.Code(err), c.want)
		}
	}
}
