package booking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrBookingNotFound = errors.New("booking not found")

type Booking struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"`
	FlightID       int64     `json:"flight_id"`
	PassengerName  string    `json:"passenger_name"`
	PassengerEmail string    `json:"passenger_email"`
	SeatCount      int32     `json:"seat_count"`
	TotalPrice     int64     `json:"total_price"` // kopecks
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
}

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

const bookingColumns = `id::text, user_id, flight_id, passenger_name, passenger_email,
	seat_count, total_price, status::text, created_at`

func scanBooking(row pgx.Row) (Booking, error) {
	var b Booking
	err := row.Scan(&b.ID, &b.UserID, &b.FlightID, &b.PassengerName, &b.PassengerEmail,
		&b.SeatCount, &b.TotalPrice, &b.Status, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, ErrBookingNotFound
	}
	return b, err
}

func (s *Store) Create(ctx context.Context, b Booking) (Booking, error) {
	return scanBooking(s.db.QueryRow(ctx, `INSERT INTO bookings
		(id, user_id, flight_id, passenger_name, passenger_email, seat_count, total_price)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+bookingColumns,
		b.ID, b.UserID, b.FlightID, b.PassengerName, b.PassengerEmail, b.SeatCount, b.TotalPrice))
}

func (s *Store) Get(ctx context.Context, id string) (Booking, error) {
	return scanBooking(s.db.QueryRow(ctx, `SELECT `+bookingColumns+` FROM bookings WHERE id = $1`, id))
}

func (s *Store) ListByUser(ctx context.Context, userID string) ([]Booking, error) {
	rows, err := s.db.Query(ctx, `SELECT `+bookingColumns+` FROM bookings
		WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Booking, error) {
		return scanBooking(row)
	})
}

func (s *Store) Cancel(ctx context.Context, id string) (Booking, error) {
	return scanBooking(s.db.QueryRow(ctx, `UPDATE bookings SET status = 'CANCELLED'
		WHERE id = $1 RETURNING `+bookingColumns, id))
}
