package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (e *Engine) Store() provider.TaskStore { return e.store }

func (s store) ReadRun(ctx context.Context, execution string) (provider.Run, error) {
	topic, found, err := s.topicOf(ctx, execution)
	if err != nil {
		return provider.Run{}, err
	}
	if !found {
		return provider.Run{}, fmt.Errorf("run %s: %w", execution, keyvalue.ErrNotFound)
	}
	item, found, err := s.readRun(ctx, topic, execution, true)
	if err != nil {
		return provider.Run{}, err
	}
	if !found {
		return provider.Run{}, fmt.Errorf("run %s: %w", execution, keyvalue.ErrNotFound)
	}
	return item.run(), nil
}

func (s store) WriteRun(ctx context.Context, run provider.Run) (keyvalue.Revision, error) {
	if run.Revision == "" {
		next, err := keyvalue.NewRevision()
		if err != nil {
			return "", err
		}
		item := itemOf(run)
		item.Revision = string(next)
		err = s.putRun(ctx, item)
		if errors.Is(err, errConditionFailed) {
			return "", fmt.Errorf("run %s already exists: %w", run.Execution, keyvalue.ErrStale)
		}
		return next, err
	}
	item, err := s.updateRun(ctx, run.Topic, run.Execution, changeOf(run))
	if errors.Is(err, errConditionFailed) {
		return "", fmt.Errorf("run %s: %w", run.Execution, keyvalue.ErrStale)
	}
	if err != nil {
		return "", err
	}
	return keyvalue.Revision(item.Revision), nil
}

func (s store) ListRuns(ctx context.Context, filter provider.RunFilter) (provider.RunPage, error) {
	topics := s.topics
	if filter.Topic != "" {
		topics = []string{filter.Topic}
	}
	return s.listRuns(ctx, topics, filter)
}

func stampOf(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixMicro()
}

func retentionOf(run provider.Run) int64 {
	if !run.FinishedAt.IsZero() {
		return run.FinishedAt.Add(runRetention).Unix()
	}
	return retentionAfter(max(stampOf(run.DueAt), stampOf(run.CreatedAt)))
}

func itemOf(run provider.Run) runItem {
	return runItem{
		SK:         run.Execution,
		Topic:      run.Topic,
		Consumer:   run.Consumer,
		Status:     string(run.Status),
		Payload:    string(run.Payload),
		Output:     string(run.Output),
		Error:      run.Error,
		Attempts:   run.Attempts,
		Tags:       run.Tags,
		Metadata:   string(run.Metadata),
		CreatedAt:  stampOf(run.CreatedAt),
		DueAt:      stampOf(run.DueAt),
		StartedAt:  stampOf(run.StartedAt),
		FinishedAt: stampOf(run.FinishedAt),
		RunExpires: stampOf(run.ExpiresAt),
		Retention:  retentionOf(run),
	}
}

func changeOf(run provider.Run) change {
	item := itemOf(run)
	if item.Tags == nil {
		item.Tags = []string{}
	}
	c := change{
		set: map[string]any{
			"consumer":   item.Consumer,
			"status":     item.Status,
			"error":      item.Error,
			"attempts":   item.Attempts,
			"tags":       item.Tags,
			"created_at": item.CreatedAt,
			"due_at":     item.DueAt,
			"expires_at": item.Retention,
		},
		condition:  "#revision = :revision",
		conditions: map[string]any{":revision": string(run.Revision)},
	}
	for attribute, v := range map[string]string{"payload": item.Payload, "output": item.Output, "metadata": item.Metadata} {
		if v == "" {
			c.remove = append(c.remove, attribute)
		} else {
			c.set[attribute] = v
		}
	}
	for attribute, v := range map[string]int64{"started_at": item.StartedAt, "finished_at": item.FinishedAt, "run_expires_at": item.RunExpires} {
		if v == 0 {
			c.remove = append(c.remove, attribute)
		} else {
			c.set[attribute] = v
		}
	}
	return c
}
