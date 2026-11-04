package booking

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrCircuitOpen = errors.New("flight service is temporarily unavailable, try again later")

type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

func (s breakerState) String() string {
	switch s {
	case stateClosed:
		return "CLOSED"
	case stateOpen:
		return "OPEN"
	default:
		return "HALF_OPEN"
	}
}

// CircuitBreaker stops calling Flight Service while it is failing.
//
//   - CLOSED: calls go through. After failureThreshold failures within
//     window it switches to OPEN.
//   - OPEN: calls fail at once with ErrCircuitOpen. After openTimeout
//     it switches to HALF_OPEN.
//   - HALF_OPEN: one trial call goes through, others fail at once.
//     If the trial succeeds it switches to CLOSED, otherwise back to OPEN.
type CircuitBreaker struct {
	failureThreshold int
	window           time.Duration
	openTimeout      time.Duration
	now              func() time.Time // replaced in tests

	mu       sync.Mutex
	state    breakerState
	failures []time.Time // times of recent failures, used in CLOSED
	openedAt time.Time   // used in OPEN
	probing  bool        // a trial call is in flight, used in HALF_OPEN
}

func NewCircuitBreaker(failureThreshold int, window, openTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		failureThreshold: failureThreshold,
		window:           window,
		openTimeout:      openTimeout,
		now:              time.Now,
	}
}

// Interceptor returns the breaker as a gRPC client interceptor.
func (b *CircuitBreaker) Interceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		allowed, trial := b.allow()
		if !allowed {
			return ErrCircuitOpen
		}
		err := invoker(ctx, method, req, reply, cc, opts...)
		b.record(err, trial)
		return err
	}
}

// allow reports whether a call may go to Flight Service now,
// and whether it is the trial call of HALF_OPEN.
func (b *CircuitBreaker) allow() (allowed, trial bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case stateClosed:
		return true, false
	case stateOpen:
		if b.now().Sub(b.openedAt) < b.openTimeout {
			return false, false
		}
		b.setState(stateHalfOpen)
	}
	if b.probing {
		return false, false
	}
	b.probing = true
	return true, true
}

func (b *CircuitBreaker) record(err error, trial bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if trial {
		b.probing = false
		switch {
		case status.Code(err) == codes.Canceled:
			// The client left before the answer: the trial told us nothing,
			// so the next call becomes the trial.
		case isFailure(err):
			b.open()
		default:
			b.failures = nil
			b.setState(stateClosed)
		}
		return
	}

	// Calls let through while CLOSED may finish after the breaker opened.
	if b.state != stateClosed || !isFailure(err) {
		return
	}
	now := b.now()
	recent := b.failures[:0]
	for _, t := range b.failures {
		if now.Sub(t) < b.window {
			recent = append(recent, t)
		}
	}
	b.failures = append(recent, now)
	if len(b.failures) >= b.failureThreshold {
		b.open()
	}
}

func (b *CircuitBreaker) open() {
	b.failures = nil
	b.openedAt = b.now()
	b.setState(stateOpen)
}

func (b *CircuitBreaker) setState(to breakerState) {
	if b.state == to {
		return
	}
	slog.Warn("circuit breaker state changed", "from", b.state.String(), "to", to.String())
	b.state = to
}

// isFailure reports whether err means Flight Service is not working. Business
// errors like NOT_FOUND are answers from a working service and do not count.
func isFailure(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Internal:
		return true
	}
	return false
}
