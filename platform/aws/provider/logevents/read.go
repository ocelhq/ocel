package logevents

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/environment"
)

type Source struct {
	Function  string
	Container string
	Label     string
}

type Query struct {
	Sources      []Source
	Tier         environment.Tier
	Since, Until time.Time
	Limit        int
	Contains     string
}

type Event struct {
	Time     time.Time
	Label    string
	Instance string
	Text     string
	Failure  bool
}

const (
	minPageEvents = 1000
	maxPageEvents = 10000
)

var earliestNewestFirstSince = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func Read(ctx context.Context, logs *cloudwatchlogs.Client, lambdas *lambda.Client, query Query) ([]Event, error) {
	if err := refuseInvalidQuery(query); err != nil {
		return nil, fmt.Errorf("read log events: %w", err)
	}
	var events []Event
	for _, source := range query.Sources {
		group, err := findGroup(ctx, lambdas, source, query.Tier)
		if err != nil {
			return nil, err
		}
		newest, err := readNewestEvents(ctx, logs, group, source, query)
		if err != nil {
			return nil, err
		}
		events = append(events, newest...)
	}
	slices.SortStableFunc(events, func(a, b Event) int { return a.Time.Compare(b.Time) })
	if len(events) > query.Limit {
		events = events[len(events)-query.Limit:]
	}
	return events, nil
}

func refuseInvalidQuery(query Query) error {
	if query.Limit < 1 {
		return fmt.Errorf("limit %d is below 1", query.Limit)
	}
	if query.Since.IsZero() {
		return fmt.Errorf("the query has no since")
	}
	if query.Since.Before(earliestNewestFirstSince) {
		return fmt.Errorf("since %s is before %s, the earliest CloudWatch Logs reads newest first from", query.Since.UTC().Format(time.RFC3339), earliestNewestFirstSince.Format(time.DateOnly))
	}
	return refuseInvalidSources(query)
}

func refuseInvalidSources(query Query) error {
	for _, source := range query.Sources {
		if (source.Function == "") == (source.Container == "") {
			return fmt.Errorf("source %+v names both or neither of a function and a container", source)
		}
		if source.Container != "" && query.Tier == "" {
			return fmt.Errorf("container %s has no tier to read its log group from", source.Container)
		}
	}
	return nil
}

func readNewestEvents(ctx context.Context, logs *cloudwatchlogs.Client, group logGroup, source Source, query Query) ([]Event, error) {
	pageEvents := int32(min(max(query.Limit, minPageEvents), maxPageEvents))
	var newestFirst []Event
	var token *string
	for len(newestFirst) < query.Limit {
		page, err := logs.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName:        aws.String(group.name),
			LogStreamNamePrefix: group.streamPrefix,
			StartFromHead:       aws.Bool(false),
			Limit:               aws.Int32(pageEvents),
			FilterPattern:       quoteFilterPattern(query.Contains),
			StartTime:           aws.Int64(query.Since.UnixMilli()),
			EndTime:             toUnixMillis(query.Until),
			NextToken:           token,
		})
		if err != nil {
			return nil, fmt.Errorf("read log events of %s: %w", group.name, err)
		}
		for _, logged := range page.Events {
			if event, keep := parseLoggedEvent(group, source, logged); keep {
				newestFirst = append(newestFirst, event)
			}
		}
		if token = page.NextToken; token == nil {
			break
		}
	}
	newestFirst = newestFirst[:min(len(newestFirst), query.Limit)]
	slices.Reverse(newestFirst)
	return newestFirst, nil
}

func parseLoggedEvent(group logGroup, source Source, logged types.FilteredLogEvent) (Event, bool) {
	event, keep := group.parseLine(strings.TrimSuffix(aws.ToString(logged.Message), "\n"))
	if !keep {
		return Event{}, false
	}
	event.Time = time.UnixMilli(aws.ToInt64(logged.Timestamp)).UTC()
	event.Label = source.Label
	event.Instance = group.parseInstance(aws.ToString(logged.LogStreamName))
	return event, true
}

func quoteFilterPattern(contains string) *string {
	if contains == "" {
		return nil
	}
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(contains)
	return aws.String(`"` + quoted + `"`)
}

func toUnixMillis(bound time.Time) *int64 {
	if bound.IsZero() {
		return nil
	}
	return aws.Int64(bound.UnixMilli())
}
