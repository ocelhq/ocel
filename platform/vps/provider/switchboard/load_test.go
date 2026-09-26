package switchboard_test

import (
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func tableAt(t *testing.T, document []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routing.json")
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestATableTheSwitchboardCannotReadIsRefusedAndTheOneItServesKeepsServing(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	board, at := served(t, routing(t, map[string]string{"shop.example.com": web}))

	for what, path := range map[string]string{
		"a table that is not json":         tableAt(t, []byte(`{"grace":`)),
		"a table ocel could not render":    tableAt(t, []byte(`{"grace":"30s","claims":[{"owner":"o","hostname":"*.example.com","pointer":"p"}]}`)),
		"a table that is not there at all": filepath.Join(t.TempDir(), "routing.json"),
	} {
		if err := board.Load(path); err == nil {
			t.Errorf("loading %s was taken, want it refused", what)
		}
	}
	if said := ask(t, http.DefaultClient, at, "shop.example.com", "/"); said.status != http.StatusOK || said.body != "web" {
		t.Errorf("after the refused loads shop.example.com answered %d %q, want the table that was serving to still serve", said.status, said.body)
	}

	if err := board.Load(tableAt(t, routing(t, map[string]string{"blog.example.com": web}))); err != nil {
		t.Fatal(err)
	}
	if said := ask(t, http.DefaultClient, at, "shop.example.com", "/"); said.status != http.StatusNotFound {
		t.Errorf("shop.example.com answered %d after a table that no longer claims it was loaded, want 404", said.status)
	}
	if said := ask(t, http.DefaultClient, at, "blog.example.com", "/"); said.body != "web" {
		t.Errorf("blog.example.com answered %q after a table claiming it was loaded, want web", said.body)
	}
}

func TestLoadingTablesUnderLoadNeverDropsARequestOrTheConnectionItCameOn(t *testing.T) {
	t.Parallel()

	blue, green := backend(t, "blue"), backend(t, "green")
	tables := []string{
		tableAt(t, routing(t, map[string]string{"shop.example.com": blue})),
		tableAt(t, routing(t, map[string]string{"shop.example.com": green})),
		tableAt(t, []byte(`{"grace":"30s","routes":[{"owner":"o","pointer":"p","app":"web","upstream":"nowhere"}]}`)),
	}
	board, at := served(t, routing(t, map[string]string{"shop.example.com": blue}))

	var dialled atomic.Int64
	client := &http.Client{Transport: &http.Transport{MaxConnsPerHost: 1}}
	trace := &httptrace.ClientTrace{GotConn: func(got httptrace.GotConnInfo) {
		if !got.Reused {
			dialled.Add(1)
		}
	}}
	stop := make(chan struct{})
	var asked sync.WaitGroup
	var failed atomic.Int64
	var served atomic.Int64
	for range 8 {
		asked.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				request, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), http.MethodGet, "http://"+at+"/", nil)
				if err != nil {
					failed.Add(1)
					return
				}
				request.Host = "shop.example.com"
				said, err := client.Do(request)
				if err != nil {
					failed.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, said.Body)
				_ = said.Body.Close()
				if said.StatusCode != http.StatusOK {
					failed.Add(1)
					continue
				}
				served.Add(1)
			}
		})
	}
	for loads := 0; loads < 300 || served.Load() < 1000; loads++ {
		_ = board.Load(tables[loads%len(tables)])
	}
	close(stop)
	asked.Wait()

	if failed.Load() != 0 {
		t.Errorf("%d of %d requests failed across 300 loads, want none: a load swaps the table under the listener and never touches a connection", failed.Load(), failed.Load()+served.Load())
	}
	if dialled.Load() != 1 {
		t.Errorf("the client dialled %d connections across the loads, want the one it opened first to serve every request", dialled.Load())
	}
}
