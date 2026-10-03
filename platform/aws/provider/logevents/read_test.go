package logevents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
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

type cloud struct {
	mu        sync.Mutex
	groups    map[string][]storedEvent
	lambdas   map[string]string
	pageSize  int
	requests  []filterRequest
	logsError string
	emptyPage bool
}

func newCloud(t *testing.T) (*cloud, *cloudwatchlogs.Client, *lambda.Client) {
	t.Helper()
	c := &cloud{groups: map[string][]storedEvent{}, lambdas: map[string]string{}, pageSize: 100}
	server := httptest.NewServer(c)
	t.Cleanup(server.Close)
	cfg := aws.Config{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(server.URL),
		Credentials:      credentials.NewStaticCredentialsProvider("id", "secret", ""),
		RetryMaxAttempts: 1,
	}
	return c, cloudwatchlogs.NewFromConfig(cfg), lambda.NewFromConfig(cfg)
}

func (c *cloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name, ok := strings.CutPrefix(r.URL.Path, "/2015-03-31/functions/"); ok {
		name = strings.TrimSuffix(name, "/configuration")
		group, found := c.lambdas[name]
		if !found {
			w.Header().Set("X-Amzn-Errortype", "ResourceNotFoundException")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"Message":"no such function"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"FunctionName": name, "LoggingConfig": map[string]any{"LogGroup": group, "LogFormat": "Text"}})
		return
	}
	if c.logsError != "" {
		w.Header().Set("X-Amzn-Errortype", c.logsError)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"message":"refused"}`)
		return
	}
	var req filterRequest
	json.NewDecoder(r.Body).Decode(&req)
	c.requests = append(c.requests, req)

	if c.emptyPage && req.NextToken == "" {
		json.NewEncoder(w).Encode(map[string]any{"events": []any{}, "nextToken": "0"})
		return
	}
	var matched []storedEvent
	for _, e := range c.groups[req.LogGroupName] {
		if req.LogStreamNamePrefix != "" && !strings.HasPrefix(e.Stream, req.LogStreamNamePrefix) {
			continue
		}
		if req.StartTime != nil && e.Time.UnixMilli() < *req.StartTime {
			continue
		}
		if req.EndTime != nil && e.Time.UnixMilli() > *req.EndTime {
			continue
		}
		matched = append(matched, e)
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].Time.After(matched[j].Time) })
	offset := 0
	if req.NextToken != "" {
		offset, _ = strconv.Atoi(req.NextToken)
	}
	size := c.pageSize
	if req.Limit != nil && int(*req.Limit) < size {
		size = int(*req.Limit)
	}
	end := min(offset+size, len(matched))
	events := []map[string]any{}
	for _, e := range matched[offset:end] {
		events = append(events, map[string]any{"logStreamName": e.Stream, "timestamp": e.Time.UnixMilli(), "message": e.Message})
	}
	out := map[string]any{"events": events}
	if end < len(matched) {
		out["nextToken"] = strconv.Itoa(end)
	}
	json.NewEncoder(w).Encode(out)
}

func (c *cloud) sentRequests() []filterRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]filterRequest(nil), c.requests...)
}

var epoch = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return epoch.Add(time.Duration(seconds) * time.Second) }

func texts(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Text
	}
	return out
}

func TestReadFindsALambdasLogGroupFromItsConfiguration(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.lambdas["web-fn"] = "/aws/lambda/web-fn-1a2b3c4"
	c.groups["/aws/lambda/web-fn-1a2b3c4"] = []storedEvent{{Stream: "2026/09/01/[$LATEST]abc", Time: at(1), Message: "hello\n"}}

	events, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn", Label: "web"}}, Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := texts(events); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("Read() texts = %q, want the line from the group named in the function's configuration", got)
	}
}

func TestReadAsksForContainerStreamsByThePhysicalPrefix(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.groups["/ocel/containers/prod"] = []storedEvent{
		{Stream: "web-c1/app/9f8e7d6c", Time: at(1), Message: "mine\n"},
		{Stream: "web-c10/app/aaaa", Time: at(2), Message: "a sibling whose name starts the same\n"},
	}

	events, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Container: "web-c1", Label: "web"}}, Tier: "prod", Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	sent := c.sentRequests()
	if len(sent) != 1 || sent[0].LogGroupName != "/ocel/containers/prod" || sent[0].LogStreamNamePrefix != "web-c1/" {
		t.Fatalf("requests = %+v, want one against group /ocel/containers/prod with stream prefix web-c1/", sent)
	}
	if len(events) != 1 || events[0].Text != "mine" || events[0].Instance != "9f8e7d6c" || events[0].Label != "web" {
		t.Fatalf("Read() = %+v, want only web-c1's line, on task 9f8e7d6c, labelled web", events)
	}
}

func TestReadMergesEventsFromSeveralLogGroupsByTime(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	c.lambdas["api-fn"] = "/aws/lambda/api-fn-2"
	c.groups["/aws/lambda/web-fn-1"] = []storedEvent{
		{Stream: "2026/09/01/[$LATEST]w", Time: at(1), Message: "web 1\n"},
		{Stream: "2026/09/01/[$LATEST]w", Time: at(4), Message: "web 4\n"},
	}
	c.groups["/aws/lambda/api-fn-2"] = []storedEvent{
		{Stream: "2026/09/01/[$LATEST]a", Time: at(2), Message: "api 2\n"},
		{Stream: "2026/09/01/[$LATEST]a", Time: at(3), Message: "api 3\n"},
	}
	c.groups["/ocel/containers/prod"] = []storedEvent{{Stream: "svc-c/app/t1", Time: at(5), Message: "svc 5\n"}}

	events, err := Read(context.Background(), logs, lambdas, Query{
		Sources: []Source{{Function: "web-fn", Label: "web"}, {Function: "api-fn", Label: "api"}, {Container: "svc-c", Label: "svc"}},
		Tier:    "prod",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := []string{"web 1", "api 2", "api 3", "web 4", "svc 5"}
	if got := texts(events); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Read() texts = %q, want %q", got, want)
	}
	if events[1].Label != "api" || events[4].Label != "svc" {
		t.Errorf("labels = %q, %q, want each event to carry its source's label", events[1].Label, events[4].Label)
	}
}

func TestReadReturnsTheNewestLimitEventsOldestFirst(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.pageSize = 2
	c.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	for i := 1; i <= 7; i++ {
		c.groups["/aws/lambda/web-fn-1"] = append(c.groups["/aws/lambda/web-fn-1"], storedEvent{Stream: "2026/09/01/[$LATEST]w", Time: at(i), Message: "line " + strconv.Itoa(i) + "\n"})
	}

	events, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Limit: 5})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := []string{"line 3", "line 4", "line 5", "line 6", "line 7"}
	if got := texts(events); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Read() texts = %q, want the newest 5 oldest first, paged through a 2-event page size", got)
	}
	sent := c.sentRequests()
	if len(sent) != 3 {
		t.Errorf("requests = %d, want 3 pages (2+2+1) and no more once the limit is met", len(sent))
	}
	if sent[0].StartFromHead == nil || *sent[0].StartFromHead {
		t.Errorf("first request StartFromHead = %v, want false so the newest events come first", sent[0].StartFromHead)
	}
}

const lambdaStream = "2026/09/01/[$LATEST]f7108dfca11d48d8ba7321a9b4fe4b02"

func readLambdaLines(t *testing.T, messages ...string) []Event {
	t.Helper()
	c, logs, lambdas := newCloud(t)
	c.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	for i, message := range messages {
		c.groups["/aws/lambda/web-fn-1"] = append(c.groups["/aws/lambda/web-fn-1"], storedEvent{Stream: lambdaStream, Time: at(i), Message: message})
	}
	events, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Limit: 100})
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
	)
	want := []string{
		"2026-09-08T05:06:39.288Z\t70b609e1-c0ae-483f-8cf2-cac7eb9aa711\tWARN\tocel: a user line",
		"plain output mentioning START RequestId: inside a line",
	}
	if got := texts(events); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Read() texts = %q, want only the app's own lines", got)
	}
	for _, e := range events {
		if e.Failure {
			t.Errorf("event %q is a failure, want none", e.Text)
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

func TestReadKeepsLambdaLookingLinesOfAContainer(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.groups["/ocel/containers/prod"] = []storedEvent{{Stream: "svc-c/app/t1", Time: at(1), Message: "START RequestId: not lambda\n"}}
	events, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Container: "svc-c"}}, Tier: "prod", Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := texts(events); len(got) != 1 || got[0] != "START RequestId: not lambda" {
		t.Fatalf("Read() texts = %q, want the container's line kept", got)
	}
}

func TestReadDoesNotSpendTheLimitOnDroppedLambdaLines(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	for i := 1; i <= 3; i++ {
		c.groups["/aws/lambda/web-fn-1"] = append(c.groups["/aws/lambda/web-fn-1"],
			storedEvent{Stream: lambdaStream, Time: at(i * 10), Message: "user " + strconv.Itoa(i) + "\n"},
			storedEvent{Stream: lambdaStream, Time: at(i*10 + 1), Message: "END RequestId: x\n"},
			storedEvent{Stream: lambdaStream, Time: at(i*10 + 2), Message: "REPORT RequestId: x\tDuration: 1.00 ms\n"},
		)
	}
	events, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Limit: 2})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := texts(events); strings.Join(got, "|") != "user 2|user 3" {
		t.Fatalf("Read() texts = %q, want the newest 2 lines the app wrote", got)
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
		c, logs, lambdas := newCloud(t)
		c.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
		c.groups["/aws/lambda/web-fn-1"] = []storedEvent{{Stream: lambdaStream, Time: at(1), Message: "x\n"}}
		if _, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Limit: 10, Contains: contains}); err != nil {
			t.Fatalf("Read(Contains: %q) error = %v", contains, err)
		}
		if got := c.sentRequests()[0].FilterPattern; got != want {
			t.Errorf("Contains %q sent FilterPattern %s, want %s", contains, got, want)
		}
	}
}

func TestReadSendsNoFilterPatternWithoutContains(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	if _, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Limit: 10}); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := c.sentRequests()[0].FilterPattern; got != "" {
		t.Errorf("FilterPattern = %q, want none", got)
	}
}

func TestReadAsksOnlyForTheTimeRangeItIsGiven(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	for i := 1; i <= 5; i++ {
		c.groups["/aws/lambda/web-fn-1"] = append(c.groups["/aws/lambda/web-fn-1"], storedEvent{Stream: lambdaStream, Time: at(i), Message: "line " + strconv.Itoa(i) + "\n"})
	}

	events, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Since: at(2), Until: at(4), Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	sent := c.sentRequests()[0]
	if sent.StartTime == nil || *sent.StartTime != at(2).UnixMilli() || sent.EndTime == nil || *sent.EndTime != at(4).UnixMilli() {
		t.Errorf("StartTime, EndTime = %v, %v, want %d, %d", sent.StartTime, sent.EndTime, at(2).UnixMilli(), at(4).UnixMilli())
	}
	if got := texts(events); strings.Join(got, "|") != "line 2|line 3|line 4" {
		t.Errorf("Read() texts = %q, want the lines from Since through Until", got)
	}

	if _, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Limit: 10}); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	open := c.sentRequests()[1]
	if open.StartTime != nil || open.EndTime != nil {
		t.Errorf("StartTime, EndTime = %v, %v, want neither when the query bounds neither", open.StartTime, open.EndTime)
	}
}

func TestReadSurfacesAnErrorFromCloudWatchLogs(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	c.logsError = "AccessDeniedException"

	_, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "/aws/lambda/web-fn-1") || !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Fatalf("Read() error = %v, want it to name the log group and carry the AWS error", err)
	}
}

func TestReadSurfacesAnErrorFromLambda(t *testing.T) {
	t.Parallel()
	_, logs, lambdas := newCloud(t)

	_, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "gone-fn"}}, Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "gone-fn") || !strings.Contains(err.Error(), "ResourceNotFoundException") {
		t.Fatalf("Read() error = %v, want it to name the function and carry the AWS error", err)
	}
}

func TestReadRefusesALimitBelowOne(t *testing.T) {
	t.Parallel()
	_, logs, lambdas := newCloud(t)
	for _, limit := range []int{0, -1} {
		if _, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Container: "svc-c"}}, Tier: "prod", Limit: limit}); err == nil {
			t.Errorf("Read(Limit: %d) error = nil, want the limit refused", limit)
		}
	}
}

func TestReadKeepsPagingThroughAPageWithNoEvents(t *testing.T) {
	t.Parallel()
	c, logs, lambdas := newCloud(t)
	c.emptyPage = true
	c.lambdas["web-fn"] = "/aws/lambda/web-fn-1"
	c.groups["/aws/lambda/web-fn-1"] = []storedEvent{{Stream: lambdaStream, Time: at(1), Message: "late\n"}}

	events, err := Read(context.Background(), logs, lambdas, Query{Sources: []Source{{Function: "web-fn"}}, Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := texts(events); len(got) != 1 || got[0] != "late" {
		t.Fatalf("Read() texts = %q, want the event behind the empty page", got)
	}
}
