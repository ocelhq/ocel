package pgmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ocelhq/ocel/pkg/cron"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const scheduleInterval = time.Second

type scheduledPayload struct {
	Timestamp string `json:"timestamp"`
}

func (e *Engine) applySchedules(ctx context.Context, deployment Deployment) error {
	now := time.Now()
	scheduled := []string{}
	return e.inTx(ctx, func(tx pgx.Tx) error {
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
	})
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
	var fired []string
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT topic, cron, next_at FROM ocel.schedules WHERE next_at <= clock_timestamp() FOR UPDATE SKIP LOCKED")
		if err != nil {
			return err
		}
		type due struct {
			topic, cron string
			at          time.Time
		}
		schedules, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
			var d due
			return d, r.Scan(&d.topic, &d.cron, &d.at)
		})
		if err != nil {
			return err
		}
		for _, d := range schedules {
			topic, deployed := deployment.Topics[d.topic]
			if !deployed {
				continue
			}
			if err := e.fireSchedule(ctx, tx, d.topic, topic, d.cron, d.at); err != nil {
				return err
			}
			fired = append(fired, d.topic)
		}
		return nil
	})
	for _, name := range fired {
		e.signalTopic(name, deployment.Topics[name])
	}
	return err
}

func (e *Engine) fireSchedule(ctx context.Context, tx pgx.Tx, name string, topic *contractv1.ManifestTopic, expr string, at time.Time) error {
	schedule, err := cron.Parse(expr)
	if err != nil {
		return err
	}
	stamp := at.UTC().Format(time.RFC3339)
	payload, err := json.Marshal(scheduledPayload{Timestamp: stamp})
	if err != nil {
		return err
	}
	if _, err := e.Tasks().trigger(ctx, tx, name, topic, payload, &taskv1.TriggerOptions{IdempotencyKey: "cron-" + stamp}); err != nil {
		return fmt.Errorf("trigger scheduled task %s: %w", name, err)
	}
	_, err = tx.Exec(ctx, "UPDATE ocel.schedules SET next_at = $2 WHERE topic = $1", name, schedule.Next(time.Now()))
	return err
}
