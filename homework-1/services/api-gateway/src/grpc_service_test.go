package main

import (
	"context"
	"net"
	"testing"
	"time"

	catalogpb "github.com/kolomigor/marketplace/gen/catalog"
	marketplacepb "github.com/kolomigor/marketplace/gen/marketplace"
	recommendationpb "github.com/kolomigor/marketplace/gen/recommendation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestMarketplaceServerListProductsThroughCatalog(t *testing.T) {
	const bufferSize = 1024 * 1024

	catalogListener := bufconn.Listen(bufferSize)
	catalogGRPCServer := grpc.NewServer()
	catalogpb.RegisterCatalogServiceServer(catalogGRPCServer, &testCatalogServer{})
	go func() {
		_ = catalogGRPCServer.Serve(catalogListener)
	}()
	defer catalogGRPCServer.Stop()

	catalogConn := dialBufConn(t, catalogListener)
	defer catalogConn.Close()

	marketplaceListener := bufconn.Listen(bufferSize)
	marketplaceGRPCServer := grpc.NewServer()
	marketplacepb.RegisterMarketplaceServiceServer(
		marketplaceGRPCServer,
		newMarketplaceServer(newCatalogClient(catalogConn), nil),
	)
	go func() {
		_ = marketplaceGRPCServer.Serve(marketplaceListener)
	}()
	defer marketplaceGRPCServer.Stop()

	marketplaceConn := dialBufConn(t, marketplaceListener)
	defer marketplaceConn.Close()

	client := marketplacepb.NewMarketplaceServiceClient(marketplaceConn)
	response, err := client.ListProducts(
		context.Background(),
		&marketplacepb.ListProductsRequest{Search: "phone"},
	)
	if err != nil {
		t.Fatalf("list products through gateway: %v", err)
	}

	if got := len(response.GetProducts()); got != 0 {
		t.Fatalf("expected empty product list, got %d products", got)
	}
}

func TestMarketplaceServerListProductsThroughRecommendationAndCatalog(t *testing.T) {
	const bufferSize = 1024 * 1024

	catalogListener := bufconn.Listen(bufferSize)
	catalogGRPCServer := grpc.NewServer()
	catalogpb.RegisterCatalogServiceServer(catalogGRPCServer, &testCatalogServer{})
	go func() {
		_ = catalogGRPCServer.Serve(catalogListener)
	}()
	defer catalogGRPCServer.Stop()

	catalogConn := dialBufConn(t, catalogListener)
	defer catalogConn.Close()

	recommendationServer := &testRecommendationServer{
		requests: make(chan *recommendationpb.GetRecommendationsRequest, 1),
	}
	recommendationListener := bufconn.Listen(bufferSize)
	recommendationGRPCServer := grpc.NewServer()
	recommendationpb.RegisterRecommendationServiceServer(recommendationGRPCServer, recommendationServer)
	go func() {
		_ = recommendationGRPCServer.Serve(recommendationListener)
	}()
	defer recommendationGRPCServer.Stop()

	recommendationConn := dialBufConn(t, recommendationListener)
	defer recommendationConn.Close()

	marketplaceListener := bufconn.Listen(bufferSize)
	marketplaceGRPCServer := grpc.NewServer()
	marketplacepb.RegisterMarketplaceServiceServer(
		marketplaceGRPCServer,
		newMarketplaceServer(
			newCatalogClient(catalogConn),
			newRecommendationClient(recommendationConn),
		),
	)
	go func() {
		_ = marketplaceGRPCServer.Serve(marketplaceListener)
	}()
	defer marketplaceGRPCServer.Stop()

	marketplaceConn := dialBufConn(t, marketplaceListener)
	defer marketplaceConn.Close()

	response, err := marketplacepb.NewMarketplaceServiceClient(marketplaceConn).ListProducts(
		context.Background(),
		&marketplacepb.ListProductsRequest{Search: "phone", UserId: "user-1"},
	)
	if err != nil {
		t.Fatalf("list personalized products through gateway: %v", err)
	}

	request := <-recommendationServer.requests
	if request.GetUserId() != "user-1" || request.GetSearch() != "phone" {
		t.Fatalf("unexpected recommendation request: %+v", request)
	}

	products := response.GetProducts()
	if len(products) != 1 {
		t.Fatalf("expected one recommended product, got %d", len(products))
	}
	if products[0].GetId() != "product-1" {
		t.Fatalf("expected product-1, got %q", products[0].GetId())
	}
}

type testCatalogServer struct {
	catalogpb.UnimplementedCatalogServiceServer
}

func (s *testCatalogServer) ListProducts(
	context.Context,
	*catalogpb.ListProductsRequest,
) (*catalogpb.ListProductsResponse, error) {
	return &catalogpb.ListProductsResponse{}, nil
}

func (s *testCatalogServer) GetProduct(
	context.Context,
	*catalogpb.GetProductRequest,
) (*catalogpb.GetProductResponse, error) {
	return &catalogpb.GetProductResponse{
		Product: &catalogpb.Product{
			Id:   "product-1",
			Name: "Phone",
		},
	}, nil
}

type testRecommendationServer struct {
	recommendationpb.UnimplementedRecommendationServiceServer
	requests chan *recommendationpb.GetRecommendationsRequest
}

func (s *testRecommendationServer) GetRecommendations(
	ctx context.Context,
	req *recommendationpb.GetRecommendationsRequest,
) (*recommendationpb.GetRecommendationsResponse, error) {
	s.requests <- req
	return &recommendationpb.GetRecommendationsResponse{
		Products: []*catalogpb.Product{{
			Id:   "product-1",
			Name: "Phone",
		}},
	}, nil
}

func dialBufConn(t *testing.T, listener *bufconn.Listener) *grpc.ClientConn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("dial in-process gRPC server: %v", err)
	}

	return conn
}
