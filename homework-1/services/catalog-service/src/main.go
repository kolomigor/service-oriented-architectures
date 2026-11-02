package main

import (
	"log"
	"net"
	"net/http"

	catalogpb "github.com/kolomigor/marketplace/gen/catalog"
	"google.golang.org/grpc"
)

func main() {
	grpcListener, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("listen gRPC: %v", err)
	}

	grpcServer := grpc.NewServer()
	catalogpb.RegisterCatalogServiceServer(grpcServer, &CatalogServer{})

	go func() {
		log.Println("catalog gRPC server listening on :50051")
		if err := grpcServer.Serve(grpcListener); err != nil {
			log.Fatalf("serve gRPC: %v", err)
		}
	}()

	httpServer := &http.Server{
		Addr: ":8080",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				http.NotFound(w, r)
				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
		}),
	}

	log.Println("catalog HTTP health server listening on :8080")
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("serve HTTP: %v", err)
	}
}
