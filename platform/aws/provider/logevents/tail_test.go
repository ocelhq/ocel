package logevents

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/environment"
)

type tailRun struct {
	batches [][]Event
	sleeps  []time.Duration
	err     error
}

func (r tailRun) batchTexts() string {
	var joined []string
	for _, batch := range r.batches {
		joined = append(joined, strings.Join(collectTexts(batch), ","))
	}
	return strings.Join(joined, "|")
}

func runTail(t *testing.T, logs *cloudwatchlogs.Client, lambdas *lambda.Client, query Query, every time.Duration, between ...func()) tailRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var run tailRun
	sleep := func(ctx context.Context, d time.Duration) error {
		run.sleeps = append(run.sleeps, d)
		if len(run.sleeps) > len(between) {
			cancel()
			return ctx.Err()
		}
		between[len(run.sleeps)-1]()
		return nil
	}
	run.err = tail(ctx, logs, lambdas, query, every, func(events []Event) error {
		run.batches = append(run.batches, events)
		return nil
	}, sleep)
	return run
}

func lambdaFunction(t *testing.T) (*fakeAWS, *cloudwatchlogs.Client, *lambda.Client) {
	t.Helper()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	return fake, logs, lambdas
}

const webGroup = "/aws/lambda/web-fn-1"

func webLine(seconds int, text string) storedEvent {
	return storedEvent{Stream: lambdaStream, Time: addToEpoch(seconds), Message: text + "\n"}
}

func TestTailEmitsOnlyEventsNewerThanTheLastPoll(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.add(webGroup, webLine(1, "one"), webLine(2, "two"))

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, time.Second,
		func() { fake.add(webGroup, webLine(3, "three")) },
	)

	if run.err != nil {
		t.Fatalf("Tail() error = %v", run.err)
	}
	if got, want := run.batchTexts(), "one,two|three"; got != want {
		t.Fatalf("Tail() emitted batches %q, want %q", got, want)
	}
}

func TestTailSkipsAnEventSeenAtTheSameTimestamp(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.add(webGroup, webLine(1, "first"))

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, time.Second,
		func() { fake.add(webGroup, webLine(1, "second")) },
	)

	if run.err != nil {
		t.Fatalf("Tail() error = %v", run.err)
	}
	if got, want := run.batchTexts(), "first|second"; got != want {
		t.Fatalf("Tail() emitted batches %q, want %q: the late event shares a timestamp with one already emitted and must still arrive once", got, want)
	}
}

func TestTailBacksOffWhenThrottled(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.add(webGroup, webLine(1, "one"))
	fake.throttles = 4

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, 2*time.Second, idle(6)...)

	if run.err != nil {
		t.Fatalf("Tail() error = %v", run.err)
	}
	want := []time.Duration{4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second}
	if !slices.Equal(run.sleeps, want) {
		t.Fatalf("Tail() slept %v, want %v: doubling up to 10s while throttled, back to 2s after a poll succeeds", run.sleeps, want)
	}
	if got := run.batchTexts(); got != "one" {
		t.Fatalf("Tail() emitted batches %q, want the event once, after the throttling ended", got)
	}
}

func TestTailStopsWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.add(webGroup, webLine(1, "one"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := Tail(ctx, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, time.Hour, func([]Event) error {
		cancel()
		return nil
	})

	if err != nil {
		t.Fatalf("Tail() error = %v, want nil once the context is cancelled", err)
	}
}

func TestTailReturnsNilWhenTheContextIsCancelledDuringARequest(t *testing.T) {
	t.Parallel()
	_, logs, lambdas := lambdaFunction(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Tail(ctx, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, time.Hour, func([]Event) error { return nil })

	if err != nil {
		t.Fatalf("Tail() error = %v, want nil for a context cancelled before the first request completes", err)
	}
}

func TestTailPollsEveryTwoSecondsByDefault(t *testing.T) {
	t.Parallel()
	_, logs, lambdas := lambdaFunction(t)

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, 0, idle(1)...)

	if want := []time.Duration{2 * time.Second, 2 * time.Second}; !slices.Equal(run.sleeps, want) {
		t.Fatalf("Tail() slept %v, want %v", run.sleeps, want)
	}
}

func TestTailEmitsNothingForAPollWithNoNewEvents(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.add(webGroup, webLine(1, "one"))

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, time.Second, idle(3)...)

	if got := run.batchTexts(); got != "one" {
		t.Fatalf("Tail() emitted batches %q, want one batch: later polls found nothing", got)
	}
}

func TestTailAsksOnlyForEventsFromSinceOnwardsOldestFirst(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.add(webGroup, webLine(-5, "before since"), webLine(1, "after since"))

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}, Contains: `say "hi"`}, time.Second)

	if got := run.batchTexts(); got != "after since" {
		t.Fatalf("Tail() emitted batches %q, want only the event from since onwards", got)
	}
	sent := fake.listRequests()
	if len(sent) != 1 || sent[0].StartFromHead == nil || !*sent[0].StartFromHead || sent[0].EndTime != nil || sent[0].FilterPattern != `"say \"hi\""` {
		t.Fatalf("requests = %+v, want one oldest-first request with the quoted filter pattern and no end time", sent)
	}
}

func TestTailPagesThroughEveryPageOfAPoll(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.pageSize = 2
	for i := 1; i <= 5; i++ {
		fake.add(webGroup, webLine(i, "line "+strconv.Itoa(i)))
	}

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, time.Second, func() { fake.add(webGroup, webLine(6, "line 6")) })

	if got, want := run.batchTexts(), "line 1,line 2,line 3,line 4,line 5|line 6"; got != want {
		t.Fatalf("Tail() emitted batches %q, want %q", got, want)
	}
}

func TestTailMergesTheGroupsOfASourceSetByTime(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.lambdas["api-fn"] = "/aws/lambda/api-fn-2"
	fake.add(webGroup, webLine(1, "web 1"), webLine(4, "web 4"))
	fake.add("/aws/lambda/api-fn-2", webLine(2, "api 2"), webLine(3, "api 3"))
	fake.add("/ocel/containers/production", storedEvent{Stream: "svc-c/app/t1", Time: addToEpoch(5), Message: "svc 5\n"})

	run := runTail(t, logs, lambdas, Query{
		Since:   epoch,
		Sources: []Source{{Function: "web-fn", Label: "web"}, {Function: "api-fn", Label: "api"}, {Container: "svc-c", Label: "svc"}},
		Tier:    environment.TierProduction,
	}, time.Second, func() { fake.add("/aws/lambda/api-fn-2", webLine(6, "api 6")) })

	if got, want := run.batchTexts(), "web 1,api 2,api 3,web 4,svc 5|api 6"; got != want {
		t.Fatalf("Tail() emitted batches %q, want %q", got, want)
	}
	if first := run.batches[0]; first[1].Label != "api" || first[4].Label != "svc" || first[4].Instance != "t1" {
		t.Errorf("first batch = %+v, want each event labelled by its source", first)
	}
}

func TestTailTracksEachGroupsPositionSeparately(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.lambdas["api-fn"] = "/aws/lambda/api-fn-2"
	fake.add(webGroup, webLine(10, "web 10"))
	fake.add("/aws/lambda/api-fn-2", webLine(1, "api 1"))

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}, {Function: "api-fn"}}}, time.Second,
		func() { fake.add("/aws/lambda/api-fn-2", webLine(5, "api 5")) },
	)

	if got, want := run.batchTexts(), "api 1,web 10|api 5"; got != want {
		t.Fatalf("Tail() emitted batches %q, want %q: a quiet group is not skipped past because another has newer events", got, want)
	}
}

func TestTailDropsLambdaBookkeepingLinesButStillAdvancesPastThem(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.add(webGroup, webLine(1, "START RequestId: abc Version: $LATEST"), webLine(2, "hello"))

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, time.Second, idle(1)...)

	if got := run.batchTexts(); got != "hello" {
		t.Fatalf("Tail() emitted batches %q, want only the app line, once", got)
	}
}

func TestTailRetriesAThrottledPollWithoutLosingEvents(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.lambdas["api-fn"] = "/aws/lambda/api-fn-2"
	fake.add(webGroup, webLine(1, "web 1"))
	fake.add("/aws/lambda/api-fn-2", webLine(2, "api 2"))

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}, {Function: "api-fn"}}}, time.Second,
		func() { fake.throttles = 1 },
		func() {},
	)

	if got, want := run.batchTexts(), "web 1,api 2"; got != want {
		t.Fatalf("Tail() emitted batches %q, want %q: a poll that failed halfway re-reads its groups, and no group's events are emitted twice", got, want)
	}
}

func TestTailStopsOnAnErrorThatIsNotThrottling(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.logsError = "AccessDeniedException"

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, time.Second, idle(1)...)

	if run.err == nil || !strings.Contains(run.err.Error(), webGroup) || !strings.Contains(run.err.Error(), "AccessDeniedException") {
		t.Fatalf("Tail() error = %v, want it to name the log group and carry the AWS error", run.err)
	}
	if len(run.sleeps) != 0 {
		t.Errorf("Tail() slept %v, want no retry of an error that will never succeed", run.sleeps)
	}
}

func TestTailStopsOnAnErrorFromLambda(t *testing.T) {
	t.Parallel()
	_, logs, lambdas := newFakeAWS(t)

	run := runTail(t, logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "gone-fn"}}}, time.Second)

	if run.err == nil || !strings.Contains(run.err.Error(), "gone-fn") || !strings.Contains(run.err.Error(), "ResourceNotFoundException") {
		t.Fatalf("Tail() error = %v, want it to name the function and carry the AWS error", run.err)
	}
}

func TestTailStopsWhenEmitFails(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := lambdaFunction(t)
	fake.add(webGroup, webLine(1, "one"))
	refused := errors.New("terminal closed")

	err := tail(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}}, time.Second,
		func([]Event) error { return refused },
		func(context.Context, time.Duration) error { t.Fatal("slept after emit failed"); return nil },
	)

	if !errors.Is(err, refused) {
		t.Fatalf("Tail() error = %v, want the error emit returned", err)
	}
}

func TestTailRefusesAnInvalidQuery(t *testing.T) {
	t.Parallel()
	_, logs, lambdas := lambdaFunction(t)
	web := []Source{{Function: "web-fn"}}
	tests := map[string]Query{
		"no since":                 {Sources: web},
		"an until":                 {Since: epoch, Until: addToEpoch(60), Sources: web},
		"a source with neither":    {Since: epoch, Sources: []Source{{}}},
		"a source with both":       {Since: epoch, Sources: []Source{{Function: "web-fn", Container: "svc-c"}}},
		"a container with no tier": {Since: epoch, Sources: []Source{{Container: "svc-c"}}},
	}
	for name, query := range tests {
		err := Tail(context.Background(), logs, lambdas, query, time.Second, func([]Event) error { return nil })
		if err == nil {
			t.Errorf("Tail() with %s: error = nil, want the query refused", name)
		}
	}
}

func idle(polls int) []func() {
	steps := make([]func(), polls)
	for i := range steps {
		steps[i] = func() {}
	}
	return steps
}
