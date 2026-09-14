package costkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/costkit"
)

func fetcher(client *http.Client, waited *[]time.Duration) costkit.Fetcher {
	return costkit.Fetcher{
		Client: client, Attempts: 4, Base: time.Second, Ceiling: 5 * time.Second,
		Sleep: func(_ context.Context, wait time.Duration) error {
			*waited = append(*waited, wait)
			return nil
		},
	}
}

func TestFetchRetriesThrottlingAndServerErrorsWithBackoff(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		switch calls {
		case 1:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer server.Close()
	var waited []time.Duration
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/prices?key=secret", nil)

	body, err := fetcher(server.Client(), &waited).Fetch(context.Background(), req)
	if err != nil || string(body) != "ok" {
		t.Fatalf("Fetch() = %q, %v", body, err)
	}
	if calls != 3 || len(waited) != 2 {
		t.Fatalf("calls = %d, waits = %v", calls, waited)
	}
	if waited[0] != 2*time.Second {
		t.Errorf("first wait = %v, want the Retry-After of 2s", waited[0])
	}
	if waited[1] <= 0 || waited[1] > 2*time.Second {
		t.Errorf("second wait = %v, want jitter within the doubled base of 2s", waited[1])
	}
}

func TestFetchGivesUpAtTheAttemptCeilingWithoutEchoingTheQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	var waited []time.Duration
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/prices?key=secret", nil)

	_, err := fetcher(server.Client(), &waited).Fetch(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "4 attempts") {
		t.Fatalf("Fetch() error = %v, want one after 4 attempts", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error echoes the query: %v", err)
	}
	for _, wait := range waited {
		if wait > 5*time.Second {
			t.Errorf("wait %v passed the 5s ceiling", wait)
		}
	}
}

func TestFetchDoesNotRetryAClientError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	var waited []time.Duration
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/prices?key=secret", nil)

	_, err := fetcher(server.Client(), &waited).Fetch(context.Background(), req)
	if err == nil || calls != 1 || len(waited) != 0 {
		t.Fatalf("Fetch() error = %v after %d calls and %v, want one refusal and no retry", err, calls, waited)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error echoes the query: %v", err)
	}
}

func TestStreamHandsTheBodyOverWithoutHoldingIt(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"version":"1"}`))
	}))
	defer server.Close()
	var waited []time.Duration
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/offers/index.json", nil)

	var seen string
	err := fetcher(server.Client(), &waited).Stream(context.Background(), req, func(body io.Reader) error {
		var held struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(body).Decode(&held); err != nil {
			return err
		}
		seen = held.Version
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() = %v", err)
	}
	if seen != "1" || calls != 2 {
		t.Errorf("the reader saw version %q after %d calls", seen, calls)
	}
}

func TestStreamDoesNotRetryWhatTheReaderRefused(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte("body"))
	}))
	defer server.Close()
	var waited []time.Duration
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/offers/index.json", nil)
	refused := errors.New("the third product makes no sense")

	err := fetcher(server.Client(), &waited).Stream(context.Background(), req, func(io.Reader) error { return refused })
	if !errors.Is(err, refused) {
		t.Fatalf("Stream() = %v, want the reader's own refusal", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want the request sent once: a half-read body cannot be replayed", calls)
	}
}
