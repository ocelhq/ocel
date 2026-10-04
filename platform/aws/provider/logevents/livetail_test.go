package logevents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

type fakeSession struct {
	events chan types.StartLiveTailResponseStream
	err    error
	mutex  sync.Mutex
	closed bool
}

func newFakeSession() *fakeSession {
	return &fakeSession{events: make(chan types.StartLiveTailResponseStream, 16)}
}

func (s *fakeSession) Events() <-chan types.StartLiveTailResponseStream { return s.events }

func (s *fakeSession) Close() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.closed = true
	return s.err
}

func (s *fakeSession) isClosed() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.closed
}

func (s *fakeSession) endWith(err error) {
	s.mutex.Lock()
	s.err = err
	s.mutex.Unlock()
	close(s.events)
}

func update(sampled bool, results ...types.LiveTailSessionLogEvent) types.StartLiveTailResponseStream {
	return &types.StartLiveTailResponseStreamMemberSessionUpdate{Value: types.LiveTailSessionUpdate{
		SessionMetadata: &types.LiveTailSessionMetadata{Sampled: sampled},
		SessionResults:  results,
	}}
}

func liveLine(group, stream, message string, at time.Time) types.LiveTailSessionLogEvent {
	return types.LiveTailSessionLogEvent{
		LogGroupIdentifier: aws.String(group),
		LogStreamName:      aws.String(stream),
		Message:            aws.String(message),
		Timestamp:          aws.Int64(at.UnixMilli()),
	}
}

type liveRun struct {
	mutex    sync.Mutex
	inputs   []*cloudwatchlogs.StartLiveTailInput
	batches  [][]Event
	notices  []Notice
	polled   [][]Source
	polledAt []time.Time
	err      error
	finished bool
}

func (r *liveRun) starts() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return len(r.inputs)
}

func (r *liveRun) polls() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return len(r.polled)
}

func (r *liveRun) returned() (error, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.err, r.finished
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("the live tail did not reach the expected state in time")
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *liveRun) noticeKinds() []NoticeKind {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	var kinds []NoticeKind
	for _, notice := range r.notices {
		kinds = append(kinds, notice.Kind)
	}
	return kinds
}

func (r *liveRun) events() []Event {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	var all []Event
	for _, batch := range r.batches {
		all = append(all, batch...)
	}
	return all
}

func runLiveTail(t *testing.T, groups []liveGroup, query Query, start func(*liveRun, *cloudwatchlogs.StartLiveTailInput) (liveSession, error)) (*liveRun, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &liveRun{}
	go func() {
		client := liveTailClient{
			startSession: func(_ context.Context, input *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
				run.mutex.Lock()
				run.inputs = append(run.inputs, input)
				run.mutex.Unlock()
				return start(run, input)
			},
			tail: func(_ context.Context, sources []Source, since time.Time) error {
				run.mutex.Lock()
				run.polled = append(run.polled, sources)
				run.polledAt = append(run.polledAt, since)
				run.mutex.Unlock()
				return nil
			},
		}
		err := liveTail(ctx, groups, query, client,
			func(events []Event) error {
				run.mutex.Lock()
				run.batches = append(run.batches, events)
				run.mutex.Unlock()
				return nil
			},
			func(notice Notice) error {
				run.mutex.Lock()
				run.notices = append(run.notices, notice)
				run.mutex.Unlock()
				return nil
			})
		run.mutex.Lock()
		run.err, run.finished = err, true
		run.mutex.Unlock()
	}()
	stop := func() {
		cancel()
		waitFor(t, func() bool { _, finished := run.returned(); return finished })
	}
	t.Cleanup(cancel)
	return run, stop
}

func idleSession(*liveRun, *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
	return newFakeSession(), nil
}

func lambdaGroups(count int) []liveGroup {
	groups := make([]liveGroup, count)
	for i := range groups {
		name := fmt.Sprintf("/aws/lambda/fn-%d", i)
		groups[i] = liveGroup{
			logGroup: logGroup{name: name, parseInstance: parseVersionAndID, parseLine: parseLambdaLine},
			source:   Source{Function: fmt.Sprintf("fn-%d", i), Label: fmt.Sprint(i)},
			arn:      "arn:aws:logs:us-east-1:123456789012:log-group:" + name,
		}
	}
	return groups
}

func containerGroup(name string) liveGroup {
	return liveGroup{
		logGroup: logGroup{name: "/ocel/containers/production", streamPrefix: aws.String(name + "/"), parseInstance: parseTaskID, parseLine: keepLine},
		source:   Source{Container: name, Label: name},
		arn:      "arn:aws:logs:us-east-1:123456789012:log-group:/ocel/containers/production",
	}
}

func TestLiveTailSplitsMoreThanTenLogGroupsAcrossSessions(t *testing.T) {
	t.Parallel()
	run, stop := runLiveTail(t, lambdaGroups(25), Query{Since: epoch}, idleSession)

	waitFor(t, func() bool { return run.starts() == 3 })
	stop()

	var sizes []int
	seen := map[string]bool{}
	for _, input := range run.inputs {
		sizes = append(sizes, len(input.LogGroupIdentifiers))
		for _, arn := range input.LogGroupIdentifiers {
			if seen[arn] {
				t.Errorf("the log group %s is in two sessions", arn)
			}
			seen[arn] = true
		}
	}
	slices.Sort(sizes)
	if want := []int{5, 10, 10}; !slices.Equal(sizes, want) {
		t.Errorf("LiveTail() started sessions of %v log groups, want %v", sizes, want)
	}
	if len(seen) != 25 {
		t.Errorf("LiveTail() tails %d log groups, want 25", len(seen))
	}
}

func TestLiveTailPutsContainersInTheirOwnSession(t *testing.T) {
	t.Parallel()
	groups := append(lambdaGroups(2), containerGroup("api-box"), containerGroup("worker-box"))
	run, stop := runLiveTail(t, groups, Query{Since: epoch}, idleSession)

	waitFor(t, func() bool { return run.starts() == 2 })
	stop()

	var lambdaSessions, containerSessions int
	for _, input := range run.inputs {
		if len(input.LogStreamNamePrefixes) == 0 {
			lambdaSessions++
			if len(input.LogGroupIdentifiers) != 2 {
				t.Errorf("the Lambda session tails %v, want both Lambda log groups", input.LogGroupIdentifiers)
			}
			continue
		}
		containerSessions++
		if len(input.LogGroupIdentifiers) != 1 {
			t.Errorf("a stream prefix is allowed with one log group, got %v", input.LogGroupIdentifiers)
		}
		if got, want := input.LogStreamNamePrefixes, []string{"api-box/", "worker-box/"}; !slices.Equal(got, want) {
			t.Errorf("the container session filters streams by %v, want %v", got, want)
		}
	}
	if lambdaSessions != 1 || containerSessions != 1 {
		t.Errorf("LiveTail() started %d Lambda sessions and %d container sessions, want 1 and 1", lambdaSessions, containerSessions)
	}
}

func TestLiveTailSplitsMoreThanAHundredContainersAcrossSessions(t *testing.T) {
	t.Parallel()
	var groups []liveGroup
	for i := range 150 {
		groups = append(groups, containerGroup(fmt.Sprintf("box-%d", i)))
	}
	run, stop := runLiveTail(t, groups, Query{Since: epoch}, idleSession)

	waitFor(t, func() bool { return run.starts() == 2 })
	stop()

	var sizes []int
	seen := map[string]bool{}
	for _, input := range run.inputs {
		if len(input.LogGroupIdentifiers) != 1 {
			t.Errorf("a stream prefix is allowed with one log group, got %v", input.LogGroupIdentifiers)
		}
		sizes = append(sizes, len(input.LogStreamNamePrefixes))
		for _, prefix := range input.LogStreamNamePrefixes {
			seen[prefix] = true
		}
	}
	slices.Sort(sizes)
	if want := []int{50, 100}; !slices.Equal(sizes, want) {
		t.Errorf("LiveTail() started sessions of %v stream prefixes, want %v", sizes, want)
	}
	if len(seen) != 150 {
		t.Errorf("LiveTail() tails %d containers, want 150", len(seen))
	}
}

func TestLiveTailEmitsAContainerSessionsEventsUnderTheContainerItsStreamNames(t *testing.T) {
	t.Parallel()
	session := newFakeSession()
	groups := []liveGroup{containerGroup("api"), containerGroup("api-box")}
	run, stop := runLiveTail(t, groups, Query{Since: epoch}, func(*liveRun, *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
		return session, nil
	})
	session.events <- update(false,
		liveLine(groups[0].arn, "api-box/app/task-2", "from api-box\n", epoch.Add(time.Second)),
		liveLine(groups[0].arn, "api/app/task-1", "from api\n", epoch.Add(2*time.Second)),
	)
	waitFor(t, func() bool { return len(run.events()) == 2 })
	stop()

	want := []Event{
		{Time: epoch.Add(time.Second).UTC(), Label: "api-box", Instance: "task-2", Text: "from api-box"},
		{Time: epoch.Add(2 * time.Second).UTC(), Label: "api", Instance: "task-1", Text: "from api"},
	}
	if got := run.events(); !slices.Equal(got, want) {
		t.Errorf("LiveTail() emitted %+v, want %+v", got, want)
	}
}

func TestLiveTailTailsALogGroupTwoSourcesShareOnceAndLabelsItsEventsForEach(t *testing.T) {
	t.Parallel()
	session := newFakeSession()
	shared := lambdaGroups(2)
	shared[1].logGroup, shared[1].arn = shared[0].logGroup, shared[0].arn
	run, stop := runLiveTail(t, shared, Query{Since: epoch}, func(*liveRun, *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
		return session, nil
	})
	waitFor(t, func() bool { return run.starts() == 1 })
	session.events <- update(false, liveLine(shared[0].arn, "2026/09/01/[$LATEST]aaa", "hello\n", epoch.Add(time.Second)))
	waitFor(t, func() bool { return len(run.events()) == 2 })
	stop()

	if got := run.inputs[0].LogGroupIdentifiers; !slices.Equal(got, []string{shared[0].arn}) {
		t.Errorf("LiveTail() tailed %v, want the shared log group once", got)
	}
	labels := []string{run.events()[0].Label, run.events()[1].Label}
	slices.Sort(labels)
	if want := []string{"0", "1"}; !slices.Equal(labels, want) {
		t.Errorf("LiveTail() labelled the shared group's event %v, want once for each source %v", labels, want)
	}
}

func TestLiveTailFallsBackToPollingAtTheSessionLimit(t *testing.T) {
	t.Parallel()
	since := epoch.Add(time.Hour)
	run, stop := runLiveTail(t, lambdaGroups(2), Query{Since: since}, func(*liveRun, *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
		return nil, &types.LimitExceededException{Message: aws.String("too many sessions")}
	})

	waitFor(t, func() bool { return run.polls() == 1 })
	stop()

	if got := run.polled[0]; len(got) != 2 || got[0].Function != "fn-0" || got[1].Function != "fn-1" {
		t.Errorf("LiveTail() polled %+v, want both Lambda sources", got)
	}
	if !run.polledAt[0].Equal(since) {
		t.Errorf("LiveTail() polled from %s, want from the query's since %s", run.polledAt[0], since)
	}
	if want := []NoticeKind{NoticeReconnected}; !slices.Equal(run.noticeKinds(), want) {
		t.Fatalf("LiveTail() reported %v, want %v", run.noticeKinds(), want)
	}
	if got, want := run.notices[0].Message, "Live Tail is at its session limit; polling instead"; got != want {
		t.Errorf("LiveTail() told the user %q, want %q", got, want)
	}
}

func TestLiveTailRestartsASessionThatTimesOutAndReportsReconnected(t *testing.T) {
	t.Parallel()
	first := newFakeSession()
	run, stop := runLiveTail(t, lambdaGroups(1), Query{Since: epoch}, func(run *liveRun, _ *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
		if run.starts() == 1 {
			return first, nil
		}
		return newFakeSession(), nil
	})
	waitFor(t, func() bool { return run.starts() == 1 })

	first.endWith(&types.SessionTimeoutException{})
	waitFor(t, func() bool { return run.starts() == 2 })
	stop()

	if want := []NoticeKind{NoticeReconnected}; !slices.Equal(run.noticeKinds(), want) {
		t.Errorf("LiveTail() reported %v after a timeout, want %v", run.noticeKinds(), want)
	}
	if !first.isClosed() {
		t.Error("LiveTail() left the timed out session open")
	}
	if err, _ := run.returned(); err != nil {
		t.Errorf("LiveTail() error = %v, want none", err)
	}
}

func TestLiveTailReportsASampledUpdateOncePerRunOfSampledUpdates(t *testing.T) {
	t.Parallel()
	session := newFakeSession()
	run, stop := runLiveTail(t, lambdaGroups(1), Query{Since: epoch}, func(*liveRun, *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
		return session, nil
	})
	session.events <- update(true)
	session.events <- update(true)
	session.events <- update(false)
	session.events <- update(true)
	waitFor(t, func() bool { return len(run.noticeKinds()) == 2 })
	stop()

	if want := []NoticeKind{NoticeSampled, NoticeSampled}; !slices.Equal(run.noticeKinds(), want) {
		t.Errorf("LiveTail() reported %v, want %v", run.noticeKinds(), want)
	}
}

func TestLiveTailEmitsEachGroupsEventsWithItsSourceAndDropsLambdaBookkeeping(t *testing.T) {
	t.Parallel()
	session := newFakeSession()
	groups := lambdaGroups(2)
	run, stop := runLiveTail(t, groups, Query{Since: epoch}, func(*liveRun, *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
		return session, nil
	})
	session.events <- update(false,
		liveLine(groups[0].name, "2026/09/01/[$LATEST]aaa", "hello\n", epoch.Add(time.Second)),
		liveLine(groups[1].arn, "2026/09/01/[$LATEST]bbb", "world\n", epoch.Add(2*time.Second)),
		liveLine(groups[0].name, "2026/09/01/[$LATEST]aaa", "START RequestId: 1 Version: $LATEST\n", epoch.Add(3*time.Second)),
		liveLine("/aws/lambda/unknown", "x", "stray\n", epoch.Add(4*time.Second)),
	)
	waitFor(t, func() bool { return len(run.events()) == 2 })
	stop()

	want := []Event{
		{Time: epoch.Add(time.Second).UTC(), Label: "0", Instance: "[$LATEST]aaa", Text: "hello"},
		{Time: epoch.Add(2 * time.Second).UTC(), Label: "1", Instance: "[$LATEST]bbb", Text: "world"},
	}
	if got := run.events(); !slices.Equal(got, want) {
		t.Errorf("LiveTail() emitted %+v, want %+v", got, want)
	}
}

func TestLiveTailSendsTheContainsTextAsTheSessionsFilterPattern(t *testing.T) {
	t.Parallel()
	run, stop := runLiveTail(t, lambdaGroups(1), Query{Since: epoch, Contains: `say "hi"`}, idleSession)

	waitFor(t, func() bool { return run.starts() == 1 })
	stop()

	if got, want := aws.ToString(run.inputs[0].LogEventFilterPattern), `"say \"hi\""`; got != want {
		t.Errorf("LiveTail() filtered by %q, want %q", got, want)
	}
}

func TestLiveTailClosesItsSessionsAndReturnsNothingWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	session := newFakeSession()
	run, stop := runLiveTail(t, lambdaGroups(1), Query{Since: epoch}, func(*liveRun, *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
		return session, nil
	})
	waitFor(t, func() bool { return run.starts() == 1 })

	stop()

	if !session.isClosed() {
		t.Error("LiveTail() left its session open after its context ended")
	}
	if err, _ := run.returned(); err != nil {
		t.Errorf("LiveTail() error = %v, want none: a cancelled tail is the normal way to stop", err)
	}
}

func TestLiveTailReturnsAStreamErrorThatIsNotATimeout(t *testing.T) {
	t.Parallel()
	session := newFakeSession()
	run, _ := runLiveTail(t, lambdaGroups(1), Query{Since: epoch}, func(*liveRun, *cloudwatchlogs.StartLiveTailInput) (liveSession, error) {
		return session, nil
	})
	session.endWith(errors.New("connection reset"))

	waitFor(t, func() bool { _, finished := run.returned(); return finished })

	if err, _ := run.returned(); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("LiveTail() error = %v, want the stream's error", err)
	}
}

func TestLiveTailRefusesAQueryWithAnUntil(t *testing.T) {
	t.Parallel()
	query := Query{Since: epoch, Until: epoch.Add(time.Hour), Sources: []Source{{Function: "web-fn"}}}
	if err := LiveTail(context.Background(), nil, nil, "123456789012", query, nil, nil); err == nil {
		t.Fatal("LiveTail() error = nil, want a refusal: a tail ends only when its context does")
	}
}
