CREATE TYPE flight_status AS ENUM ('SCHEDULED', 'DEPARTED', 'CANCELLED', 'COMPLETED');
CREATE TYPE reservation_status AS ENUM ('ACTIVE', 'RELEASED', 'EXPIRED');

CREATE TABLE airlines (
    code CHAR(2) PRIMARY KEY CHECK (code ~ '^[A-Z0-9]{2}$'),
    name VARCHAR(100) NOT NULL CHECK (name <> '')
);

CREATE TABLE flights (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    airline_code CHAR(2) NOT NULL REFERENCES airlines (code),
    flight_number VARCHAR(4) NOT NULL CHECK (flight_number ~ '^[0-9]{1,4}$'),
    origin CHAR(3) NOT NULL CHECK (origin ~ '^[A-Z]{3}$'),
    destination CHAR(3) NOT NULL CHECK (destination ~ '^[A-Z]{3}$'),
    departure_time TIMESTAMPTZ NOT NULL,
    arrival_time TIMESTAMPTZ NOT NULL,
    total_seats INT NOT NULL CHECK (total_seats > 0),
    available_seats INT NOT NULL CHECK (available_seats >= 0),
    price BIGINT NOT NULL CHECK (price > 0),
    status flight_status NOT NULL DEFAULT 'SCHEDULED',
    CHECK (origin <> destination),
    CHECK (arrival_time > departure_time),
    CHECK (available_seats <= total_seats)
);

-- One flight number per day: SU1234 on 2026-12-01 is exactly one flight.
CREATE UNIQUE INDEX flights_number_date_key
    ON flights (airline_code, flight_number, ((departure_time AT TIME ZONE 'UTC')::date));

-- SearchFlights filters by route and departure time.
CREATE INDEX flights_route_idx ON flights (origin, destination, departure_time);

CREATE TABLE seat_reservations (
    booking_id UUID PRIMARY KEY,
    flight_id BIGINT NOT NULL REFERENCES flights (id),
    seat_count INT NOT NULL CHECK (seat_count > 0),
    status reservation_status NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX seat_reservations_flight_id_idx ON seat_reservations (flight_id);
