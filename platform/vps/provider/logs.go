package vps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type logs struct{ *Provider }

func (p logs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, _ func(provider.LogNotice) error) error {
	if q.Tail {
		return p.follow(ctx, q, emit)
	}
	if q.Limit < 1 {
		return nil
	}
	var entries []provider.LogEntry
	var missing provider.LogTargetsMissing
	for _, target := range q.Targets {
		if err := refuseNonContainer(target); err != nil {
			return err
		}
		lines, err := p.host.ReadContainerLogs(ctx, target.Physical(), q.Since, q.Until, q.Limit, q.Contains)
		if errors.Is(err, host.ErrContainerMissing) {
			missing.Targets = append(missing.Targets, target)
			continue
		}
		if err != nil {
			return err
		}
		for _, line := range lines {
			stream := provider.LogStreamStdout
			if line.Stderr {
				stream = provider.LogStreamStderr
			}
			entries = append(entries, provider.LogEntry{
				Time:    line.Time,
				App:     target.App,
				Source:  target.Source,
				Release: target.Release,
				Stream:  stream,
				Message: line.Text,
			})
		}
	}
	slices.SortStableFunc(entries, func(a, b provider.LogEntry) int { return a.Time.Compare(b.Time) })
	entries = entries[len(entries)-min(len(entries), q.Limit):]
	if len(entries) > 0 {
		if err := emit(entries); err != nil {
			return err
		}
	}
	if len(missing.Targets) > 0 {
		return missing
	}
	return nil
}

func refuseNonContainer(target provider.LogTarget) error {
	if target.Container == nil {
		return refusal.Refuse(refusal.CodeInvalid, "%s of release %s names no container, and a box runs only containers", target.App, target.Release)
	}
	return nil
}

func (p logs) follow(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error) error {
	for _, target := range q.Targets {
		if err := refuseNonContainer(target); err != nil {
			return err
		}
	}
	followCtx, stop := context.WithCancel(ctx)
	defer stop()
	var (
		mu       sync.Mutex
		failure  error
		followed sync.WaitGroup
	)
	for _, target := range q.Targets {
		followed.Go(func() {
			err := p.host.FollowContainerLogs(followCtx, target.Physical(), q.Since, func(line host.Line) error {
				if line.Time.Before(q.Since) || !strings.Contains(line.Text, q.Contains) {
					return nil
				}
				stream := provider.LogStreamStdout
				if line.Stderr {
					stream = provider.LogStreamStderr
				}
				mu.Lock()
				defer mu.Unlock()
				return emit([]provider.LogEntry{{
					Time:    line.Time,
					App:     target.App,
					Source:  target.Source,
					Release: target.Release,
					Stream:  stream,
					Message: line.Text,
				}})
			})
			if err == nil || errors.Is(err, host.ErrContainerMissing) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if failure == nil && followCtx.Err() == nil {
				failure = err
				stop()
			}
		})
	}
	followed.Wait()
	return failure
}
