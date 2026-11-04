package flight

import (
	"context"
	"crypto/subtle"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// apiKeyHeader is the metadata key the client puts the API key under.
const apiKeyHeader = "x-api-key"

func checkAPIKey(ctx context.Context, apiKey string) error {
	md, _ := metadata.FromIncomingContext(ctx)
	keys := md.Get(apiKeyHeader)
	// ConstantTimeCompare takes the same time wherever the keys differ,
	// so the key cannot be guessed char by char from response times.
	if len(keys) != 1 || subtle.ConstantTimeCompare([]byte(keys[0]), []byte(apiKey)) != 1 {
		return status.Error(codes.Unauthenticated, "missing or invalid API key")
	}
	return nil
}

// UnaryAuthInterceptor rejects unary calls without a valid API key.
func UnaryAuthInterceptor(apiKey string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := checkAPIKey(ctx, apiKey); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamAuthInterceptor rejects streaming calls without a valid API key.
// FlightService has no streams; this covers the reflection service.
func StreamAuthInterceptor(apiKey string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := checkAPIKey(ss.Context(), apiKey); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}
