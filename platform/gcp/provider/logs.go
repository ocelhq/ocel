package gcp

import (
	"context"
	"strconv"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/logentries"
)

type logs struct{ *Provider }

func (p logs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error) error {
	if len(q.Targets) == 0 || q.Limit < 1 {
		return nil
	}
	resolved, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	service, err := resolved.LoggingREST()
	if err != nil {
		return err
	}
	events, err := logentries.Read(ctx, service, resolved.project, logentries.Query{
		Sources:  logSourcesOf(q.Targets),
		Since:    q.Since,
		Until:    q.Until,
		Limit:    q.Limit,
		Contains: q.Contains,
	})
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	entries := make([]provider.LogEntry, 0, len(events))
	for _, event := range events {
		target, labelled := targetOfLabel(q.Targets, event.Label)
		if !labelled {
			continue
		}
		entries = append(entries, provider.LogEntry{
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
