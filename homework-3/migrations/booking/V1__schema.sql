CREATE TYPE booking_status AS ENUM ('CONFIRMED', 'CANCELLED');

CREATE TABLE bookings (
    id UUID PRIMARY KEY,
    user_id VARCHAR(64) NOT NULL CHECK (user_id <> ''),
    -- Flight lives in Flight Service's database, so there is no foreign key.
    flight_id BIGINT NOT NULL CHECK (flight_id > 0),
    passenger_name VARCHAR(255) NOT NULL CHECK (btrim(passenger_name) <> ''),
    passenger_email VARCHAR(255) NOT NULL CHECK (passenger_email LIKE '%_@_%'),
    seat_count INT NOT NULL CHECK (seat_count > 0),
    total_price BIGINT NOT NULL CHECK (total_price > 0),
    status booking_status NOT NULL DEFAULT 'CONFIRMED',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- GET /bookings?user_id=X, newest first.
CREATE INDEX bookings_user_id_idx ON bookings (user_id, created_at DESC);
