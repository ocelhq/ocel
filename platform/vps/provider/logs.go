package vps

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type logs struct{ *Provider }

func (p logs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, notice func(provider.LogNotice) error) error {
	if q.Tail {
		return p.tail(ctx, q, emit, notice)
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
			entries = append(entries, logEntryOf(target, line))
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

func (p logs) tail(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, notice func(provider.LogNotice) error) error {
	if len(q.Targets) == 0 {
		return nil
	}
	names := make([]string, len(q.Targets))
	for i, target := range q.Targets {
		if err := refuseNonContainer(target); err != nil {
			return err
		}
		names[i] = target.Physical()
	}
	err := p.host.FollowContainerLogs(ctx, names, q.Since, func(container int, line host.Line) error {
		if !strings.Contains(line.Text, q.Contains) {
			return nil
		}
		return emit([]provider.LogEntry{logEntryOf(q.Targets[container], line)})
	}, func(container int) error {
		return notice(provider.LogNotice{Kind: provider.LogSourceGone, Target: q.Targets[container]})
	})
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func logEntryOf(target provider.LogTarget, line host.Line) provider.LogEntry {
	stream := provider.LogStreamStdout
	if line.Stderr {
		stream = provider.LogStreamStderr
	}
	return provider.LogEntry{
		Time:    line.Time,
		App:     target.App,
		Source:  target.Source,
		Release: target.Release,
		Stream:  stream,
		Message: line.Text,
	}
}
