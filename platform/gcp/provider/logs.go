package gcp

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/logentries"
)

type logs struct{ *Provider }

func (p logs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, notice func(provider.LogNotice) error) error {
	if len(q.Targets) == 0 || !q.Tail && q.Limit < 1 {
		return nil
	}
	resolved, err := p.openClients(ctx)
	if q.Tail && ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return err
	}
	query := logentries.Query{
		Sources:  logSourcesOf(q.Targets),
		Since:    q.Since,
		Until:    q.Until,
		Limit:    q.Limit,
		Contains: q.Contains,
	}
	emitEvents := func(events []logentries.Event) error {
		entries := make([]provider.LogEntry, 0, len(events))
		for _, event := range events {
			target, labelled := targetOfLabel(q.Targets, event.Label)
			if !labelled {
				continue
			}
			entries = append(entries, provider.LogEntry{
				ID:       event.ID,
				Time:     event.Time,
				App:      target.App,
				Source:   target.Source,
				Release:  target.Release,
				Instance: event.Instance,
				Message:  event.Text,
				Severity: event.Severity,
			})
		}
		if len(entries) == 0 {
			return nil
		}
		return emit(entries)
	}
	if q.Tail {
		stream, err := resolved.Logging()
		if err != nil {
			return err
		}
		return logentries.Tail(ctx, stream, resolved.project, query, emitEvents, func(suppressed logentries.Notice) error {
			return notice(noticeOf(suppressed))
		})
	}
	service, err := resolved.LoggingREST()
	if err != nil {
		return err
	}
	events, err := logentries.Read(ctx, service, resolved.project, query)
	if err != nil {
		return err
	}
	return emitEvents(events)
}

func noticeOf(suppressed logentries.Notice) provider.LogNotice {
	if suppressed.Reason == logentries.Reconnected {
		return provider.LogNotice{Kind: provider.LogReconnected, Message: "the log stream dropped and reconnected, so entries written meanwhile may be missing"}
	}
	return provider.LogNotice{Kind: provider.LogSampled, Omitted: suppressed.Count, Message: fmt.Sprintf("%d entries were left out because the log store sampled them", suppressed.Count)}
}

func logSourcesOf(targets []provider.LogTarget) []logentries.Source {
	sources := make([]logentries.Source, len(targets))
	for i, target := range targets {
		sources[i] = logentries.Source{Service: target.Physical(), Revision: target.Revision(), Label: strconv.Itoa(i)}
	}
	return sources
}

func targetOfLabel(targets []provider.LogTarget, label string) (provider.LogTarget, bool) {
	index, err := strconv.Atoi(label)
	if err != nil || index < 0 || index >= len(targets) {
		return provider.LogTarget{}, false
	}
	return targets[index], true
}
