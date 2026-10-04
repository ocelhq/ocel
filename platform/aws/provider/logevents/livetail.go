package logevents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"golang.org/x/sync/errgroup"
)

const (
	maxLiveTailGroups = 10

	sessionLimitMessage = "Live Tail is at its session limit; polling instead"
	sampledMessage      = "Live Tail sampled the entries it sent, so entries written meanwhile may be missing"
)

type NoticeKind int

const (
	NoticeSampled NoticeKind = iota + 1
	NoticeReconnected
)

type Notice struct {
	Kind    NoticeKind
	Message string
}

type liveSession interface {
	Events() <-chan types.StartLiveTailResponseStream
	Err() error
	Close() error
}

type startSession func(ctx context.Context, input *cloudwatchlogs.StartLiveTailInput) (liveSession, error)

type pollSources func(ctx context.Context, sources []Source, since time.Time) error

type liveGroup struct {
	logGroup
	source Source
	arn    string
}

func LiveTail(ctx context.Context, logs *cloudwatchlogs.Client, lambdas *lambda.Client, query Query, emit func([]Event) error, notice func(Notice) error) error {
	if err := refuseInvalidTailQuery(query); err != nil {
		return fmt.Errorf("live tail log events: %w", err)
	}
	groups, err := findLiveGroups(ctx, logs, lambdas, query)
	if err != nil {
		return err
	}
	var mutex sync.Mutex
	emitOne := func(events []Event) error {
		mutex.Lock()
		defer mutex.Unlock()
		return emit(events)
	}
	noticeOne := func(n Notice) error {
		mutex.Lock()
		defer mutex.Unlock()
		return notice(n)
	}
	start := func(ctx context.Context, input *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
		out, err := logs.StartLiveTail(ctx, input)
		if err != nil {
			return nil, err
		}
		return out.GetStream(), nil
	}
	poll := func(ctx context.Context, sources []Source, since time.Time) error {
		polled := query
		polled.Sources, polled.Since = sources, since
		return Tail(ctx, logs, lambdas, polled, 0, emitOne)
	}
	return liveTail(ctx, groups, query, start, poll, emitOne, noticeOne)
}

func findLiveGroups(ctx context.Context, logs *cloudwatchlogs.Client, lambdas *lambda.Client, query Query) ([]liveGroup, error) {
	arns := map[string]string{}
	groups := make([]liveGroup, 0, len(query.Sources))
	for _, source := range query.Sources {
		group, err := findGroup(ctx, lambdas, source, query.Tier)
		if err != nil {
			return nil, err
		}
		arn, known := arns[group.name]
		if !known {
			if arn, err = findGroupARN(ctx, logs, group.name); err != nil {
				return nil, err
			}
			arns[group.name] = arn
		}
		groups = append(groups, liveGroup{logGroup: group, source: source, arn: arn})
	}
	return groups, nil
}

func findGroupARN(ctx context.Context, logs *cloudwatchlogs.Client, name string) (string, error) {
	pages := cloudwatchlogs.NewDescribeLogGroupsPaginator(logs, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(name)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("find the ARN of log group %s: %w", name, err)
		}
		for _, found := range page.LogGroups {
			if aws.ToString(found.LogGroupName) == name {
				return strings.TrimSuffix(aws.ToString(found.Arn), ":*"), nil
			}
		}
	}
	return "", fmt.Errorf("find the ARN of log group %s: it does not exist", name)
}

func liveTail(ctx context.Context, groups []liveGroup, query Query, start startSession, poll pollSources, emit func([]Event) error, notice func(Notice) error) error {
	running, ctx := errgroup.WithContext(ctx)
	for _, sessionGroups := range splitIntoSessions(groups) {
		tail := &liveSessionTail{groups: sessionGroups, query: query, start: start, poll: poll, emit: emit, notice: notice, resume: query.Since}
		running.Go(func() error { return tail.run(ctx) })
	}
	return running.Wait()
}

func splitIntoSessions(groups []liveGroup) [][]liveGroup {
	var sessions [][]liveGroup
	var lambdaGroups []liveGroup
	for _, group := range groups {
		if group.streamPrefix != nil {
			sessions = append(sessions, []liveGroup{group})
			continue
		}
		lambdaGroups = append(lambdaGroups, group)
	}
	for len(lambdaGroups) > 0 {
		size := min(len(lambdaGroups), maxLiveTailGroups)
		sessions = append(sessions, lambdaGroups[:size])
		lambdaGroups = lambdaGroups[size:]
	}
	return sessions
}

type liveSessionTail struct {
	groups  []liveGroup
	query   Query
	start   startSession
	poll    pollSources
	emit    func([]Event) error
	notice  func(Notice) error
	resume  time.Time
	sampled bool
}

func (s *liveSessionTail) run(ctx context.Context) error {
	input := &cloudwatchlogs.StartLiveTailInput{LogEventFilterPattern: quoteFilterPattern(s.query.Contains)}
	sources := make([]Source, len(s.groups))
	for i, group := range s.groups {
		input.LogGroupIdentifiers = append(input.LogGroupIdentifiers, group.arn)
		if group.streamPrefix != nil {
			input.LogStreamNamePrefixes = []string{aws.ToString(group.streamPrefix)}
		}
		sources[i] = group.source
	}
	for {
		session, err := s.start(ctx, input)
		var limit *types.LimitExceededException
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.As(err, &limit):
			if err := s.notice(Notice{Kind: NoticeReconnected, Message: sessionLimitMessage}); err != nil {
				return err
			}
			return s.poll(ctx, sources, s.resume)
		case err != nil:
			return fmt.Errorf("start a Live Tail session of %s: %w", s.groups[0].name, err)
		}
		err = s.read(ctx, session)
		session.Close()
		var timeout *types.SessionTimeoutException
		switch {
		case ctx.Err() != nil:
			return nil
		case !errors.As(err, &timeout):
			return err
		}
		if err := s.notice(Notice{Kind: NoticeReconnected}); err != nil {
			return err
		}
	}
}

func (s *liveSessionTail) read(ctx context.Context, session liveSession) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case received, open := <-session.Events():
			if !open {
				if err := session.Err(); err != nil {
					return fmt.Errorf("read a Live Tail session of %s: %w", s.groups[0].name, err)
				}
				return fmt.Errorf("the Live Tail session of %s ended unexpectedly", s.groups[0].name)
			}
			if err := s.handle(received); err != nil {
				return err
			}
		}
	}
}

func (s *liveSessionTail) handle(received types.StartLiveTailResponseStream) error {
	update, isUpdate := received.(*types.StartLiveTailResponseStreamMemberSessionUpdate)
	if !isUpdate {
		return nil
	}
	sampled := update.Value.SessionMetadata != nil && update.Value.SessionMetadata.Sampled
	if sampled && !s.sampled {
		if err := s.notice(Notice{Kind: NoticeSampled, Message: sampledMessage}); err != nil {
			return err
		}
	}
	s.sampled = sampled
	var events []Event
	for _, logged := range update.Value.SessionResults {
		group, found := s.groupOf(aws.ToString(logged.LogGroupIdentifier))
		if !found {
			continue
		}
		event, keep := buildEvent(group.logGroup, group.source, aws.ToString(logged.LogStreamName), aws.ToInt64(logged.Timestamp), aws.ToString(logged.Message))
		if !keep {
			continue
		}
		if event.Time.After(s.resume) {
			s.resume = event.Time
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil
	}
	return s.emit(events)
}

func (s *liveSessionTail) groupOf(identifier string) (liveGroup, bool) {
	for _, group := range s.groups {
		if identifier == group.name || identifier == group.arn {
			return group, true
		}
	}
	return liveGroup{}, false
}
