package logentries

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
)

type listing struct {
	mu       sync.Mutex
	requests []logging.ListLogEntriesRequest
}

func (l *listing) asked() []logging.ListLogEntriesRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.requests)
}

func serve(t *testing.T, answer func(call int, req logging.ListLogEntriesRequest) (int, any)) (*logging.Service, *listing) {
	t.Helper()
	listed := &listing{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req logging.ListLogEntriesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		listed.mu.Lock()
		listed.requests = append(listed.requests, req)
		call := len(listed.requests)
		listed.mu.Unlock()
		code, body := answer(call, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	logs, err := logging.NewService(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	return logs, listed
}

func entry(at time.Time, text string) map[string]any {
	return map[string]any{
		"timestamp":   at.UTC().Format(time.RFC3339Nano),
		"textPayload": text,
		"resource": map[string]any{
			"type":   "cloud_run_revision",
			"labels": map[string]string{"service_name": "web", "revision_name": "web-00001"},
		},
	}
}

func empty(int, logging.ListLogEntriesRequest) (int, any) { return http.StatusOK, map[string]any{} }

var since = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

func TestReadAsksForTheRevisionsOfEachSource(t *testing.T) {
	t.Parallel()
	logs, listed := serve(t, empty)

	_, err := Read(context.Background(), logs, "acme-prod", Query{
		Sources: []Source{
			{Service: "web", Revision: "web-00001"},
			{Service: "web", Revision: "web-00002"},
			{Service: "jobs", Revision: "jobs-00007"},
		},
		Since: since,
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := listed.asked()
	if len(got) != 1 {
		t.Fatalf("asked %d times, want 1", len(got))
	}
	if !slices.Equal(got[0].ResourceNames, []string{"projects/acme-prod"}) {
		t.Errorf("ResourceNames = %v, want [projects/acme-prod]", got[0].ResourceNames)
	}
	want := `resource.type="cloud_run_revision"` +
		` AND ((resource.labels.service_name="web" AND resource.labels.revision_name="web-00001")` +
		` OR (resource.labels.service_name="web" AND resource.labels.revision_name="web-00002")` +
		` OR (resource.labels.service_name="jobs" AND resource.labels.revision_name="jobs-00007"))` +
		` AND timestamp>="2026-10-03T09:00:00Z"` +
		` AND (log_id("run.googleapis.com/stdout") OR log_id("run.googleapis.com/stderr"))`
	if got[0].Filter != want {
		t.Errorf("Filter =\n%s\nwant\n%s", got[0].Filter, want)
	}
}

func TestReadLeavesTheRevisionOutWhenNoneIsGiven(t *testing.T) {
	t.Parallel()
	logs, listed := serve(t, empty)

	_, err := Read(context.Background(), logs, "acme-prod", Query{
		Sources: []Source{{Service: "web"}},
		Since:   since,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}

	filter := listed.asked()[0].Filter
	if !strings.Contains(filter, `AND ((resource.labels.service_name="web"))`) {
		t.Errorf("Filter = %s, want a term for the service alone", filter)
	}
	if strings.Contains(filter, "revision_name") {
		t.Errorf("Filter = %s, want no revision_name", filter)
	}
}

func TestReadQuotesContainsAndUntilIntoTheFilter(t *testing.T) {
	t.Parallel()
	logs, listed := serve(t, empty)

	_, err := Read(context.Background(), logs, "acme-prod", Query{
		Sources:  []Source{{Service: "web"}},
		Since:    since,
		Until:    since.Add(time.Hour),
		Limit:    10,
		Contains: `said "hi" \ bye`,
	})
	if err != nil {
		t.Fatal(err)
	}

	filter := listed.asked()[0].Filter
	for _, term := range []string{
		` AND timestamp<="2026-10-03T10:00:00Z"`,
		` AND "said \"hi\" \\ bye"`,
	} {
		if !strings.Contains(filter, term) {
			t.Errorf("Filter = %s, want it to contain %s", filter, term)
		}
	}
}

func TestReadReturnsTheNewestLimitEntriesOldestFirst(t *testing.T) {
	t.Parallel()
	newest := since.Add(10 * time.Minute)
	pages := map[string]map[string]any{
		"": {
			"entries":       []any{entry(newest, "e5"), entry(newest.Add(-time.Minute), "e4")},
			"nextPageToken": "second",
		},
		"second": {
			"entries":       []any{entry(newest.Add(-2*time.Minute), "e3"), entry(newest.Add(-3*time.Minute), "e2")},
			"nextPageToken": "third",
		},
		"third": {"entries": []any{entry(newest.Add(-4*time.Minute), "e1")}},
	}
	logs, listed := serve(t, func(_ int, req logging.ListLogEntriesRequest) (int, any) {
		return http.StatusOK, pages[req.PageToken]
	})

	got, err := Read(context.Background(), logs, "acme-prod", Query{
		Sources: []Source{{Service: "web", Revision: "web-00001", Label: "web"}},
		Since:   since,
		Limit:   3,
	})
	if err != nil {
		t.Fatal(err)
	}

	var texts []string
	for _, e := range got {
		texts = append(texts, e.Text)
	}
	if want := "e3 e4 e5"; strings.Join(texts, " ") != want {
		t.Errorf("texts = %v, want %s", texts, want)
	}
	if !got[0].Time.Equal(newest.Add(-2 * time.Minute)) {
		t.Errorf("Time = %v, want %v", got[0].Time, newest.Add(-2*time.Minute))
	}
	requests := listed.asked()
	if len(requests) != 2 {
		t.Fatalf("asked %d times, want 2: no page past the limit", len(requests))
	}
	if requests[0].OrderBy != "timestamp desc" {
		t.Errorf("OrderBy = %q, want timestamp desc", requests[0].OrderBy)
	}
	if requests[0].PageSize != 3 || requests[1].PageSize != 3 {
		t.Errorf("PageSizes = %d, %d, want 3 on every page: a page token only answers the request it came from", requests[0].PageSize, requests[1].PageSize)
	}
}

func TestReadAsksForNoMoreThanAThousandEntriesAPage(t *testing.T) {
	t.Parallel()
	logs, listed := serve(t, empty)

	_, err := Read(context.Background(), logs, "acme-prod", Query{Sources: []Source{{Service: "web"}}, Since: since, Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}

	if got := listed.asked()[0].PageSize; got != 1000 {
		t.Errorf("PageSize = %d, want 1000", got)
	}
}

func TestReadEchoesTheLabelOfTheSourceAnEntryCameFrom(t *testing.T) {
	t.Parallel()
	other := entry(since.Add(time.Minute), "from jobs")
	other["resource"] = map[string]any{"labels": map[string]string{"service_name": "jobs", "revision_name": "jobs-00007"}}
	other["labels"] = map[string]string{"instanceId": "abc123"}
	logs, _ := serve(t, func(int, logging.ListLogEntriesRequest) (int, any) {
		return http.StatusOK, map[string]any{"entries": []any{entry(since.Add(2*time.Minute), "from web"), other}}
	})

	got, err := Read(context.Background(), logs, "acme-prod", Query{
		Sources: []Source{
			{Service: "web", Label: "web"},
			{Service: "jobs", Revision: "jobs-00007", Label: "jobs"},
		},
		Since: since,
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[0].Text != "from jobs" || got[0].Label != "jobs" || got[0].Instance != "abc123" ||
		got[1].Text != "from web" || got[1].Label != "web" {
		t.Errorf("entries = %+v", got)
	}
}

func TestReadPassesAJSONPayloadThroughAsText(t *testing.T) {
	t.Parallel()
	const payload = `{"level":30,"time":1790000000000,"msg":"listening","port":8080}`
	structured := entry(since.Add(time.Minute), "")
	delete(structured, "textPayload")
	structured["jsonPayload"] = json.RawMessage(payload)
	logs, _ := serve(t, func(int, logging.ListLogEntriesRequest) (int, any) {
		return http.StatusOK, map[string]any{"entries": []any{structured}}
	})

	got, err := Read(context.Background(), logs, "acme-prod", Query{Sources: []Source{{Service: "web"}}, Since: since, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Text != payload {
		t.Errorf("entries = %+v, want one with Text %s", got, payload)
	}
}

func TestReadKeepsTheVendorsSeverity(t *testing.T) {
	t.Parallel()
	warning := entry(since.Add(time.Minute), "careful")
	warning["severity"] = "WARNING"
	plain := entry(since.Add(2*time.Minute), "plain")
	plain["severity"] = "DEFAULT"
	unset := entry(since.Add(3*time.Minute), "unset")
	logs, _ := serve(t, func(int, logging.ListLogEntriesRequest) (int, any) {
		return http.StatusOK, map[string]any{"entries": []any{unset, plain, warning}}
	})

	got, err := Read(context.Background(), logs, "acme-prod", Query{Sources: []Source{{Service: "web"}}, Since: since, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	var severities []string
	for _, e := range got {
		severities = append(severities, e.Severity)
	}
	if want := "WARNING||"; strings.Join(severities, "|") != want {
		t.Errorf("severities = %q, want %q", severities, want)
	}
}

func TestReadAsksAgainAfterAThrottledAnswer(t *testing.T) {
	t.Parallel()
	logs, listed := serve(t, func(call int, _ logging.ListLogEntriesRequest) (int, any) {
		if call == 1 {
			return http.StatusTooManyRequests, map[string]any{"error": map[string]any{"code": 429, "message": "quota"}}
		}
		return http.StatusOK, map[string]any{"entries": []any{entry(since.Add(time.Minute), "kept")}}
	})

	got, err := Read(context.Background(), logs, "acme-prod", Query{Sources: []Source{{Service: "web"}}, Since: since, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || len(listed.asked()) != 2 {
		t.Errorf("got %d entries after %d asks, want 1 after 2", len(got), len(listed.asked()))
	}
}

func TestReadDoesNotAskAgainAfterARefusal(t *testing.T) {
	t.Parallel()
	logs, listed := serve(t, func(int, logging.ListLogEntriesRequest) (int, any) {
		return http.StatusForbidden, map[string]any{"error": map[string]any{"code": 403, "message": "no logging.logEntries.list"}}
	})

	_, err := Read(context.Background(), logs, "acme-prod", Query{Sources: []Source{{Service: "web"}}, Since: since, Limit: 10})
	if err == nil {
		t.Fatal("Read succeeded on a refused list")
	}
	if n := len(listed.asked()); n != 1 {
		t.Errorf("asked %d times, want 1", n)
	}
}

func TestReadDoesNotAskAgainWhenTheEndpointCannotSpeakTLS(t *testing.T) {
	t.Parallel()
	var dialled atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			dialled.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	logs, err := logging.NewService(context.Background(),
		option.WithEndpoint("https://"+server.Listener.Addr().String()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}

	_, err = Read(context.Background(), logs, "acme-prod", Query{Sources: []Source{{Service: "web"}}, Since: since, Limit: 10})
	if err == nil {
		t.Fatal("Read succeeded against an endpoint that speaks no TLS")
	}
	if n := dialled.Load(); n != 1 {
		t.Errorf("dialled %d times, want 1: a handshake that failed once fails every time", n)
	}
}

func TestReadAsksAgainWhenTheConnectionDropsMidAnswer(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"entries": []any{entry(since.Add(time.Minute), "kept")}})
	}))
	t.Cleanup(server.Close)
	logs, err := logging.NewService(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}

	got, err := Read(context.Background(), logs, "acme-prod", Query{Sources: []Source{{Service: "web"}}, Since: since, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || calls.Load() != 2 {
		t.Errorf("got %d entries after %d calls, want 1 after 2", len(got), calls.Load())
	}
}

func TestReadRefusesAQueryWithNoStartRatherThanScanAllRetention(t *testing.T) {
	t.Parallel()
	logs, listed := serve(t, empty)

	_, err := Read(context.Background(), logs, "acme-prod", Query{Sources: []Source{{Service: "web"}}, Limit: 10})
	if err == nil {
		t.Fatal("Read succeeded with no Since")
	}
	if n := len(listed.asked()); n != 0 {
		t.Errorf("asked %d times, want 0", n)
	}
}

func TestReadAsksForNothingWhenThereIsNothingToRead(t *testing.T) {
	t.Parallel()
	logs, listed := serve(t, empty)

	for _, q := range []Query{
		{Since: since, Limit: 10},
		{Sources: []Source{{Service: "web"}}, Since: since},
	} {
		got, err := Read(context.Background(), logs, "acme-prod", q)
		if err != nil || len(got) != 0 {
			t.Errorf("Read(%+v) = %v, %v, want nothing", q, got, err)
		}
	}
	if n := len(listed.asked()); n != 0 {
		t.Errorf("asked %d times, want 0", n)
	}
}
