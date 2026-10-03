package logevents

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/smithy-go"
)

const (
	defaultPollInterval  = 2 * time.Second
	maxThrottledInterval = 10 * time.Second
	lateEventWindow      = 10 * time.Second
)

func Tail(ctx context.Context, logs *cloudwatchlogs.Client, lambdas *lambda.Client, query Query, every time.Duration, emit func([]Event) error) error {
	return tail(ctx, logs, lambdas, query, every, emit, sleepFor)
}

func sleepFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type tailedGroup struct {
	logGroup
	source Source
	cursor cursor
}

type cursor struct {
	sinceMillis  int64
	newestMillis int64
	seenMillis   map[string]int64
}

func newCursor(since time.Time) cursor {
	return cursor{sinceMillis: since.UnixMilli(), newestMillis: since.UnixMilli(), seenMillis: map[string]int64{}}
}

func (c cursor) startMillis() int64 {
	return max(c.sinceMillis, c.newestMillis-lateEventWindow.Milliseconds())
}

func (c *cursor) markSeen(id string, millis int64) bool {
	if _, seen := c.seenMillis[id]; seen {
		return false
	}
	c.seenMillis[id] = millis
	c.newestMillis = max(c.newestMillis, millis)
	return true
}

func (c *cursor) forgetBeforeStart() {
	start := c.startMillis()
	maps.DeleteFunc(c.seenMillis, func(_ string, millis int64) bool { return millis < start })
}

func tail(ctx context.Context, logs *cloudwatchlogs.Client, lambdas *lambda.Client, query Query, every time.Duration, emit func([]Event) error, sleep func(context.Context, time.Duration) error) error {
	if err := refuseInvalidTailQuery(query); err != nil {
		return fmt.Errorf("tail log events: %w", err)
	}
	if every <= 0 {
		every = defaultPollInterval
	}
	var groups []*tailedGroup
	for _, source := range query.Sources {
		group, err := findGroup(ctx, lambdas, source, query.Tier)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		groups = append(groups, &tailedGroup{logGroup: group, source: source, cursor: newCursor(query.Since)})
	}
	wait := every
	for {
		err := pollGroups(ctx, logs, groups, query, emit)
		switch {
		case ctx.Err() != nil:
			return nil
		case isThrottled(err):
			wait = min(wait*2, max(maxThrottledInterval, every))
		case err != nil:
			return err
		default:
			wait = every
		}
		if err := sleep(ctx, wait); err != nil {
			return nil
		}
	}
}

func refuseInvalidTailQuery(query Query) error {
	if query.Since.IsZero() {
		return fmt.Errorf("the query has no since")
	}
	if !query.Until.IsZero() {
		return fmt.Errorf("the query has an until, and a tail ends only when its context does")
	}
	return refuseInvalidSources(query)
}

func pollGroups(ctx context.Context, logs *cloudwatchlogs.Client, groups []*tailedGroup, query Query, emit func([]Event) error) error {
	for _, group := range groups {
		if err := pollGroup(ctx, logs, group, query, emit); err != nil {
			return err
		}
	}
	return nil
}

func isThrottled(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "ThrottlingException"
}

func pollGroup(ctx context.Context, logs *cloudwatchlogs.Client, group *tailedGroup, query Query, emit func([]Event) error) error {
	pages := cloudwatchlogs.NewFilterLogEventsPaginator(logs, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:        aws.String(group.name),
		LogStreamNamePrefix: group.streamPrefix,
		StartFromHead:       aws.Bool(true),
		FilterPattern:       quoteFilterPattern(query.Contains),
		StartTime:           aws.Int64(group.cursor.startMillis()),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("tail log events of %s: %w", group.name, err)
		}
		var unseen []Event
		for _, logged := range page.Events {
			if !group.cursor.markSeen(aws.ToString(logged.EventId), aws.ToInt64(logged.Timestamp)) {
				continue
			}
			if event, keep := parseLoggedEvent(group.logGroup, group.source, logged); keep {
				unseen = append(unseen, event)
			}
		}
		group.cursor.forgetBeforeStart()
		if len(unseen) > 0 {
			if err := emit(unseen); err != nil {
				return err
			}
		}
	}
	return nil
}
