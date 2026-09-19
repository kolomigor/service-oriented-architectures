package main

import (
	"log"
	"net"
	"net/http"
	"os"

	marketplacepb "github.com/kolomigor/marketplace/gen/marketplace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	catalogAddress := os.Getenv("CATALOG_SERVICE_ADDR")
	if catalogAddress == "" {
		catalogAddress = "catalog-service:50051"
	}
	recommendationAddress := os.Getenv("RECOMMENDATION_SERVICE_ADDR")
	if recommendationAddress == "" {
		recommendationAddress = "recommendation-service:50053"
	}

	catalogConn, err := grpc.NewClient(
		catalogAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("connect to catalog service: %v", err)
	}
	defer catalogConn.Close()

	recommendationConn, err := grpc.NewClient(
		recommendationAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("connect to recommendation service: %v", err)
	}
	defer recommendationConn.Close()

	grpcListener, err := net.Listen("tcp", ":50052")
	if err != nil {
		log.Fatalf("listen gRPC: %v", err)
	}

	grpcServer := grpc.NewServer()
	marketplacepb.RegisterMarketplaceServiceServer(
		grpcServer,
		newMarketplaceServer(
			newCatalogClient(catalogConn),
			newRecommendationClient(recommendationConn),
		),
	)

	go func() {
		log.Println("marketplace gRPC server listening on :50052")
		if err := grpcServer.Serve(grpcListener); err != nil {
			log.Fatalf("serve gRPC: %v", err)
		}
	}()

	httpServer := &http.Server{
		Addr:    ":8080",
		Handler: http.HandlerFunc(healthHandler),
	}

	log.Println("marketplace HTTP health server listening on :8080")
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("serve HTTP: %v", err)
	}
}
