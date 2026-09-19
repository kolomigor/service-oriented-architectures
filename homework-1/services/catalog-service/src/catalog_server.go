package main

import (
	"context"

	catalogpb "github.com/kolomigor/marketplace/gen/catalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type CatalogServer struct {
	catalogpb.UnimplementedCatalogServiceServer
}

func (s *CatalogServer) ListProducts(
	ctx context.Context,
	req *catalogpb.ListProductsRequest,
) (*catalogpb.ListProductsResponse, error) {
	return &catalogpb.ListProductsResponse{
		Products: []*catalogpb.Product{},
	}, nil
}

func (s *CatalogServer) GetProduct(
	ctx context.Context,
	req *catalogpb.GetProductRequest,
) (*catalogpb.GetProductResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}
