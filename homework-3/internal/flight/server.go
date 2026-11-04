package flight

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	flightv1 "github.com/kolomigor/flight-booking/gen/flight/v1"
)

var iataCode = regexp.MustCompile(`^[A-Z]{3}$`)

// flightStore is implemented by Store and by CachedStore, which wraps it.
type flightStore interface {
	SearchFlights(ctx context.Context, origin, destination string, day *time.Time) ([]Flight, error)
	GetFlight(ctx context.Context, id int64) (Flight, error)
	ReserveSeats(ctx context.Context, flightID int64, seatCount int32, bookingID string) (Reservation, error)
	ReleaseReservation(ctx context.Context, bookingID string) (Reservation, error)
	UpdateFlight(ctx context.Context, id int64, price *int64, status *string) (Flight, error)
}

// Server implements flightv1.FlightServiceServer: it validates requests,
// calls the store and converts results and errors to gRPC.
type Server struct {
	flightv1.UnimplementedFlightServiceServer
	store flightStore
}

func NewServer(store flightStore) *Server {
	return &Server{store: store}
}

func (s *Server) SearchFlights(ctx context.Context, req *flightv1.SearchFlightsRequest) (*flightv1.SearchFlightsResponse, error) {
	origin := strings.ToUpper(req.GetOrigin())
	destination := strings.ToUpper(req.GetDestination())
	if !iataCode.MatchString(origin) || !iataCode.MatchString(destination) {
		return nil, status.Error(codes.InvalidArgument, "origin and destination must be 3-letter IATA codes")
	}

	var day *time.Time
	if req.Date != nil {
		if err := req.GetDate().CheckValid(); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid date")
		}
		t := req.GetDate().AsTime().UTC()
		start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		day = &start
	}

	flights, err := s.store.SearchFlights(ctx, origin, destination, day)
	if err != nil {
		return nil, toStatus(err)
	}
	resp := &flightv1.SearchFlightsResponse{}
	for _, f := range flights {
		resp.Flights = append(resp.Flights, toProtoFlight(f))
	}
	return resp, nil
}

func (s *Server) GetFlight(ctx context.Context, req *flightv1.GetFlightRequest) (*flightv1.GetFlightResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id must be positive")
	}
	f, err := s.store.GetFlight(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &flightv1.GetFlightResponse{Flight: toProtoFlight(f)}, nil
}

func (s *Server) ReserveSeats(ctx context.Context, req *flightv1.ReserveSeatsRequest) (*flightv1.ReserveSeatsResponse, error) {
	if req.GetFlightId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "flight_id must be positive")
	}
	if req.GetSeatCount() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "seat_count must be positive")
	}
	if _, err := uuid.Parse(req.GetBookingId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "booking_id must be a UUID")
	}

	r, err := s.store.ReserveSeats(ctx, req.GetFlightId(), req.GetSeatCount(), req.GetBookingId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &flightv1.ReserveSeatsResponse{Reservation: toProtoReservation(r)}, nil
}

func (s *Server) ReleaseReservation(ctx context.Context, req *flightv1.ReleaseReservationRequest) (*flightv1.ReleaseReservationResponse, error) {
	if _, err := uuid.Parse(req.GetBookingId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "booking_id must be a UUID")
	}

	r, err := s.store.ReleaseReservation(ctx, req.GetBookingId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &flightv1.ReleaseReservationResponse{Reservation: toProtoReservation(r)}, nil
}

func (s *Server) UpdateFlight(ctx context.Context, req *flightv1.UpdateFlightRequest) (*flightv1.UpdateFlightResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id must be positive")
	}
	if req.Price != nil && req.GetPrice() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "price must be positive")
	}
	if _, ok := flightv1.FlightStatus_name[int32(req.GetStatus())]; !ok {
		return nil, status.Error(codes.InvalidArgument, "unknown status")
	}
	var newStatus *string
	if req.GetStatus() != flightv1.FlightStatus_FLIGHT_STATUS_UNSPECIFIED {
		name := strings.TrimPrefix(req.GetStatus().String(), "FLIGHT_STATUS_")
		newStatus = &name
	}
	if req.Price == nil && newStatus == nil {
		return nil, status.Error(codes.InvalidArgument, "nothing to update")
	}

	f, err := s.store.UpdateFlight(ctx, req.GetId(), req.Price, newStatus)
	if err != nil {
		return nil, toStatus(err)
	}
	return &flightv1.UpdateFlightResponse{Flight: toProtoFlight(f)}, nil
}

// toStatus maps store errors to the gRPC codes described in flight.proto.
func toStatus(err error) error {
	switch {
	case errors.Is(err, ErrFlightNotFound), errors.Is(err, ErrReservationNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrFlightNotScheduled):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrNotEnoughSeats):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, ErrBookingConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	default:
		slog.Error("flight store failed", "err", err)
		return status.Error(codes.Internal, "internal error")
	}
}

func toProtoFlight(f Flight) *flightv1.Flight {
	return &flightv1.Flight{
		Id:             f.ID,
		FlightNumber:   f.FlightNumber,
		Airline:        f.Airline,
		Origin:         f.Origin,
		Destination:    f.Destination,
		DepartureTime:  timestamppb.New(f.DepartureTime),
		ArrivalTime:    timestamppb.New(f.ArrivalTime),
		TotalSeats:     f.TotalSeats,
		AvailableSeats: f.AvailableSeats,
		Price:          f.Price,
		Status:         flightv1.FlightStatus(flightv1.FlightStatus_value["FLIGHT_STATUS_"+f.Status]),
	}
}

func toProtoReservation(r Reservation) *flightv1.SeatReservation {
	return &flightv1.SeatReservation{
		BookingId: r.BookingID,
		FlightId:  r.FlightID,
		SeatCount: r.SeatCount,
		Status:    flightv1.ReservationStatus(flightv1.ReservationStatus_value["RESERVATION_STATUS_"+r.Status]),
		CreatedAt: timestamppb.New(r.CreatedAt),
	}
}
