# Flight Booking: gRPC + Redis

Третья домашняя работа: система бронирования авиабилетов из двух сервисов на Go.

```
Client (REST) → Booking Service → (gRPC + API key) → Flight Service
                      ↓                                  ↓
                 PostgreSQL                 PostgreSQL + Redis Sentinel
```

- **Booking Service** — REST API для клиентов, хранит бронирования.
  Ходит во Flight Service через цепочку interceptor'ов:
  circuit breaker → retry → таймаут → API key.
- **Flight Service** — gRPC API, хранит рейсы и резервации мест,
  кеширует рейсы и поиск в Redis.

Цены везде в копейках (`650000` = 6500 ₽).

## Запуск

Нужен Docker с Compose. Команды выполняются из `homework-3`.

```bash
docker compose up --build -d --wait
curl -s localhost:8080/flights/1
```

Compose поднимает две PostgreSQL, Flyway для каждой (миграции и тестовые рейсы),
Redis (master, replica, 3 sentinel) и оба сервиса.

| Порт | Что |
|------|-----|
| `8080` | Booking Service, REST |
| `50051` | Flight Service, gRPC (API key: `flight-local-api-key`) |
| `5434`, `5435` | PostgreSQL рейсов и бронирований |

Остановить и удалить данные: `docker compose down -v`.

Тесты (retry и circuit breaker): `go test ./...`.
Перегенерировать код из `.proto`: `make generate`
(нужны `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc`).

Для примеров с gRPC нужен `grpcurl`: `brew install grpcurl`
или `go install github.com/fullstorydev/grpcurl/cmd/grpcurl@v1.9.3`.

## Структура

```
proto/flight/v1/flight.proto   gRPC-контракт Flight Service
gen/                           код, сгенерированный protoc
migrations/flight, booking     миграции Flyway
docs/er-diagram.md             ER-диаграмма
redis/sentinel.conf            конфиг Sentinel
cmd/flight-service             запуск Flight Service
cmd/booking-service            запуск Booking Service
internal/flight                gRPC-сервер, SQL, кеш, аутентификация
internal/booking               REST, бизнес-логика, SQL, interceptor'ы
```

## REST API

| Метод | Путь | Ответ |
|-------|------|-------|
| `GET` | `/flights?origin=SVO&destination=LED&date=2026-12-01` | рейсы `SCHEDULED`; `date` необязателен |
| `GET` | `/flights/{id}` | рейс или `404` |
| `POST` | `/bookings` | `201` и бронирование |
| `GET` | `/bookings/{id}` | бронирование или `404` |
| `POST` | `/bookings/{id}/cancel` | `200`; `409`, если уже отменено |
| `GET` | `/bookings?user_id=X` | бронирования пользователя |

Ошибки приходят как `{"error": "..."}`. Коды Flight Service переводятся так:
`INVALID_ARGUMENT` → `400`, `NOT_FOUND` → `404`,
`RESOURCE_EXHAUSTED`/`FAILED_PRECONDITION`/`ALREADY_EXISTS` → `409`,
`UNAVAILABLE` и открытый circuit breaker → `503`, `DEADLINE_EXCEEDED` → `504`.

```bash
curl -s -X POST localhost:8080/bookings -d '{
  "user_id": "u1", "flight_id": 1, "passenger_name": "Ivan Petrov",
  "passenger_email": "ivan@example.com", "seat_count": 2
}'
```

## Как проверить каждый пункт

### 1. gRPC-контракт

[`proto/flight/v1/flight.proto`](proto/flight/v1/flight.proto): `SearchFlights`, `GetFlight`,
`ReserveSeats`, `ReleaseReservation` и `UpdateFlight` (нужен для инвалидации кеша в п.7).
Даты — `google.protobuf.Timestamp`, статусы — enum, коды ошибок описаны
в комментарии к каждому методу.

```bash
grpcurl -plaintext -H 'x-api-key: flight-local-api-key' localhost:50051 describe flight.v1.FlightService
```

### 2. ER-диаграмма

[`docs/er-diagram.md`](docs/er-diagram.md) (Mermaid): ограничения целостности и разбор 3NF.
Те же ограничения заданы в миграциях через `CHECK`, `UNIQUE` и `FOREIGN KEY`.

### 3–4. Сервисы и gRPC между ними

Создание бронирования ([`internal/booking/service.go`](internal/booking/service.go)):
`GetFlight` → `ReserveSeats` → `total_price = seat_count * price` → запись в БД.
Если резервирование не удалось, бронирование не создаётся.

```bash
curl -s -X POST localhost:8080/bookings \
  -d '{"user_id":"u1","flight_id":1,"passenger_name":"A","passenger_email":"a@b.ru","seat_count":500}'
# {"error":"not enough available seats"}  — 409, бронирования нет
```

### 5. Транзакционная целостность

- `ReserveSeats` и `ReleaseReservation` ([`internal/flight/store.go`](internal/flight/store.go))
  выполняются в одной транзакции с `SELECT ... FOR UPDATE` на строке рейса.
- Если запись бронирования в БД упала после успешного `ReserveSeats`,
  Booking Service вызывает `ReleaseReservation` и возвращает места.

На рейсе 6 одно свободное место; 10 параллельных запросов:

```bash
for i in $(seq 1 10); do
  curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/bookings \
    -d '{"user_id":"race","flight_id":6,"passenger_name":"A","passenger_email":"a@b.ru","seat_count":1}' &
done | sort | uniq -c; wait
#    1 201
#    9 409
```

### 6. Аутентификация

API key в metadata `x-api-key` ([`internal/flight/auth.go`](internal/flight/auth.go)),
проверяется для всех методов, включая reflection. Ключ задаётся переменной
`FLIGHT_API_KEY` обоим контейнерам.

```bash
grpcurl -plaintext -import-path proto -proto flight/v1/flight.proto \
  -d '{"id":1}' localhost:50051 flight.v1.FlightService/GetFlight
# Code: Unauthenticated
```

### 7. Кеширование

Cache-Aside в [`internal/flight/cache.go`](internal/flight/cache.go): ключи `flight:{id}` и
`search:{origin}:{destination}:{date|any}`, TTL 5 минут (`CACHE_TTL`).
`ReserveSeats`, `ReleaseReservation` и `UpdateFlight` удаляют ключи рейса.
Если Redis недоступен, запросы идут в PostgreSQL.

```bash
curl -s -o /dev/null localhost:8080/flights/1; curl -s -o /dev/null localhost:8080/flights/1
docker compose logs flight-service | grep cache
# cache miss key=flight:1
# cache hit key=flight:1
```

### 8. Retry

[`internal/booking/interceptors.go`](internal/booking/interceptors.go): 3 попытки,
паузы 100 и 200 мс, повтор только при `UNAVAILABLE` и `DEADLINE_EXCEEDED`.
Повтор `ReserveSeats` безопасен: с тем же `booking_id` Flight Service возвращает
существующую резервацию.

```bash
docker compose pause flight-service
curl -s localhost:8080/flights/1      # 504 примерно через 6 секунд
docker compose unpause flight-service
docker compose logs booking-service | grep retrying
```

### 9. Redis Sentinel

Master, replica и 3 sentinel с кворумом 2. Flight Service использует
`redis.UniversalClient` в режиме Sentinel (`REDIS_MASTER_NAME`, `REDIS_ADDRS`).

```bash
docker compose stop redis-master
docker compose exec redis-sentinel-1 redis-cli -p 26379 sentinel get-master-addr-by-name mymaster
# через несколько секунд: redis-replica
docker compose logs flight-service | grep "new master"
docker compose start redis-master    # вернётся репликой нового master
```

### 10. Circuit Breaker

[`internal/booking/breaker.go`](internal/booking/breaker.go): открывается после 5 сбоев
(`UNAVAILABLE`, `DEADLINE_EXCEEDED`, `INTERNAL`) за 10 секунд, через 15 секунд
пропускает один пробный запрос. Параметры: `CB_FAILURE_THRESHOLD`, `CB_WINDOW`,
`CB_OPEN_TIMEOUT`.

```bash
docker compose pause flight-service
for i in 1 2 3 4 5; do curl -s -o /dev/null localhost:8080/flights/1 & done; wait
curl -s -w ' %{http_code}\n' localhost:8080/flights/1
# {"error":"flight service is temporarily unavailable, try again later"} 503 — сразу
docker compose unpause flight-service
sleep 15; curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/flights/1   # 200
docker compose logs booking-service | grep "circuit breaker"
# from=CLOSED to=OPEN, from=OPEN to=HALF_OPEN, from=HALF_OPEN to=CLOSED
```
