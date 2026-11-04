package booking

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	flightv1 "github.com/kolomigor/flight-booking/gen/flight/v1"
)

type handler struct {
	svc *Service
}

func NewHandler(svc *Service) http.Handler {
	h := &handler{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /flights", h.searchFlights)
	mux.HandleFunc("GET /flights/{id}", h.getFlight)
	mux.HandleFunc("POST /bookings", h.createBooking)
	mux.HandleFunc("GET /bookings", h.listBookings)
	mux.HandleFunc("GET /bookings/{id}", h.getBooking)
	mux.HandleFunc("POST /bookings/{id}/cancel", h.cancelBooking)
	return mux
}

type flightJSON struct {
	ID             int64     `json:"id"`
	FlightNumber   string    `json:"flight_number"`
	Airline        string    `json:"airline"`
	Origin         string    `json:"origin"`
	Destination    string    `json:"destination"`
	DepartureTime  time.Time `json:"departure_time"`
	ArrivalTime    time.Time `json:"arrival_time"`
	TotalSeats     int32     `json:"total_seats"`
	AvailableSeats int32     `json:"available_seats"`
	Price          int64     `json:"price"` // kopecks
	Status         string    `json:"status"`
}

func toFlightJSON(f *flightv1.Flight) flightJSON {
	return flightJSON{
		ID:             f.GetId(),
		FlightNumber:   f.GetFlightNumber(),
		Airline:        f.GetAirline(),
		Origin:         f.GetOrigin(),
		Destination:    f.GetDestination(),
		DepartureTime:  f.GetDepartureTime().AsTime(),
		ArrivalTime:    f.GetArrivalTime().AsTime(),
		TotalSeats:     f.GetTotalSeats(),
		AvailableSeats: f.GetAvailableSeats(),
		Price:          f.GetPrice(),
		Status:         strings.TrimPrefix(f.GetStatus().String(), "FLIGHT_STATUS_"),
	}
}

func (h *handler) searchFlights(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	origin, destination := q.Get("origin"), q.Get("destination")
	if origin == "" || destination == "" {
		writeError(w, fmt.Errorf("%w: origin and destination are required", ErrInvalidInput))
		return
	}
	var date *time.Time
	if s := q.Get("date"); s != "" {
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			writeError(w, fmt.Errorf("%w: date must be YYYY-MM-DD", ErrInvalidInput))
			return
		}
		date = &d
	}

	flights, err := h.svc.SearchFlights(r.Context(), origin, destination, date)
	if err != nil {
		writeError(w, err)
		return
	}
	resp := make([]flightJSON, 0, len(flights))
	for _, f := range flights {
		resp = append(resp, toFlightJSON(f))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) getFlight(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, fmt.Errorf("%w: flight id must be a number", ErrInvalidInput))
		return
	}
	f, err := h.svc.GetFlight(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlightJSON(f))
}

func (h *handler) createBooking(w http.ResponseWriter, r *http.Request) {
	var req CreateBookingRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, fmt.Errorf("%w: body must be a JSON object", ErrInvalidInput))
		return
	}
	b, err := h.svc.CreateBooking(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (h *handler) listBookings(w http.ResponseWriter, r *http.Request) {
	bookings, err := h.svc.ListBookings(r.Context(), r.URL.Query().Get("user_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bookings)
}

func (h *handler) getBooking(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.GetBooking(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (h *handler) cancelBooking(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.CancelBooking(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write response", "err", err)
	}
}

// writeError picks the HTTP status for our own errors and for gRPC errors
// from Flight Service.
func writeError(w http.ResponseWriter, err error) {
	code, msg := http.StatusInternalServerError, "internal error"
	switch {
	case errors.Is(err, ErrInvalidInput):
		code, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrBookingNotFound):
		code, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, ErrBookingNotConfirmed):
		code, msg = http.StatusConflict, err.Error()
	default:
		if st, ok := status.FromError(err); ok {
			code, msg = httpStatusFromGRPC(st)
		}
	}
	if code >= http.StatusInternalServerError {
		slog.Error("request failed", "status", code, "err", err)
	}
	writeJSON(w, code, map[string]string{"error": msg})
}

func httpStatusFromGRPC(st *status.Status) (int, string) {
	switch st.Code() {
	case codes.InvalidArgument:
		return http.StatusBadRequest, st.Message()
	case codes.NotFound:
		return http.StatusNotFound, st.Message()
	case codes.ResourceExhausted, codes.FailedPrecondition, codes.AlreadyExists:
		return http.StatusConflict, st.Message()
	case codes.Unavailable:
		return http.StatusServiceUnavailable, "flight service is unavailable"
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout, "flight service did not respond in time"
	default:
		return http.StatusBadGateway, "flight service error"
	}
}
