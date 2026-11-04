package booking

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestBreaker opens after 3 failures within 10s and stays open for 15s.
func newTestBreaker() (*CircuitBreaker, *fakeClock) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b := NewCircuitBreaker(3, 10*time.Second, 15*time.Second)
	b.now = clock.now
	return b, clock
}

// call makes one call through the breaker; Flight Service answers with code.
// It reports whether the call reached Flight Service.
func call(b *CircuitBreaker, code codes.Code) (reached bool, err error) {
	err = b.Interceptor()(context.Background(), "/m", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			reached = true
			return status.Error(code, code.String())
		})
	return reached, err
}

func openBreaker(t *testing.T, b *CircuitBreaker) {
	t.Helper()
	for range 3 {
		call(b, codes.Unavailable)
	}
	if b.state != stateOpen {
		t.Fatalf("state %v, want OPEN", b.state)
	}
}

func TestBreakerOpensAfterThreshold(t *testing.T) {
	b, _ := newTestBreaker()
	call(b, codes.Unavailable)
	call(b, codes.DeadlineExceeded)
	if b.state != stateClosed {
		t.Fatalf("after 2 failures: state %v, want CLOSED", b.state)
	}
	call(b, codes.Internal)
	if b.state != stateOpen {
		t.Fatalf("after 3 failures: state %v, want OPEN", b.state)
	}

	reached, err := call(b, codes.OK)
	if reached || !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("OPEN: reached=%v err=%v, want no call and ErrCircuitOpen", reached, err)
	}
}

func TestBreakerCountsOnlyFailuresWithinWindow(t *testing.T) {
	b, clock := newTestBreaker()
	call(b, codes.Unavailable)
	call(b, codes.Unavailable)
	clock.advance(11 * time.Second)
	call(b, codes.Unavailable)
	if b.state != stateClosed {
		t.Errorf("state %v, want CLOSED: the first two failures are out of the window", b.state)
	}
}

func TestBreakerIgnoresBusinessErrors(t *testing.T) {
	b, _ := newTestBreaker()
	for _, code := range []codes.Code{codes.NotFound, codes.ResourceExhausted, codes.InvalidArgument, codes.NotFound} {
		call(b, code)
	}
	if b.state != stateClosed {
		t.Errorf("state %v, want CLOSED", b.state)
	}
}

func TestBreakerHalfOpenAfterTimeout(t *testing.T) {
	b, clock := newTestBreaker()
	openBreaker(t, b)

	clock.advance(14 * time.Second)
	if reached, _ := call(b, codes.OK); reached {
		t.Fatal("call went through before the open timeout")
	}
	clock.advance(time.Second)
	if reached, _ := call(b, codes.OK); !reached {
		t.Fatal("trial call did not go through after the open timeout")
	}
	if b.state != stateClosed {
		t.Errorf("after successful trial: state %v, want CLOSED", b.state)
	}
}

func TestBreakerFailedTrialReopens(t *testing.T) {
	b, clock := newTestBreaker()
	openBreaker(t, b)
	clock.advance(15 * time.Second)

	call(b, codes.Unavailable)
	if b.state != stateOpen {
		t.Fatalf("after failed trial: state %v, want OPEN", b.state)
	}
	if reached, _ := call(b, codes.OK); reached {
		t.Error("call went through right after the breaker reopened")
	}
}

func TestBreakerHalfOpenLetsOneTrialAtATime(t *testing.T) {
	b, clock := newTestBreaker()
	openBreaker(t, b)
	clock.advance(15 * time.Second)

	inTrial, finishTrial := make(chan struct{}), make(chan struct{})
	go b.Interceptor()(context.Background(), "/m", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			close(inTrial)
			<-finishTrial
			return nil
		})
	<-inTrial

	reached, err := call(b, codes.OK)
	if reached || !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("second call during trial: reached=%v err=%v, want no call and ErrCircuitOpen", reached, err)
	}
	close(finishTrial)
}
