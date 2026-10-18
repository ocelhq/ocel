package pgmq

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ocelhq/ocel/pkg/cron"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const scheduleInterval = time.Second

func isTask(topic *contractv1.ManifestTopic) bool {
	return len(topic.GetConsumers()) == 1 && topic.GetConsumers()[0].GetExclusive()
}

func applySchedules(ctx context.Context, tx pgx.Tx, deployment Deployment) error {
	now := time.Now()
	scheduled := []string{}
	for name, topic := range deployment.Topics {
		if topic.GetCron() == "" || !isTask(topic) {
			continue
		}
		schedule, err := cron.Parse(topic.GetCron())
		if err != nil {
			return fmt.Errorf("task %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ocel.schedules (topic, cron, next_at) VALUES ($1, $2, $3)
				ON CONFLICT (topic) DO UPDATE SET cron = EXCLUDED.cron, next_at = EXCLUDED.next_at
				WHERE ocel.schedules.cron <> EXCLUDED.cron`, name, topic.GetCron(), schedule.Next(now)); err != nil {
			return fmt.Errorf("schedule task %s: %w", name, err)
		}
		scheduled = append(scheduled, name)
	}
	_, err := tx.Exec(ctx, "DELETE FROM ocel.schedules WHERE topic <> ALL($1)", scheduled)
	return err
}

func (e *Engine) fireSchedules(ctx context.Context) {
	ticker := time.NewTicker(scheduleInterval)
	defer ticker.Stop()
	for {
		if err := e.fireDueSchedules(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("fire due schedules", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *Engine) fireDueSchedules(ctx context.Context) error {
	deployment := e.current()
	return e.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT topic, cron, next_at FROM ocel.schedules WHERE next_at <= clock_timestamp() FOR UPDATE SKIP LOCKED")
		if err != nil {
			return err
		}
		type dueSchedule struct {
			topic, cron string
			at          time.Time
		}
		schedules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dueSchedule, error) {
			var entry dueSchedule
			return entry, row.Scan(&entry.topic, &entry.cron, &entry.at)
		})
		if err != nil {
			return err
		}
		for _, entry := range schedules {
			if _, deployed := deployment.Topics[entry.topic]; !deployed {
				continue
			}
			if err := e.fireSchedule(ctx, tx, entry.topic, entry.cron, entry.at); err != nil {
				return err
			}
		}
		return nil
	})
}

func (e *Engine) fireSchedule(ctx context.Context, tx pgx.Tx, name, expr string, at time.Time) error {
	schedule, err := cron.Parse(expr)
	if err != nil {
		return err
	}
	if err := e.runs.FireCron(ctx, name, at); err != nil {
		return fmt.Errorf("trigger scheduled task %s: %w", name, err)
	}
	_, err = tx.Exec(ctx, "UPDATE ocel.schedules SET next_at = $2 WHERE topic = $1", name, schedule.Next(time.Now()))
	return err
}
