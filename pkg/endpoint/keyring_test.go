package endpoint_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/jwx/v3/jwk"

	"github.com/ocelhq/ocel/pkg/endpoint"
)

func countedKeys(t *testing.T, failing bool) (string, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if failing {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwk.NewSet())
	}))
	t.Cleanup(server.Close)
	return server.URL, &hits
}

func readConcurrently(keys *endpoint.Keyring, readers int) {
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() { _, _ = keys.Read(context.Background(), "a-kid-nobody-signs-with") })
	}
	wg.Wait()
}

func TestAFloodOfTokensNamingAnUnknownKeyFetchesTheKeysOnce(t *testing.T) {
	t.Parallel()
	for name, failing := range map[string]bool{"keys served": false, "keys failing": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			url, hits := countedKeys(t, failing)
			keys := endpoint.NewKeyring(url)

			readConcurrently(keys, 50)
			readConcurrently(keys, 50)
			if hits.Load() != 1 {
				t.Errorf("100 reads naming an unknown key fetched the keys %d times, want once until the refetch floor passes", hits.Load())
			}
		})
	}
}

func TestAReaderThatStopsWaitingLeavesTheFetchToTheOthers(t *testing.T) {
	t.Parallel()
	url, hits := countedKeys(t, false)
	keys := endpoint.NewKeyring(url)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = keys.Read(canceled, "kid")
	if _, err := keys.Read(context.Background(), "kid"); err != nil {
		t.Fatalf("Read() after a canceled reader = %v, want the keys fetched", err)
	}
	if hits.Load() != 1 {
		t.Errorf("the keys were fetched %d times, want once", hits.Load())
	}
}

func TestReadersOfAnUnfetchedKeyringWaitForTheFetchInFlight(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwk.NewSet())
	}))
	t.Cleanup(server.Close)
	keys := endpoint.NewKeyring(server.URL)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			set, err := keys.Read(context.Background(), "kid")
			if set == nil && err == nil {
				errs <- errors.New("Read() = (nil, nil), and a nil key set panics the verifier")
			}
		})
	}
	for hits.Load() == 0 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if hits.Load() != 1 {
		t.Errorf("the keys were fetched %d times, want once for every reader waiting on it", hits.Load())
	}
}
