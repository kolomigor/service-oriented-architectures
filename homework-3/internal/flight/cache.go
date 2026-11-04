package flight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// CachedStore wraps Store with a Redis cache (cache-aside): reads go to Redis
// first, mutations go to the database and then delete the affected keys.
// If Redis is unavailable, everything still works through the database.
type CachedStore struct {
	store *Store
	rdb   redis.UniversalClient
	ttl   time.Duration
}

func NewCachedStore(store *Store, rdb redis.UniversalClient, ttl time.Duration) *CachedStore {
	return &CachedStore{store: store, rdb: rdb, ttl: ttl}
}

func flightKey(id int64) string {
	return fmt.Sprintf("flight:%d", id)
}

func searchKey(origin, destination string, day *time.Time) string {
	date := "any"
	if day != nil {
		date = day.UTC().Format(time.DateOnly)
	}
	return fmt.Sprintf("search:%s:%s:%s", origin, destination, date)
}

func (c *CachedStore) SearchFlights(ctx context.Context, origin, destination string, day *time.Time) ([]Flight, error) {
	return cached(ctx, c, searchKey(origin, destination, day), func() ([]Flight, error) {
		return c.store.SearchFlights(ctx, origin, destination, day)
	})
}

func (c *CachedStore) GetFlight(ctx context.Context, id int64) (Flight, error) {
	return cached(ctx, c, flightKey(id), func() (Flight, error) {
		return c.store.GetFlight(ctx, id)
	})
}

func (c *CachedStore) ReserveSeats(ctx context.Context, flightID int64, seatCount int32, bookingID string) (Reservation, error) {
	r, err := c.store.ReserveSeats(ctx, flightID, seatCount, bookingID)
	if err == nil {
		c.invalidateFlight(ctx, flightID)
	}
	return r, err
}

func (c *CachedStore) ReleaseReservation(ctx context.Context, bookingID string) (Reservation, error) {
	r, err := c.store.ReleaseReservation(ctx, bookingID)
	if err == nil {
		c.invalidateFlight(ctx, r.FlightID)
	}
	return r, err
}

func (c *CachedStore) UpdateFlight(ctx context.Context, id int64, price *int64, status *string) (Flight, error) {
	f, err := c.store.UpdateFlight(ctx, id, price, status)
	if err == nil {
		c.deleteKeys(ctx, flightKeys(f))
	}
	return f, err
}

// cached returns the value under key from Redis. On a miss it loads the value
// from the database and puts it into Redis with the TTL.
func cached[T any](ctx context.Context, c *CachedStore, key string, load func() (T, error)) (T, error) {
	var v T
	data, err := c.rdb.Get(ctx, key).Bytes()
	switch {
	case err == nil && json.Unmarshal(data, &v) == nil:
		slog.Info("cache hit", "key", key)
		return v, nil
	case err != nil && !errors.Is(err, redis.Nil):
		slog.Warn("cache read failed, using database", "key", key, "err", err)
	default:
		slog.Info("cache miss", "key", key)
	}

	v, err = load()
	if err != nil {
		return v, err
	}
	data, err = json.Marshal(v)
	if err == nil {
		err = c.rdb.Set(ctx, key, data, c.ttl).Err()
	}
	if err != nil {
		slog.Warn("cache write failed", "key", key, "err", err)
	}
	return v, nil
}

// flightKeys lists every key whose value contains the flight: the flight
// itself and the searches by its route, with its date and without a date.
func flightKeys(f Flight) []string {
	day := f.DepartureTime
	return []string{
		flightKey(f.ID),
		searchKey(f.Origin, f.Destination, &day),
		searchKey(f.Origin, f.Destination, nil),
	}
}

// invalidateFlight is called after the flight's seats changed. It reads the
// flight's route from the database to know which search keys to delete.
func (c *CachedStore) invalidateFlight(ctx context.Context, id int64) {
	f, err := c.store.GetFlight(context.WithoutCancel(ctx), id)
	if err != nil {
		slog.Error("load flight for cache invalidation", "flight_id", id, "err", err)
		c.deleteKeys(ctx, []string{flightKey(id)})
		return
	}
	c.deleteKeys(ctx, flightKeys(f))
}

func (c *CachedStore) deleteKeys(ctx context.Context, keys []string) {
	// The data is already changed in the database, so the keys must be
	// deleted even if the client has gone away.
	if err := c.rdb.Del(context.WithoutCancel(ctx), keys...).Err(); err != nil {
		slog.Error("cache invalidation failed", "keys", keys, "err", err)
		return
	}
	slog.Info("cache invalidated", "keys", keys)
}
