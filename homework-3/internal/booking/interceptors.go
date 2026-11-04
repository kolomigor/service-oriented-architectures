package booking

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// TimeoutInterceptor gives every call to Flight Service a deadline,
// so a hanging Flight Service cannot hang Booking Service requests.
func TimeoutInterceptor(timeout time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
