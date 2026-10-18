package pgmq

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	sweepInterval = time.Second
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
			slog.Warn("sweep due and expired runs and records", "error", err)
		}
	}
}

func (e *Engine) sweepOnce(ctx context.Context) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := moveRuns(ctx, tx, expireMove); err != nil {
			return err
		}
		if _, err := moveRuns(ctx, tx, runMove{
			from:  []provider.RunStatus{provider.RunDelayed},
			to:    provider.RunQueued,
			where: "due_at <= clock_timestamp()",
		}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM ocel.records WHERE expires_at <= now()"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DELETE FROM ocel.runs WHERE finished_at < now() - make_interval(secs => $1)", runRetention.Seconds())
		return err
	})
}
