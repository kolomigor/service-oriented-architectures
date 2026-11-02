package main

import (
	"context"

	catalogpb "github.com/kolomigor/marketplace/gen/catalog"
	recommendationpb "github.com/kolomigor/marketplace/gen/recommendation"
)

type RecommendationServer struct {
	recommendationpb.UnimplementedRecommendationServiceServer
	catalogClient catalogpb.CatalogServiceClient
}

func newRecommendationServer(catalogClient catalogpb.CatalogServiceClient) *RecommendationServer {
	return &RecommendationServer{catalogClient: catalogClient}
}

func (s *RecommendationServer) GetRecommendations(
	ctx context.Context,
	req *recommendationpb.GetRecommendationsRequest,
) (*recommendationpb.GetRecommendationsResponse, error) {
	response, err := s.catalogClient.ListProducts(ctx, &catalogpb.ListProductsRequest{
		Search: req.GetSearch(),
	})
	if err != nil {
		return nil, err
	}

	return &recommendationpb.GetRecommendationsResponse{
		Products: response.GetProducts(),
	}, nil
}
