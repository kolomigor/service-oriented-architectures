package main

import (
	recommendationpb "github.com/kolomigor/marketplace/gen/recommendation"
	"google.golang.org/grpc"
)

func newRecommendationClient(conn grpc.ClientConnInterface) recommendationpb.RecommendationServiceClient {
	return recommendationpb.NewRecommendationServiceClient(conn)
}
