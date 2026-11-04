# ER-диаграмма

Две независимые базы: `flight_db` (Flight Service) и `booking_db` (Booking Service).
Пунктирная связь `flights → bookings` логическая: базы разные, поэтому
внешнего ключа нет, `bookings.flight_id` проверяется через `GetFlight`.

```mermaid
erDiagram
    airlines ||--o{ flights : "operates"
    flights ||--o{ seat_reservations : "has"
    flights ||..o{ bookings : "flight_id, other DB"
    seat_reservations |o..|| bookings : "booking_id, other DB"

    airlines {
        char code PK "IATA, 2 chars, e.g. SU"
        varchar name "NOT NULL"
    }

    flights {
        bigint id PK
        char airline_code FK "NOT NULL"
        varchar flight_number "NOT NULL, 1-4 digits, e.g. 1234"
        char origin "NOT NULL, IATA, 3 chars"
        char destination "NOT NULL, IATA, 3 chars, != origin"
        timestamptz departure_time "NOT NULL"
        timestamptz arrival_time "NOT NULL, > departure_time"
        int total_seats "NOT NULL, > 0"
        int available_seats "NOT NULL, 0..total_seats"
        bigint price "NOT NULL, > 0, kopecks"
        flight_status status "NOT NULL, default SCHEDULED"
    }

    seat_reservations {
        uuid booking_id PK
        bigint flight_id FK "NOT NULL"
        int seat_count "NOT NULL, > 0"
        reservation_status status "NOT NULL, default ACTIVE"
        timestamptz created_at "NOT NULL, default now()"
    }

    bookings {
        uuid id PK
        varchar user_id "NOT NULL, indexed"
        bigint flight_id "NOT NULL, logical ref"
        varchar passenger_name "NOT NULL, not empty"
        varchar passenger_email "NOT NULL, contains @"
        int seat_count "NOT NULL, > 0"
        bigint total_price "NOT NULL, > 0, kopecks"
        booking_status status "NOT NULL, default CONFIRMED"
        timestamptz created_at "NOT NULL, default now()"
    }
```

## Ограничения целостности

**flight_db**

- `flight_status`: `SCHEDULED`, `DEPARTED`, `CANCELLED`, `COMPLETED`.
- `reservation_status`: `ACTIVE`, `RELEASED`, `EXPIRED`.
- `flights`: уникален `(airline_code, flight_number, дата вылета в UTC)`.
  Один номер рейса в один день — это один рейс.
- `flights`: `total_seats > 0`, `price > 0`,
  `0 <= available_seats <= total_seats`, `arrival_time > departure_time`,
  `origin <> destination`, IATA-коды — заглавные латинские буквы.
- `seat_reservations.booking_id` — первичный ключ, поэтому одному бронированию
  соответствует ровно одна резервация. На этом же держится идемпотентность
  `ReserveSeats`.

**booking_db**

- `booking_status`: `CONFIRMED`, `CANCELLED`.
- `bookings`: `seat_count > 0`, `total_price > 0`.
- Индекс по `user_id` для `GET /bookings?user_id=X`.

## Нормализация (3NF)

- Название авиакомпании вынесено в `airlines`. Код `SU` однозначно определяет
  `Aeroflot`, и при хранении названия в `flights` оно повторялось бы
  в каждом рейсе. Пользователю номер рейса отдаётся целиком:
  `airline_code || flight_number` = `SU1234`.
- Аэропорты хранятся кодом без отдельной таблицы: других атрибутов
  у аэропорта в системе нет, дублировать нечего.
- `bookings.total_price` — не производное от цены рейса, а цена, зафиксированная
  на момент бронирования. Последующее изменение цены рейса её не меняет.
- `flights.available_seats` можно вычислить как
  `total_seats - SUM(seat_count)` по активным резервациям. Колонка хранится
  по требованию задания: резервирование атомарно уменьшает её под
  `SELECT ... FOR UPDATE`, и согласованность обеспечивает транзакция.
