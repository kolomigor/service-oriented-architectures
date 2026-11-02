package main

import (
	"context"

	catalogpb "github.com/kolomigor/marketplace/gen/catalog"
	marketplacepb "github.com/kolomigor/marketplace/gen/marketplace"
	recommendationpb "github.com/kolomigor/marketplace/gen/recommendation"
)

type MarketplaceServer struct {
	marketplacepb.UnimplementedMarketplaceServiceServer
	catalogClient        catalogpb.CatalogServiceClient
	recommendationClient recommendationpb.RecommendationServiceClient
}

func newMarketplaceServer(
	catalogClient catalogpb.CatalogServiceClient,
	recommendationClient recommendationpb.RecommendationServiceClient,
) *MarketplaceServer {
	return &MarketplaceServer{
		catalogClient:        catalogClient,
		recommendationClient: recommendationClient,
	}
}

func (s *MarketplaceServer) ListProducts(
	ctx context.Context,
	req *marketplacepb.ListProductsRequest,
) (*marketplacepb.ListProductsResponse, error) {
	if req.GetUserId() != "" {
		recommendations, err := s.recommendationClient.GetRecommendations(ctx, &recommendationpb.GetRecommendationsRequest{
			UserId: req.GetUserId(),
			Search: req.GetSearch(),
		})
		if err != nil {
			return nil, err
		}

		products := make([]*marketplacepb.Product, 0, len(recommendations.GetProducts()))
		for _, product := range recommendations.GetProducts() {
			products = append(products, toMarketplaceProduct(product))
		}

		return &marketplacepb.ListProductsResponse{Products: products}, nil
	}

	response, err := s.catalogClient.ListProducts(ctx, &catalogpb.ListProductsRequest{
		Search: req.GetSearch(),
	})
	if err != nil {
		return nil, err
	}

	products := make([]*marketplacepb.Product, 0, len(response.GetProducts()))
	for _, product := range response.GetProducts() {
		products = append(products, toMarketplaceProduct(product))
	}

	return &marketplacepb.ListProductsResponse{Products: products}, nil
}

func (s *MarketplaceServer) GetProduct(
	ctx context.Context,
	req *marketplacepb.GetProductRequest,
) (*marketplacepb.GetProductResponse, error) {
	response, err := s.catalogClient.GetProduct(ctx, &catalogpb.GetProductRequest{
		ProductId: req.GetProductId(),
	})
	if err != nil {
		return nil, err
	}

	return &marketplacepb.GetProductResponse{
		Product: toMarketplaceProduct(response.GetProduct()),
	}, nil
}

func toMarketplaceProduct(product *catalogpb.Product) *marketplacepb.Product {
	if product == nil {
		return nil
	}

	return &marketplacepb.Product{
		Id:          product.GetId(),
		Name:        product.GetName(),
		Description: product.GetDescription(),
		Price:       product.GetPrice(),
		SellerId:    product.GetSellerId(),
	}
}
