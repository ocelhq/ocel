package aws

import (
	"context"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/logevents"
)

type logs struct{ *Provider }

func (p logs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error) error {
	if len(q.Targets) == 0 || q.Limit < 1 {
		return nil
	}
	events, err := logevents.Read(ctx, cloudwatchlogs.NewFromConfig(p.aws), lambda.NewFromConfig(p.aws), logevents.Query{
		Sources:  logSourcesOf(q.Targets),
		Tier:     q.Tier,
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
			Failure:  event.Failure,
		})
	}
	if len(entries) == 0 {
		return nil
	}
	return emit(entries)
}

func logSourcesOf(targets []provider.LogTarget) []logevents.Source {
	sources := make([]logevents.Source, len(targets))
	for i, target := range targets {
		sources[i].Label = strconv.Itoa(i)
		if target.Function != nil {
			sources[i].Function = target.Physical()
		} else {
			sources[i].Container = target.Physical()
		}
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
