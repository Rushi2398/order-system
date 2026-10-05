package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a connection pool and blocks (with retries) until the
// database actually answers a ping. Waiting here matters because
// docker-compose's `service_healthy` dependency only guarantees Postgres
// accepted a TCP connection at some point during its healthcheck -- there
// can still be a brief window on startup where order-service's own
// connection attempt races ahead of that. Retrying a few times is cheaper
// than a crash-loop.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database url: %w", err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	var pingErr error

	for attempt := 1; attempt <= 5; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		pingErr = pool.Ping(pingCtx)
		cancel()
		if pingErr == nil {
			return pool, nil
		}
		// Don't wait after the final attempt.
		if attempt == 5 {
			break
		}

		// Exponential backoff: 1s, 2s, 4s, 8s
		backoff := time.Duration(1<<uint(attempt-1)) * time.Second
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
			// Backoff completed; continue to next attempt.
		case <-ctx.Done():
			timer.Stop()
			pool.Close()
			return nil, ctx.Err()
		}
	}

	pool.Close()
	return nil, fmt.Errorf("database not reachable after retries: %w", pingErr)
}
