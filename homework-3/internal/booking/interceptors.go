package booking

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// RetryInterceptor repeats calls that failed with UNAVAILABLE or
// DEADLINE_EXCEEDED: up to maxAttempts in total, waiting baseDelay before
// the second attempt and twice as long before each next one.
// Other errors are answers from Flight Service and are returned at once.
//
// Repeating ReserveSeats is safe: the request keeps the same booking_id,
// and Flight Service does not reserve seats twice for one booking.
func RetryInterceptor(maxAttempts int, baseDelay time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		delay := baseDelay
		for attempt := 1; ; attempt++ {
			err := invoker(ctx, method, req, reply, cc, opts...)
			if err == nil || attempt == maxAttempts || !isRetryable(err) {
				return err
			}
			slog.Warn("flight service call failed, retrying",
				"method", method, "attempt", attempt, "delay", delay, "err", err)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				// The client is gone or out of time: no point in retrying.
				return err
			}
			delay *= 2
		}
	}
}

func isRetryable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return false
}

// APIKeyInterceptor puts the API key into the metadata of every call
// to Flight Service.
func APIKeyInterceptor(apiKey string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-api-key", apiKey)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

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
