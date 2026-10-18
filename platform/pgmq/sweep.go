package pgmq

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	sweepInterval = time.Minute
	runRetention  = 30 * 24 * time.Hour
)

func (e *Engine) sweep(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := e.sweepOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("sweep expired runs and records", "error", err)
		}
	}
}

func (e *Engine) sweepOnce(ctx context.Context) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM ocel.records WHERE expires_at <= now()"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM ocel.runs WHERE finished_at < now() - make_interval(secs => $1)", runRetention.Seconds()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DELETE FROM ocel.runs WHERE finished_at IS NULL AND GREATEST(due_at, created_at) < now() - make_interval(secs => $1)", runRetention.Seconds())
		return err
	})
}
