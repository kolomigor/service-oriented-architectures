package booking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	flightv1 "github.com/kolomigor/flight-booking/gen/flight/v1"
)

var (
	ErrInvalidInput        = errors.New("invalid input")
	ErrBookingNotConfirmed = errors.New("booking is not confirmed")
)

type CreateBookingRequest struct {
	UserID         string `json:"user_id"`
	FlightID       int64  `json:"flight_id"`
	PassengerName  string `json:"passenger_name"`
	PassengerEmail string `json:"passenger_email"`
	SeatCount      int32  `json:"seat_count"`
}

func (r CreateBookingRequest) validate() error {
	email, err := mail.ParseAddress(r.PassengerEmail)
	switch {
	case r.UserID == "":
		return fmt.Errorf("%w: user_id is required", ErrInvalidInput)
	case r.FlightID <= 0:
		return fmt.Errorf("%w: flight_id must be positive", ErrInvalidInput)
	case strings.TrimSpace(r.PassengerName) == "":
		return fmt.Errorf("%w: passenger_name is required", ErrInvalidInput)
	case err != nil || email.Address != r.PassengerEmail:
		return fmt.Errorf("%w: passenger_email is not a valid email", ErrInvalidInput)
	case r.SeatCount <= 0:
		return fmt.Errorf("%w: seat_count must be positive", ErrInvalidInput)
	}
	return nil
}

// Service holds the booking business logic. Errors from Flight Service
// are returned as is, as gRPC status errors.
type Service struct {
	store   *Store
	flights flightv1.FlightServiceClient
}

func NewService(store *Store, flights flightv1.FlightServiceClient) *Service {
	return &Service{store: store, flights: flights}
}

func (s *Service) SearchFlights(ctx context.Context, origin, destination string, date *time.Time) ([]*flightv1.Flight, error) {
	req := &flightv1.SearchFlightsRequest{Origin: origin, Destination: destination}
	if date != nil {
		req.Date = timestamppb.New(*date)
	}
	resp, err := s.flights.SearchFlights(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.GetFlights(), nil
}

func (s *Service) GetFlight(ctx context.Context, id int64) (*flightv1.Flight, error) {
	resp, err := s.flights.GetFlight(ctx, &flightv1.GetFlightRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return resp.GetFlight(), nil
}

func (s *Service) CreateBooking(ctx context.Context, req CreateBookingRequest) (Booking, error) {
	if err := req.validate(); err != nil {
		return Booking{}, err
	}

	flight, err := s.GetFlight(ctx, req.FlightID)
	if err != nil {
		return Booking{}, err
	}

	// The ID is generated before the call so that Flight Service can tie
	// the reservation to this booking.
	bookingID := uuid.NewString()
	_, err = s.flights.ReserveSeats(ctx, &flightv1.ReserveSeatsRequest{
		FlightId:  req.FlightID,
		SeatCount: req.SeatCount,
		BookingId: bookingID,
	})
	if err != nil {
		return Booking{}, err
	}

	b, err := s.store.Create(ctx, Booking{
		ID:             bookingID,
		UserID:         req.UserID,
		FlightID:       req.FlightID,
		PassengerName:  req.PassengerName,
		PassengerEmail: req.PassengerEmail,
		SeatCount:      req.SeatCount,
		TotalPrice:     int64(req.SeatCount) * flight.GetPrice(),
	})
	if err != nil {
		// The seats are already taken in Flight Service: give them back,
		// so a failed booking leaves no changes behind.
		s.releaseAfterFailure(ctx, bookingID)
		return Booking{}, err
	}
	return b, nil
}

func (s *Service) releaseAfterFailure(ctx context.Context, bookingID string) {
	// The request context may already be cancelled, which is often why we are here.
	ctx = context.WithoutCancel(ctx)
	_, err := s.flights.ReleaseReservation(ctx, &flightv1.ReleaseReservationRequest{BookingId: bookingID})
	if err != nil {
		slog.Error("release reservation of failed booking", "booking_id", bookingID, "err", err)
	}
}

func (s *Service) GetBooking(ctx context.Context, id string) (Booking, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Booking{}, ErrBookingNotFound
	}
	return s.store.Get(ctx, id)
}

func (s *Service) ListBookings(ctx context.Context, userID string) ([]Booking, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user_id is required", ErrInvalidInput)
	}
	return s.store.ListByUser(ctx, userID)
}

func (s *Service) CancelBooking(ctx context.Context, id string) (Booking, error) {
	b, err := s.GetBooking(ctx, id)
	if err != nil {
		return Booking{}, err
	}
	if b.Status != "CONFIRMED" {
		return Booking{}, ErrBookingNotConfirmed
	}

	// Seats go back first. ReleaseReservation is idempotent, so if marking
	// the booking cancelled fails, repeating the cancel finishes the job.
	_, err = s.flights.ReleaseReservation(ctx, &flightv1.ReleaseReservationRequest{BookingId: id})
	if err != nil {
		return Booking{}, err
	}
	return s.store.Cancel(ctx, id)
}
