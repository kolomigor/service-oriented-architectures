package main

import (
	"google.golang.org/grpc"

	catalogpb "github.com/kolomigor/marketplace/gen/catalog"
)

func newCatalogClient(conn grpc.ClientConnInterface) catalogpb.CatalogServiceClient {
	return catalogpb.NewCatalogServiceClient(conn)
}
