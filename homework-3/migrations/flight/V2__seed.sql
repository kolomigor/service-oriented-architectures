INSERT INTO airlines (code, name) VALUES
    ('SU', 'Aeroflot'),
    ('S7', 'S7 Airlines'),
    ('U6', 'Ural Airlines');

INSERT INTO flights
    (airline_code, flight_number, origin, destination, departure_time, arrival_time,
     total_seats, available_seats, price, status)
VALUES
    ('SU', '1234', 'SVO', 'LED', '2026-12-01 08:00+03', '2026-12-01 09:30+03', 150, 150, 650000, 'SCHEDULED'),
    ('S7', '21',   'SVO', 'LED', '2026-12-01 14:00+03', '2026-12-01 15:25+03', 120, 120, 520000, 'SCHEDULED'),
    ('U6', '301',  'SVO', 'LED', '2026-12-01 19:00+03', '2026-12-01 20:30+03', 180, 180, 480000, 'CANCELLED'),
    ('SU', '1234', 'SVO', 'LED', '2026-12-02 08:00+03', '2026-12-02 09:30+03', 150, 150, 700000, 'SCHEDULED'),
    ('SU', '6',    'LED', 'SVO', '2026-12-01 18:00+03', '2026-12-01 19:30+03', 150, 150, 610000, 'SCHEDULED'),
    ('S7', '1001', 'VKO', 'AER', '2026-12-05 10:00+03', '2026-12-05 12:20+03', 2,   1,   890000, 'SCHEDULED');
