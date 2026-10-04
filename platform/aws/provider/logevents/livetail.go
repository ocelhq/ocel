package logevents

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	maxSessionGroups   = 10
	maxSessionPrefixes = 100

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
	Close() error
}

type liveTailClient struct {
	startSession func(ctx context.Context, input *cloudwatchlogs.StartLiveTailInput) (liveSession, error)
	tail         func(ctx context.Context, sources []Source, since time.Time) error
}

type liveGroup struct {
	logGroup
	source Source
	arn    string
}

func LiveTail(ctx context.Context, logs *cloudwatchlogs.Client, lambdas *lambda.Client, account string, query Query, emit func([]Event) error, notice func(Notice) error) error {
	if err := refuseInvalidTailQuery(query); err != nil {
		return fmt.Errorf("live tail log events: %w", err)
	}
	groups, err := findLiveGroups(ctx, lambdas, logs.Options().Region, account, query)
	if err != nil {
		return err
	}
	var mutex sync.Mutex
	emitSerialized := func(events []Event) error {
		mutex.Lock()
		defer mutex.Unlock()
		return emit(events)
	}
	noticeSerialized := func(n Notice) error {
		mutex.Lock()
		defer mutex.Unlock()
		return notice(n)
	}
	client := liveTailClient{
		startSession: func(ctx context.Context, input *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
			out, err := logs.StartLiveTail(ctx, input)
			if err != nil {
				return nil, err
			}
			return out.GetStream(), nil
		},
		tail: func(ctx context.Context, sources []Source, since time.Time) error {
			polled := query
			polled.Sources, polled.Since = sources, since
			return Tail(ctx, logs, lambdas, polled, 0, emitSerialized)
		},
	}
	return liveTail(ctx, groups, query, client, emitSerialized, noticeSerialized)
}

func findLiveGroups(ctx context.Context, lambdas *lambda.Client, region, account string, query Query) ([]liveGroup, error) {
	groups := make([]liveGroup, 0, len(query.Sources))
	for _, source := range query.Sources {
		group, err := findGroup(ctx, lambdas, source, query.Tier)
		if err != nil {
			return nil, err
		}
		groups = append(groups, liveGroup{logGroup: group, source: source, arn: nameGroupARN(region, account, group.name)})
	}
	return groups, nil
}

func nameGroupARN(region, account, name string) string {
	return fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s", region, account, name)
}

func liveTail(ctx context.Context, groups []liveGroup, query Query, client liveTailClient, emit func([]Event) error, notice func(Notice) error) error {
	running, ctx := errgroup.WithContext(ctx)
	for _, sessionGroups := range splitIntoSessions(groups) {
		tail := &liveSessionTail{groups: sessionGroups, query: query, client: client, emit: emit, notice: notice}
		running.Go(func() error { return tail.run(ctx) })
	}
	return running.Wait()
}

func splitIntoSessions(groups []liveGroup) [][]liveGroup {
	var lambdaGroups []liveGroup
	var containerARNs []string
	containerGroups := map[string][]liveGroup{}
	for _, group := range groups {
		if group.streamPrefix == nil {
			lambdaGroups = append(lambdaGroups, group)
			continue
		}
		if _, known := containerGroups[group.arn]; !known {
			containerARNs = append(containerARNs, group.arn)
		}
		containerGroups[group.arn] = append(containerGroups[group.arn], group)
	}
	sessions := splitByKey(lambdaGroups, maxSessionGroups, func(group liveGroup) string { return group.arn })
	for _, arn := range containerARNs {
		sessions = append(sessions, splitByKey(containerGroups[arn], maxSessionPrefixes, func(group liveGroup) string { return aws.ToString(group.streamPrefix) })...)
	}
	return sessions
}

func splitByKey(groups []liveGroup, maxKeys int, keyOf func(liveGroup) string) [][]liveGroup {
	var sessions [][]liveGroup
	var keys []int
	sessionOf := map[string]int{}
	for _, group := range groups {
		key := keyOf(group)
		i, known := sessionOf[key]
		if !known {
			if len(sessions) == 0 || keys[len(keys)-1] == maxKeys {
				sessions, keys = append(sessions, nil), append(keys, 0)
			}
			i = len(sessions) - 1
			sessionOf[key] = i
			keys[i]++
		}
		sessions[i] = append(sessions[i], group)
	}
	return sessions
}

func buildSessionInput(groups []liveGroup, contains string) *cloudwatchlogs.StartLiveTailInput {
	input := &cloudwatchlogs.StartLiveTailInput{LogEventFilterPattern: quoteFilterPattern(contains)}
	for _, group := range groups {
		if !slices.Contains(input.LogGroupIdentifiers, group.arn) {
			input.LogGroupIdentifiers = append(input.LogGroupIdentifiers, group.arn)
		}
		if prefix := aws.ToString(group.streamPrefix); group.streamPrefix != nil && !slices.Contains(input.LogStreamNamePrefixes, prefix) {
			input.LogStreamNamePrefixes = append(input.LogStreamNamePrefixes, prefix)
		}
	}
	return input
}

type liveSessionTail struct {
	groups  []liveGroup
	query   Query
	client  liveTailClient
	emit    func([]Event) error
	notice  func(Notice) error
	newest  time.Time
	sampled bool
}

func (s *liveSessionTail) run(ctx context.Context) error {
	input := buildSessionInput(s.groups, s.query.Contains)
	sources := make([]Source, len(s.groups))
	for i, group := range s.groups {
		sources[i] = group.source
	}
	for {
		session, err := s.client.startSession(ctx, input)
		var limit *types.LimitExceededException
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.As(err, &limit):
			if err := s.notice(Notice{Kind: NoticeReconnected, Message: sessionLimitMessage}); err != nil {
				return err
			}
			return s.client.tail(ctx, sources, s.pollFrom())
		case err != nil:
			return fmt.Errorf("start a Live Tail session of %s: %w", s.groups[0].name, err)
		}
		err = s.follow(ctx, session)
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

func (s *liveSessionTail) pollFrom() time.Time {
	if next := s.newest.Add(time.Millisecond); next.After(s.query.Since) {
		return next
	}
	return s.query.Since
}

func (s *liveSessionTail) follow(ctx context.Context, session liveSession) error {
	err := s.emitUpdates(ctx, session)
	ended := session.Close()
	switch {
	case ctx.Err() != nil || err != nil:
		return err
	case ended != nil:
		return fmt.Errorf("read a Live Tail session of %s: %w", s.groups[0].name, ended)
	}
	return fmt.Errorf("the Live Tail session of %s ended unexpectedly", s.groups[0].name)
}

func (s *liveSessionTail) emitUpdates(ctx context.Context, session liveSession) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case received, open := <-session.Events():
			if !open {
				return nil
			}
			if err := s.emitUpdate(received); err != nil {
				return err
			}
		}
	}
}

func (s *liveSessionTail) emitUpdate(received types.StartLiveTailResponseStream) error {
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
		stream := aws.ToString(logged.LogStreamName)
		for _, group := range s.groupsOf(aws.ToString(logged.LogGroupIdentifier), stream) {
			event, keep := buildEvent(group.logGroup, group.source, stream, aws.ToInt64(logged.Timestamp), aws.ToString(logged.Message))
			if !keep {
				continue
			}
			if event.Time.After(s.newest) {
				s.newest = event.Time
			}
			events = append(events, event)
		}
	}
	if len(events) == 0 {
		return nil
	}
	return s.emit(events)
}

func (s *liveSessionTail) groupsOf(identifier, stream string) []liveGroup {
	var groups []liveGroup
	for _, group := range s.groups {
		if (identifier == group.name || identifier == group.arn) && strings.HasPrefix(stream, aws.ToString(group.streamPrefix)) {
			groups = append(groups, group)
		}
	}
	return groups
}
