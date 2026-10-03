package vps

import (
	"context"
	"errors"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type logs struct{ *Provider }

func (p logs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error) error {
	if q.Limit < 1 {
		return nil
	}
	var entries []provider.LogEntry
	var missing provider.LogTargetsMissing
	for _, target := range q.Targets {
		if target.Container == nil {
			return refusal.Refuse(refusal.CodeInvalid, "%s of release %s names no container, and a box runs only containers", target.App, target.Release)
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
