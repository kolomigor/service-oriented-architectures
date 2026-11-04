package booking

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// failingInvoker stands in for Flight Service: it returns the given codes
// one per call (codes.OK means success) and records when each call happened.
type failingInvoker struct {
	codes []codes.Code
	calls []time.Time
}

func (f *failingInvoker) invoke(ctx context.Context, method string, req, reply any,
	cc *grpc.ClientConn, opts ...grpc.CallOption) error {
	code := f.codes[min(len(f.calls), len(f.codes)-1)]
	f.calls = append(f.calls, time.Now())
	return status.Error(code, code.String())
}

func TestRetryInterceptorRetriesOnlyTransientErrors(t *testing.T) {
	tests := []struct {
		code         codes.Code
		wantAttempts int
	}{
		{codes.Unavailable, 3},
		{codes.DeadlineExceeded, 3},
		{codes.InvalidArgument, 1},
		{codes.NotFound, 1},
		{codes.ResourceExhausted, 1},
	}
	for _, tt := range tests {
		t.Run(tt.code.String(), func(t *testing.T) {
			inv := &failingInvoker{codes: []codes.Code{tt.code}}
			err := RetryInterceptor(3, time.Millisecond)(context.Background(), "/m", nil, nil, nil, inv.invoke)

			if status.Code(err) != tt.code {
				t.Errorf("got code %v, want %v", status.Code(err), tt.code)
			}
			if len(inv.calls) != tt.wantAttempts {
				t.Errorf("got %d attempts, want %d", len(inv.calls), tt.wantAttempts)
			}
		})
	}
}

func TestRetryInterceptorReturnsFirstSuccess(t *testing.T) {
	inv := &failingInvoker{codes: []codes.Code{codes.Unavailable, codes.OK}}
	err := RetryInterceptor(3, time.Millisecond)(context.Background(), "/m", nil, nil, nil, inv.invoke)

	if err != nil {
		t.Errorf("got error %v, want success", err)
	}
	if len(inv.calls) != 2 {
		t.Errorf("got %d attempts, want 2", len(inv.calls))
	}
}

func TestRetryInterceptorBackoffDoubles(t *testing.T) {
	inv := &failingInvoker{codes: []codes.Code{codes.Unavailable}}
	RetryInterceptor(3, 100*time.Millisecond)(context.Background(), "/m", nil, nil, nil, inv.invoke)

	for i, want := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond} {
		if got := inv.calls[i+1].Sub(inv.calls[i]); got < want {
			t.Errorf("pause before attempt %d: got %v, want at least %v", i+2, got, want)
		}
	}
}

func TestRetryInterceptorStopsWhenContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inv := &failingInvoker{codes: []codes.Code{codes.Unavailable}}
	invoke := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		cancel()
		return inv.invoke(ctx, method, req, reply, cc, opts...)
	}
	RetryInterceptor(3, time.Second)(ctx, "/m", nil, nil, nil, invoke)

	if len(inv.calls) != 1 {
		t.Errorf("got %d attempts, want 1", len(inv.calls))
	}
}
