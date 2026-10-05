package telemetry_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

type receivedBatch struct {
	Path        string
	ContentType string
	Body        struct {
		APIKey string            `json:"api_key"`
		Batch  []json.RawMessage `json:"batch"`
	}
}

func aCapturingServer(t *testing.T, status int) (url string, received func() []receivedBatch) {
	t.Helper()
	var mu sync.Mutex
	var batches []receivedBatch
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		batch := receivedBatch{Path: r.URL.Path, ContentType: r.Header.Get("Content-Type")}
		if err := json.Unmarshal(raw, &batch.Body); err != nil {
			t.Errorf("request body %q is not JSON: %v", raw, err)
		}
		mu.Lock()
		batches = append(batches, batch)
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server.URL, func() []receivedBatch {
		mu.Lock()
		defer mu.Unlock()
		return append([]receivedBatch(nil), batches...)
	}
}

func aSpoolHolding(t *testing.T, commands ...string) telemetry.Spool {
	t.Helper()
	spool := telemetry.NewSpool(t.TempDir(), 1<<20)
	for _, command := range commands {
		if err := spool.Append(aCompletedEvent(t, command)); err != nil {
			t.Fatal(err)
		}
	}
	return spool
}

func TestSendPostsOneBatchWithTheKeyAndEveryEventThenEmptiesTheSpool(t *testing.T) {
	url, received := aCapturingServer(t, http.StatusOK)
	spool := aSpoolHolding(t, "deploy", "env set")
	spooled, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}

	if err := spool.Send(context.Background(), http.DefaultClient, url, "a-key"); err != nil {
		t.Fatal(err)
	}

	batches := received()
	if len(batches) != 1 {
		t.Fatalf("server received %d requests, want exactly one batch", len(batches))
	}
	if batches[0].Path != "/batch/" || batches[0].ContentType != "application/json" {
		t.Errorf("request = %s %s, want POST-ed JSON to /batch/", batches[0].Path, batches[0].ContentType)
	}
	if batches[0].Body.APIKey != "a-key" {
		t.Errorf("api_key = %q, want the key", batches[0].Body.APIKey)
	}
	if len(batches[0].Body.Batch) != 2 || string(batches[0].Body.Batch[0]) != string(spooled[0]) || string(batches[0].Body.Batch[1]) != string(spooled[1]) {
		t.Errorf("batch = %s, want the two spooled events %s", batches[0].Body.Batch, spooled)
	}
	if spool.HasEvents() {
		t.Error("the spool still holds events after a 2xx")
	}
}

func TestSendJoinsATrailingSlashEndpointToTheBatchPathOnce(t *testing.T) {
	url, received := aCapturingServer(t, http.StatusOK)
	spool := aSpoolHolding(t, "deploy")

	if err := spool.Send(context.Background(), http.DefaultClient, url+"/", "a-key"); err != nil {
		t.Fatal(err)
	}

	if batches := received(); len(batches) != 1 || batches[0].Path != "/batch/" {
		t.Errorf("requests = %+v, want one to /batch/", batches)
	}
}

func TestSendLeavesTheEventsInTheSpoolOnAnyNon2xxResponse(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusMovedPermanently} {
		url, received := aCapturingServer(t, status)
		spool := aSpoolHolding(t, "deploy")

		err := spool.Send(context.Background(), http.DefaultClient, url, "a-key")

		if err == nil {
			t.Errorf("status %d: Send() = nil, want the failure", status)
		}
		if got := commandsOf(t, spool); len(got) != 1 {
			t.Errorf("status %d: spool holds %v, want the event kept", status, got)
		}
		if len(received()) != 1 {
			t.Errorf("status %d: server saw %d requests, want one with no retry", status, len(received()))
		}
	}
}

func TestSendLeavesTheEventsInTheSpoolWhenTheEndpointIsUnreachable(t *testing.T) {
	spool := aSpoolHolding(t, "deploy")
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	err := spool.Send(context.Background(), http.DefaultClient, closed.URL, "a-key")

	if err == nil || len(commandsOf(t, spool)) != 1 {
		t.Errorf("Send() = %v, spool = %v, want the failure and the event kept", err, commandsOf(t, spool))
	}
}

func TestSendGivesUpOnAnEndpointThatNeverAnswersWithinItsTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	spool := aSpoolHolding(t, "deploy")
	started := time.Now()

	err := spool.Send(context.Background(), &http.Client{Timeout: 200 * time.Millisecond}, server.URL, "a-key")

	if err == nil {
		t.Error("Send() = nil, want the timeout")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("Send() took %s, want it bounded by the client timeout", elapsed)
	}
	if len(commandsOf(t, spool)) != 1 {
		t.Error("the spool lost its event to a timeout")
	}
}

func TestSendKeepsEventsAppendedWhileTheBatchWasInFlight(t *testing.T) {
	spool := aSpoolHolding(t, "deploy")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := spool.Append(aCompletedEvent(t, "late")); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	if err := spool.Send(context.Background(), http.DefaultClient, server.URL, "a-key"); err != nil {
		t.Fatal(err)
	}

	if got := commandsOf(t, spool); len(got) != 1 || got[0] != "late" {
		t.Errorf("spool holds %v, want only the event appended during the send", got)
	}
}

func TestTwoSendsAtOnceDeliverEachEventOnce(t *testing.T) {
	var requests atomic.Int32
	inFlight := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(inFlight)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	spool := aSpoolHolding(t, "deploy")
	first := make(chan error, 1)
	go func() { first <- spool.Send(context.Background(), http.DefaultClient, server.URL, "a-key") }()
	<-inFlight

	secondErr := spool.Send(context.Background(), http.DefaultClient, server.URL, "a-key")
	close(release)

	if secondErr != nil || <-first != nil {
		t.Errorf("first and second sends failed: %v", secondErr)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("server received %d batches, want the event sent once", got)
	}
}

func TestSendWithAnEmptySpoolMakesNoRequest(t *testing.T) {
	url, received := aCapturingServer(t, http.StatusOK)
	spool := telemetry.NewSpool(t.TempDir(), 1<<20)

	if err := spool.Send(context.Background(), http.DefaultClient, url, "a-key"); err != nil {
		t.Fatal(err)
	}

	if got := received(); len(got) != 0 {
		t.Errorf("server received %+v, want no request for an empty spool", got)
	}
}
