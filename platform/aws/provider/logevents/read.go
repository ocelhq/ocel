package logevents

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

type Source struct {
	Function  string
	Container string
	Label     string
}

type Query struct {
	Sources      []Source
	Tier         string
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

const maxPageEvents = 10000

func Read(ctx context.Context, logs *cloudwatchlogs.Client, lambdas *lambda.Client, q Query) ([]Event, error) {
	if q.Limit < 1 {
		return nil, fmt.Errorf("read log events: limit %d is below 1", q.Limit)
	}
	var events []Event
	for _, source := range q.Sources {
		group, err := findGroup(ctx, lambdas, source, q.Tier)
		if err != nil {
			return nil, err
		}
		newest, err := readNewest(ctx, logs, group, source, q)
		if err != nil {
			return nil, err
		}
		events = append(events, newest...)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	if len(events) > q.Limit {
		events = events[len(events)-q.Limit:]
	}
	return events, nil
}

func readNewest(ctx context.Context, logs *cloudwatchlogs.Client, group logGroup, source Source, q Query) ([]Event, error) {
	var newestFirst []Event
	var token *string
	for len(newestFirst) < q.Limit {
		out, err := logs.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName:        aws.String(group.name),
			LogStreamNamePrefix: group.streamPrefix,
			StartFromHead:       aws.Bool(false),
			Limit:               aws.Int32(int32(min(q.Limit-len(newestFirst), maxPageEvents))),
			FilterPattern:       filterPattern(q.Contains),
			StartTime:           unixMillis(q.Since),
			EndTime:             unixMillis(q.Until),
			NextToken:           token,
		})
		if err != nil {
			return nil, fmt.Errorf("read log events of %s: %w", group.name, err)
		}
		for _, e := range out.Events {
			text, failure, keep := group.read(strings.TrimSuffix(aws.ToString(e.Message), "\n"))
			if !keep {
				continue
			}
			newestFirst = append(newestFirst, Event{
				Time:     time.UnixMilli(aws.ToInt64(e.Timestamp)).UTC(),
				Label:    source.Label,
				Instance: group.instance(aws.ToString(e.LogStreamName)),
				Text:     text,
				Failure:  failure,
			})
		}
		if token = out.NextToken; token == nil {
			break
		}
	}
	slices.Reverse(newestFirst)
	return newestFirst, nil
}

func filterPattern(contains string) *string {
	if contains == "" {
		return nil
	}
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(contains)
	return aws.String(`"` + quoted + `"`)
}

func unixMillis(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	return aws.Int64(t.UnixMilli())
}
