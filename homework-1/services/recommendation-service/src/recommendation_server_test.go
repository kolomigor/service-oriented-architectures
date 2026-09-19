package main

import (
	"context"
	"testing"

	catalogpb "github.com/kolomigor/marketplace/gen/catalog"
	recommendationpb "github.com/kolomigor/marketplace/gen/recommendation"
	"google.golang.org/grpc"
)

func TestRecommendationServerGetsCandidatesFromCatalog(t *testing.T) {
	catalogClient := &fakeCatalogClient{}
	server := newRecommendationServer(catalogClient)

	response, err := server.GetRecommendations(
		context.Background(),
		&recommendationpb.GetRecommendationsRequest{
			UserId: "user-1",
			Search: "phone",
		},
	)
	if err != nil {
		t.Fatalf("get recommendations: %v", err)
	}

	if catalogClient.search != "phone" {
		t.Fatalf("expected search to be forwarded to catalog, got %q", catalogClient.search)
	}
	if len(response.GetProducts()) != 1 || response.GetProducts()[0].GetId() != "product-1" {
		t.Fatalf("unexpected recommendation response: %+v", response.GetProducts())
	}
}

type fakeCatalogClient struct {
	search string
}

func (f *fakeCatalogClient) ListProducts(
	ctx context.Context,
	req *catalogpb.ListProductsRequest,
	opts ...grpc.CallOption,
) (*catalogpb.ListProductsResponse, error) {
	f.search = req.GetSearch()
	return &catalogpb.ListProductsResponse{
		Products: []*catalogpb.Product{{Id: "product-1"}},
	}, nil
}

func (f *fakeCatalogClient) GetProduct(
	ctx context.Context,
	req *catalogpb.GetProductRequest,
	opts ...grpc.CallOption,
) (*catalogpb.GetProductResponse, error) {
	return nil, nil
}
