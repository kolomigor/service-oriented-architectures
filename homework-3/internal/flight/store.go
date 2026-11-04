package flight

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrFlightNotFound      = errors.New("flight not found")
	ErrReservationNotFound = errors.New("reservation not found")
	ErrFlightNotScheduled  = errors.New("flight is not scheduled")
	ErrNotEnoughSeats      = errors.New("not enough available seats")
	ErrBookingConflict     = errors.New("booking already has a reservation with other parameters")
)

type Flight struct {
	ID             int64
	FlightNumber   string // airline code + number, e.g. "SU1234"
	Airline        string
	Origin         string
	Destination    string
	DepartureTime  time.Time
	ArrivalTime    time.Time
	TotalSeats     int32
	AvailableSeats int32
	Price          int64 // kopecks
	Status         string
}

type Reservation struct {
	BookingID string
	FlightID  int64
	SeatCount int32
	Status    string
	CreatedAt time.Time
}

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

const flightColumns = `f.id, f.airline_code || f.flight_number, a.name, f.origin, f.destination,
	f.departure_time, f.arrival_time, f.total_seats, f.available_seats, f.price, f.status::text`

const flightFrom = ` FROM flights f JOIN airlines a ON a.code = f.airline_code`

const reservationColumns = `booking_id::text, flight_id, seat_count, status::text, created_at`

func scanFlight(row pgx.Row) (Flight, error) {
	var f Flight
	err := row.Scan(&f.ID, &f.FlightNumber, &f.Airline, &f.Origin, &f.Destination,
		&f.DepartureTime, &f.ArrivalTime, &f.TotalSeats, &f.AvailableSeats, &f.Price, &f.Status)
	return f, err
}

func scanReservation(row pgx.Row) (Reservation, error) {
	var r Reservation
	err := row.Scan(&r.BookingID, &r.FlightID, &r.SeatCount, &r.Status, &r.CreatedAt)
	return r, err
}

// SearchFlights returns SCHEDULED flights for the route. If day is not nil,
// only flights departing within that day (starting at *day) are returned.
func (s *Store) SearchFlights(ctx context.Context, origin, destination string, day *time.Time) ([]Flight, error) {
	rows, err := s.db.Query(ctx, `SELECT `+flightColumns+flightFrom+`
		WHERE f.origin = $1 AND f.destination = $2 AND f.status = 'SCHEDULED'
		  AND ($3::timestamptz IS NULL OR (f.departure_time >= $3 AND f.departure_time < $3 + interval '1 day'))
		ORDER BY f.departure_time`, origin, destination, day)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Flight, error) {
		return scanFlight(row)
	})
}

func (s *Store) GetFlight(ctx context.Context, id int64) (Flight, error) {
	f, err := scanFlight(s.db.QueryRow(ctx, `SELECT `+flightColumns+flightFrom+` WHERE f.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Flight{}, ErrFlightNotFound
	}
	return f, err
}

// ReserveSeats decreases available_seats and creates a reservation in one transaction.
// A repeated call with the same bookingID returns the existing reservation.
func (s *Store) ReserveSeats(ctx context.Context, flightID int64, seatCount int32, bookingID string) (Reservation, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Reservation{}, err
	}
	defer tx.Rollback(ctx)

	// The row lock makes concurrent reservations on this flight wait for each other,
	// so two bookings cannot both take the last seat.
	var available int32
	var status string
	err = tx.QueryRow(ctx, `SELECT available_seats, status::text FROM flights WHERE id = $1 FOR UPDATE`,
		flightID).Scan(&available, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, ErrFlightNotFound
	}
	if err != nil {
		return Reservation{}, err
	}

	existing, err := scanReservation(tx.QueryRow(ctx,
		`SELECT `+reservationColumns+` FROM seat_reservations WHERE booking_id = $1`, bookingID))
	if err == nil {
		if existing.FlightID != flightID || existing.SeatCount != seatCount {
			return Reservation{}, ErrBookingConflict
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, err
	}

	if status != "SCHEDULED" {
		return Reservation{}, ErrFlightNotScheduled
	}
	if available < seatCount {
		return Reservation{}, ErrNotEnoughSeats
	}

	_, err = tx.Exec(ctx, `UPDATE flights SET available_seats = available_seats - $1 WHERE id = $2`,
		seatCount, flightID)
	if err != nil {
		return Reservation{}, err
	}
	r, err := scanReservation(tx.QueryRow(ctx, `INSERT INTO seat_reservations (booking_id, flight_id, seat_count)
		VALUES ($1, $2, $3) RETURNING `+reservationColumns, bookingID, flightID, seatCount))
	// The same booking_id was reserved on another flight at the same moment.
	if isUniqueViolation(err) {
		return Reservation{}, ErrBookingConflict
	}
	if err != nil {
		return Reservation{}, err
	}
	return r, tx.Commit(ctx)
}

// ReleaseReservation returns the seats of an ACTIVE reservation to the flight
// and marks it RELEASED in one transaction. A reservation that is no longer
// ACTIVE is returned as is.
func (s *Store) ReleaseReservation(ctx context.Context, bookingID string) (Reservation, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Reservation{}, err
	}
	defer tx.Rollback(ctx)

	var flightID int64
	err = tx.QueryRow(ctx, `SELECT flight_id FROM seat_reservations WHERE booking_id = $1`,
		bookingID).Scan(&flightID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, ErrReservationNotFound
	}
	if err != nil {
		return Reservation{}, err
	}

	// Lock the flight before the reservation, in the same order as ReserveSeats,
	// so the two operations cannot deadlock.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM flights WHERE id = $1 FOR UPDATE`, flightID); err != nil {
		return Reservation{}, err
	}
	r, err := scanReservation(tx.QueryRow(ctx,
		`SELECT `+reservationColumns+` FROM seat_reservations WHERE booking_id = $1 FOR UPDATE`, bookingID))
	if err != nil {
		return Reservation{}, err
	}
	if r.Status != "ACTIVE" {
		return r, nil
	}

	_, err = tx.Exec(ctx, `UPDATE flights SET available_seats = available_seats + $1 WHERE id = $2`,
		r.SeatCount, flightID)
	if err != nil {
		return Reservation{}, err
	}
	r, err = scanReservation(tx.QueryRow(ctx, `UPDATE seat_reservations SET status = 'RELEASED'
		WHERE booking_id = $1 RETURNING `+reservationColumns, bookingID))
	if err != nil {
		return Reservation{}, err
	}
	return r, tx.Commit(ctx)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
