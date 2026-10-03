package logevents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/environment"
)

type storedEvent struct {
	Stream  string
	Time    time.Time
	Message string
}

type filterRequest struct {
	LogGroupName        string `json:"logGroupName"`
	LogStreamNamePrefix string `json:"logStreamNamePrefix"`
	FilterPattern       string `json:"filterPattern"`
	StartTime           *int64 `json:"startTime"`
	EndTime             *int64 `json:"endTime"`
	StartFromHead       *bool  `json:"startFromHead"`
	Limit               *int32 `json:"limit"`
	NextToken           string `json:"nextToken"`
}

type fakeAWS struct {
	mutex     sync.Mutex
	groups    map[string][]storedEvent
	lambdas   map[string]string
	pageSize  int
	requests  []filterRequest
	logsError string
	emptyPage bool
	throttles int
}

func newFakeAWS(t *testing.T) (*fakeAWS, *cloudwatchlogs.Client, *lambda.Client) {
	t.Helper()
	fake := &fakeAWS{groups: map[string][]storedEvent{}, lambdas: map[string]string{}, pageSize: 100}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	cfg := aws.Config{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(server.URL),
		Credentials:      credentials.NewStaticCredentialsProvider("id", "secret", ""),
		RetryMaxAttempts: 1,
	}
	return fake, cloudwatchlogs.NewFromConfig(cfg), lambda.NewFromConfig(cfg)
}

func (f *fakeAWS) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if name, ok := strings.CutPrefix(req.URL.Path, "/2015-03-31/functions/"); ok {
		name = strings.TrimSuffix(name, "/configuration")
		group, found := f.lambdas[name]
		if !found {
			writer.Header().Set("X-Amzn-Errortype", "ResourceNotFoundException")
			writer.WriteHeader(http.StatusNotFound)
			io.WriteString(writer, `{"Message":"no such function"}`)
			return
		}
		json.NewEncoder(writer).Encode(map[string]any{"FunctionName": name, "LoggingConfig": map[string]any{"LogGroup": group, "LogFormat": "Text"}})
		return
	}
	if f.logsError != "" {
		writer.Header().Set("X-Amzn-Errortype", f.logsError)
		writer.WriteHeader(http.StatusBadRequest)
		io.WriteString(writer, `{"message":"refused"}`)
		return
	}
	var filter filterRequest
	json.NewDecoder(req.Body).Decode(&filter)
	f.requests = append(f.requests, filter)
	if f.throttles > 0 {
		f.throttles--
		writer.Header().Set("X-Amzn-Errortype", "ThrottlingException")
		writer.WriteHeader(http.StatusBadRequest)
		io.WriteString(writer, `{"message":"Rate exceeded"}`)
		return
	}

	if f.emptyPage && filter.NextToken == "" {
		json.NewEncoder(writer).Encode(map[string]any{"events": []any{}, "nextToken": "0"})
		return
	}
	type numbered struct {
		storedEvent
		id string
	}
	var matched []numbered
	for i, stored := range f.groups[filter.LogGroupName] {
		if filter.LogStreamNamePrefix != "" && !strings.HasPrefix(stored.Stream, filter.LogStreamNamePrefix) {
			continue
		}
		if filter.StartTime != nil && stored.Time.UnixMilli() < *filter.StartTime {
			continue
		}
		if filter.EndTime != nil && stored.Time.UnixMilli() > *filter.EndTime {
			continue
		}
		matched = append(matched, numbered{stored, filter.LogGroupName + "#" + strconv.Itoa(i)})
	}
	if filter.StartFromHead != nil && *filter.StartFromHead {
		slices.SortStableFunc(matched, func(a, b numbered) int { return a.Time.Compare(b.Time) })
	} else {
		slices.SortStableFunc(matched, func(a, b numbered) int { return b.Time.Compare(a.Time) })
	}
	offset := 0
	if filter.NextToken != "" {
		offset, _ = strconv.Atoi(filter.NextToken)
	}
	size := f.pageSize
	if filter.Limit != nil && int(*filter.Limit) < size {
		size = int(*filter.Limit)
	}
	end := min(offset+size, len(matched))
	events := []map[string]any{}
	for _, stored := range matched[offset:end] {
		events = append(events, map[string]any{"eventId": stored.id, "logStreamName": stored.Stream, "timestamp": stored.Time.UnixMilli(), "message": stored.Message})
	}
	body := map[string]any{"events": events}
	if end < len(matched) {
		body["nextToken"] = strconv.Itoa(end)
	}
	json.NewEncoder(writer).Encode(body)
}

func (f *fakeAWS) add(group string, events ...storedEvent) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.groups[group] = append(f.groups[group], events...)
}

func (f *fakeAWS) listRequests() []filterRequest {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return append([]filterRequest(nil), f.requests...)
}

var epoch = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func addToEpoch(seconds int) time.Time { return epoch.Add(time.Duration(seconds) * time.Second) }

func collectTexts(events []Event) []string {
	texts := make([]string, len(events))
	for i, event := range events {
		texts[i] = event.Text
	}
	return texts
}

func TestReadFindsALambdasLogGroupFromItsConfiguration(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1a2b3c4"
	fake.groups["/aws/lambda/web-fn-1a2b3c4"] = []storedEvent{{Stream: "2026/09/01/[$LATEST]abc", Time: addToEpoch(1), Message: "hello\n"}}

	events, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn", Label: "web"}}, Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := collectTexts(events); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("Read() texts = %q, want the line from the group named in the function's configuration", got)
	}
}

func TestReadAsksForContainerStreamsByThePhysicalPrefix(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.groups["/ocel/containers/production"] = []storedEvent{
		{Stream: "web-c1/app/9f8e7d6c", Time: addToEpoch(1), Message: "mine\n"},
		{Stream: "web-c10/app/aaaa", Time: addToEpoch(2), Message: "a sibling whose name starts the same\n"},
	}

	events, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Container: "web-c1", Label: "web"}}, Tier: environment.TierProduction, Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	sent := fake.listRequests()
	if len(sent) != 1 || sent[0].LogGroupName != "/ocel/containers/production" || sent[0].LogStreamNamePrefix != "web-c1/" {
		t.Fatalf("requests = %+v, want one against group /ocel/containers/production with stream prefix web-c1/", sent)
	}
	if len(events) != 1 || events[0].Text != "mine" || events[0].Instance != "9f8e7d6c" || events[0].Label != "web" {
		t.Fatalf("Read() = %+v, want only web-c1's line, on task 9f8e7d6c, labelled web", events)
	}
}

func TestReadMergesEventsFromSeveralLogGroupsByTime(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	fake.lambdas["api-fn"] = "/aws/lambda/api-fn-2"
	fake.groups["/aws/lambda/web-fn-1"] = []storedEvent{
		{Stream: "2026/09/01/[$LATEST]w", Time: addToEpoch(1), Message: "web 1\n"},
		{Stream: "2026/09/01/[$LATEST]w", Time: addToEpoch(4), Message: "web 4\n"},
	}
	fake.groups["/aws/lambda/api-fn-2"] = []storedEvent{
		{Stream: "2026/09/01/[$LATEST]a", Time: addToEpoch(2), Message: "api 2\n"},
		{Stream: "2026/09/01/[$LATEST]a", Time: addToEpoch(3), Message: "api 3\n"},
	}
	fake.groups["/ocel/containers/production"] = []storedEvent{{Stream: "svc-c/app/t1", Time: addToEpoch(5), Message: "svc 5\n"}}

	events, err := Read(context.Background(), logs, lambdas, Query{
		Since:   epoch,
		Sources: []Source{{Function: "web-fn", Label: "web"}, {Function: "api-fn", Label: "api"}, {Container: "svc-c", Label: "svc"}},
		Tier:    environment.TierProduction,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := []string{"web 1", "api 2", "api 3", "web 4", "svc 5"}
	if got := collectTexts(events); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Read() texts = %q, want %q", got, want)
	}
	if events[1].Label != "api" || events[4].Label != "svc" {
		t.Errorf("labels = %q, %q, want each event to carry its source's label", events[1].Label, events[4].Label)
	}
}

func TestReadReturnsTheNewestLimitEventsOldestFirst(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.pageSize = 2
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	for i := 1; i <= 7; i++ {
		fake.groups["/aws/lambda/web-fn-1"] = append(fake.groups["/aws/lambda/web-fn-1"], storedEvent{Stream: "2026/09/01/[$LATEST]w", Time: addToEpoch(i), Message: "line " + strconv.Itoa(i) + "\n"})
	}

	events, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}, Limit: 5})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := []string{"line 3", "line 4", "line 5", "line 6", "line 7"}
	if got := collectTexts(events); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Read() texts = %q, want the newest 5 oldest first, paged through a 2-event page size", got)
	}
	sent := fake.listRequests()
	if len(sent) != 3 {
		t.Errorf("requests = %d, want 3 pages (2+2+2) and no more once the limit is met", len(sent))
	}
	if sent[0].StartFromHead == nil || *sent[0].StartFromHead {
		t.Errorf("first request StartFromHead = %v, want false so the newest events come first", sent[0].StartFromHead)
	}
}

const lambdaStream = "2026/09/01/[$LATEST]f7108dfca11d48d8ba7321a9b4fe4b02"

func readLambdaLines(t *testing.T, messages ...string) []Event {
	t.Helper()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	for i, message := range messages {
		fake.groups["/aws/lambda/web-fn-1"] = append(fake.groups["/aws/lambda/web-fn-1"], storedEvent{Stream: lambdaStream, Time: addToEpoch(i), Message: message})
	}
	events, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}, Limit: 100})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	return events
}

func TestReadDropsLambdaStartEndAndReportLines(t *testing.T) {
	t.Parallel()
	report, err := os.ReadFile("testdata/report_success.txt")
	if err != nil {
		t.Fatal(err)
	}

	events := readLambdaLines(t,
		"INIT_START Runtime Version: nodejs:22.mainline.v98\tRuntime Version ARN: arn:aws:lambda:us-east-1::runtime:a783\n",
		"START RequestId: 70b609e1-c0ae-483f-8cf2-cac7eb9aa711 Version: $LATEST\n",
		"2026-09-08T05:06:39.288Z\t70b609e1-c0ae-483f-8cf2-cac7eb9aa711\tWARN\tocel: a user line\n",
		"plain output mentioning START RequestId: inside a line\n",
		"END RequestId: 70b609e1-c0ae-483f-8cf2-cac7eb9aa711\n",
		string(report),
		"INIT_REPORT Init Duration: 612.33 ms\tPhase: init\n",
	)
	want := []string{
		"2026-09-08T05:06:39.288Z\t70b609e1-c0ae-483f-8cf2-cac7eb9aa711\tWARN\tocel: a user line",
		"plain output mentioning START RequestId: inside a line",
	}
	if got := collectTexts(events); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Read() texts = %q, want only the app's own lines", got)
	}
	for _, event := range events {
		if event.Failure {
			t.Errorf("event %q is a failure, want none", event.Text)
		}
	}
}

func TestReadReportsALambdaTimeoutAsAFailure(t *testing.T) {
	t.Parallel()
	events := readLambdaLines(t, "REPORT RequestId: 1c2d\tDuration: 3003.12 ms\tBilled Duration: 3000 ms\tMemory Size: 128 MB\tMax Memory Used: 20 MB\tStatus: timeout\n")
	if len(events) != 1 || !events[0].Failure || events[0].Text != "timed out after 3003.12 ms" {
		t.Fatalf("Read() = %+v, want one failure reading %q", events, "timed out after 3003.12 ms")
	}
	if events[0].Instance != "[$LATEST]f7108dfca11d48d8ba7321a9b4fe4b02" {
		t.Errorf("Instance = %q, want the bracketed version and id of the stream", events[0].Instance)
	}
}

func TestReadReportsALambdaOutOfMemoryAsAFailure(t *testing.T) {
	t.Parallel()
	events := readLambdaLines(t, "REPORT RequestId: 1c2d\tDuration: 812.40 ms\tBilled Duration: 813 ms\tMemory Size: 256 MB\tMax Memory Used: 256 MB\tStatus: error\tError Type: Runtime.OutOfMemory\n")
	if len(events) != 1 || !events[0].Failure || events[0].Text != "ran out of memory (256 MB)" {
		t.Fatalf("Read() = %+v, want one failure reading %q", events, "ran out of memory (256 MB)")
	}
}

func TestReadKeepsTheRawReportLineOfAnyOtherLambdaStatus(t *testing.T) {
	t.Parallel()
	line := "REPORT RequestId: 1c2d\tDuration: 5.00 ms\tBilled Duration: 6 ms\tMemory Size: 128 MB\tMax Memory Used: 30 MB\tStatus: error\tError Type: Runtime.ExitError"
	events := readLambdaLines(t, line+"\n")
	if len(events) != 1 || !events[0].Failure || events[0].Text != line {
		t.Fatalf("Read() = %+v, want one failure keeping the raw line", events)
	}
}

func TestReadReportsALambdaInitTimeoutAsAFailure(t *testing.T) {
	t.Parallel()
	events := readLambdaLines(t, "INIT_REPORT Init Duration: 10000.12 ms\tPhase: init\tStatus: timeout\n")
	if len(events) != 1 || !events[0].Failure || events[0].Text != "timed out after 10000.12 ms" {
		t.Fatalf("Read() = %+v, want one failure reading %q", events, "timed out after 10000.12 ms")
	}
}

func TestReadKeepsTheRawInitReportLineOfAnyOtherLambdaStatus(t *testing.T) {
	t.Parallel()
	line := "INIT_REPORT Init Duration: 1236.04 ms\tPhase: init\tStatus: error\tError Type: Extension.Crash"
	events := readLambdaLines(t, line+"\n")
	if len(events) != 1 || !events[0].Failure || events[0].Text != line {
		t.Fatalf("Read() = %+v, want one failure keeping the raw line", events)
	}
}

func TestReadKeepsLambdaLookingLinesOfAContainer(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.groups["/ocel/containers/production"] = []storedEvent{{Stream: "svc-c/app/t1", Time: addToEpoch(1), Message: "START RequestId: not lambda\n"}}
	events, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Container: "svc-c"}}, Tier: environment.TierProduction, Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := collectTexts(events); len(got) != 1 || got[0] != "START RequestId: not lambda" {
		t.Fatalf("Read() texts = %q, want the container's line kept", got)
	}
}

func TestReadDoesNotSpendTheLimitOnDroppedLambdaLines(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	for i := 1; i <= 3; i++ {
		fake.groups["/aws/lambda/web-fn-1"] = append(fake.groups["/aws/lambda/web-fn-1"],
			storedEvent{Stream: lambdaStream, Time: addToEpoch(i * 10), Message: "user " + strconv.Itoa(i) + "\n"},
			storedEvent{Stream: lambdaStream, Time: addToEpoch(i*10 + 1), Message: "END RequestId: x\n"},
			storedEvent{Stream: lambdaStream, Time: addToEpoch(i*10 + 2), Message: "REPORT RequestId: x\tDuration: 1.00 ms\n"},
		)
	}
	events, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}, Limit: 2})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := collectTexts(events); strings.Join(got, "|") != "user 2|user 3" {
		t.Fatalf("Read() texts = %q, want the newest 2 lines the app wrote", got)
	}
	if sent := fake.listRequests(); len(sent) != 1 {
		t.Errorf("requests = %d, want the dropped lines read in the same single page", len(sent))
	}
}

func TestReadQuotesContainsAsALiteralFilterPattern(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"timeout":           `"timeout"`,
		"user is not found": `"user is not found"`,
		`said "hi"`:         `"said \"hi\""`,
		`C:\temp`:           `"C:\\temp"`,
		"-ERROR ?x":         `"-ERROR ?x"`,
	}
	for contains, want := range tests {
		fake, logs, lambdas := newFakeAWS(t)
		fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
		fake.groups["/aws/lambda/web-fn-1"] = []storedEvent{{Stream: lambdaStream, Time: addToEpoch(1), Message: "x\n"}}
		if _, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}, Limit: 10, Contains: contains}); err != nil {
			t.Fatalf("Read(Contains: %q) error = %v", contains, err)
		}
		if got := fake.listRequests()[0].FilterPattern; got != want {
			t.Errorf("Contains %q sent FilterPattern %s, want %s", contains, got, want)
		}
	}
}

func TestReadSendsNoFilterPatternWithoutContains(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	if _, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}, Limit: 10}); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := fake.listRequests()[0].FilterPattern; got != "" {
		t.Errorf("FilterPattern = %q, want none", got)
	}
}

func TestReadAsksOnlyForTheTimeRangeItIsGiven(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	for i := 1; i <= 5; i++ {
		fake.groups["/aws/lambda/web-fn-1"] = append(fake.groups["/aws/lambda/web-fn-1"], storedEvent{Stream: lambdaStream, Time: addToEpoch(i), Message: "line " + strconv.Itoa(i) + "\n"})
	}

	events, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Since: addToEpoch(2), Until: addToEpoch(4), Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	sent := fake.listRequests()[0]
	if sent.StartTime == nil || *sent.StartTime != addToEpoch(2).UnixMilli() || sent.EndTime == nil || *sent.EndTime != addToEpoch(4).UnixMilli() {
		t.Errorf("StartTime, EndTime = %v, %v, want %d, %d", sent.StartTime, sent.EndTime, addToEpoch(2).UnixMilli(), addToEpoch(4).UnixMilli())
	}
	if got := collectTexts(events); strings.Join(got, "|") != "line 2|line 3|line 4" {
		t.Errorf("Read() texts = %q, want the lines from Since through Until", got)
	}

	if _, err := Read(context.Background(), logs, lambdas, Query{Since: addToEpoch(2), Sources: []Source{{Function: "web-fn"}}, Limit: 10}); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if open := fake.listRequests()[1]; open.EndTime != nil {
		t.Errorf("EndTime = %v, want none when the query has no Until", *open.EndTime)
	}
}

func TestReadRefusesAQueryWithNoSince(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	if _, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Limit: 10}); err == nil {
		t.Fatal("Read() error = nil, want a query with no Since refused")
	}
	if sent := fake.listRequests(); len(sent) != 0 {
		t.Errorf("requests = %d, want none sent for a refused query", len(sent))
	}
}

func TestReadRefusesASinceBeforeCloudWatchReadsNewestFirst(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	cutoff := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	_, err := Read(context.Background(), logs, lambdas, Query{Since: cutoff.Add(-time.Millisecond), Sources: []Source{{Function: "web-fn"}}, Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "2024-01-01") {
		t.Fatalf("Read(Since: just before 2024) error = %v, want it refused, naming 2024-01-01", err)
	}
	if _, err := Read(context.Background(), logs, lambdas, Query{Since: cutoff, Sources: []Source{{Function: "web-fn"}}, Limit: 10}); err != nil {
		t.Fatalf("Read(Since: 2024-01-01) error = %v, want it read", err)
	}
}

func TestReadRefusesASourceNamingBothOrNeitherOfAFunctionAndAContainer(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	for _, source := range []Source{{Function: "web-fn", Container: "web-c1"}, {Label: "web"}} {
		if _, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{source}, Tier: environment.TierProduction, Limit: 10}); err == nil {
			t.Errorf("Read(Sources: %+v) error = nil, want the source refused", source)
		}
	}
	if sent := fake.listRequests(); len(sent) != 0 {
		t.Errorf("requests = %d, want none sent for a refused source", len(sent))
	}
}

func TestReadRefusesAContainerSourceWithNoTier(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	if _, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Container: "web-c1"}}, Limit: 10}); err == nil {
		t.Fatal("Read() error = nil, want a container source with no tier refused")
	}
	if sent := fake.listRequests(); len(sent) != 0 {
		t.Errorf("requests = %d, want none sent for a refused source", len(sent))
	}
}

func TestReadRequestsPagesOfItsLimitWithinAThousandToTenThousandEvents(t *testing.T) {
	t.Parallel()
	tests := map[int]int32{5: 1000, 2500: 2500, 50000: 10000}
	for limit, want := range tests {
		fake, logs, lambdas := newFakeAWS(t)
		fake.pageSize = 1
		fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
		for i := range 3 {
			fake.groups["/aws/lambda/web-fn-1"] = append(fake.groups["/aws/lambda/web-fn-1"], storedEvent{Stream: lambdaStream, Time: addToEpoch(i), Message: "line\n"})
		}
		if _, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}, Limit: limit}); err != nil {
			t.Fatalf("Read(Limit: %d) error = %v", limit, err)
		}
		for i, sent := range fake.listRequests() {
			if sent.Limit == nil || *sent.Limit != want {
				t.Errorf("Limit %d: request %d asked for a page of %v, want %d", limit, i, sent.Limit, want)
			}
		}
	}
}

func TestReadSurfacesAnErrorFromCloudWatchLogs(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	fake.logsError = "AccessDeniedException"

	_, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}, Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "/aws/lambda/web-fn-1") || !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Fatalf("Read() error = %v, want it to name the log group and carry the AWS error", err)
	}
}

func TestReadSurfacesAnErrorFromLambda(t *testing.T) {
	t.Parallel()
	_, logs, lambdas := newFakeAWS(t)

	_, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "gone-fn"}}, Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "gone-fn") || !strings.Contains(err.Error(), "ResourceNotFoundException") {
		t.Fatalf("Read() error = %v, want it to name the function and carry the AWS error", err)
	}
}

func TestReadRefusesALimitBelowOne(t *testing.T) {
	t.Parallel()
	_, logs, lambdas := newFakeAWS(t)
	for _, limit := range []int{0, -1} {
		if _, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Container: "svc-c"}}, Tier: environment.TierProduction, Limit: limit}); err == nil {
			t.Errorf("Read(Limit: %d) error = nil, want the limit refused", limit)
		}
	}
}

func TestReadKeepsPagingThroughAPageWithNoEvents(t *testing.T) {
	t.Parallel()
	fake, logs, lambdas := newFakeAWS(t)
	fake.emptyPage = true
	fake.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	fake.groups["/aws/lambda/web-fn-1"] = []storedEvent{{Stream: lambdaStream, Time: addToEpoch(1), Message: "late\n"}}

	events, err := Read(context.Background(), logs, lambdas, Query{Since: epoch, Sources: []Source{{Function: "web-fn"}}, Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := collectTexts(events); len(got) != 1 || got[0] != "late" {
		t.Fatalf("Read() texts = %q, want the event behind the empty page", got)
	}
}
