package main

import (
	"log"
	"net"
	"net/http"
	"os"

	catalogpb "github.com/kolomigor/marketplace/gen/catalog"
	recommendationpb "github.com/kolomigor/marketplace/gen/recommendation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	catalogAddress := os.Getenv("CATALOG_SERVICE_ADDR")
	if catalogAddress == "" {
		catalogAddress = "catalog-service:50051"
	}

	catalogConn, err := grpc.NewClient(
		catalogAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("connect to catalog service: %v", err)
	}
	defer catalogConn.Close()

	grpcListener, err := net.Listen("tcp", ":50053")
	if err != nil {
		log.Fatalf("listen gRPC: %v", err)
	}

	grpcServer := grpc.NewServer()
	recommendationpb.RegisterRecommendationServiceServer(
		grpcServer,
		newRecommendationServer(catalogpb.NewCatalogServiceClient(catalogConn)),
	)

	go func() {
		log.Println("recommendation gRPC server listening on :50053")
		if err := grpcServer.Serve(grpcListener); err != nil {
			log.Fatalf("serve gRPC: %v", err)
		}
	}()

	httpServer := &http.Server{
		Addr:    ":8080",
		Handler: http.HandlerFunc(healthHandler),
	}

	log.Println("recommendation HTTP health server listening on :8080")
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("serve HTTP: %v", err)
	}
}
