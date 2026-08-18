package certs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func testProber(get func(context.Context, string) (http.Header, error), attempts int) Prober {
	return Prober{
		Get:      get,
		Wait:     func(context.Context, time.Duration) error { return nil },
		Attempts: attempts,
		Every:    time.Millisecond,
		Now:      func() time.Time { return time.Unix(1755500000, 0).UTC() },
		Jitter:   func() float64 { return 0.5 },
	}
}

func headerNaming(kind string) http.Header {
	return http.Header{http.CanonicalHeaderKey(edge.HeaderEdge): []string{kind}}
}

func TestProberAwait(t *testing.T) {
	t.Parallel()

	t.Run("passes once the wildcard answers as the edge", func(t *testing.T) {
		t.Parallel()

		var asked []string
		probe, err := testProber(func(_ context.Context, url string) (http.Header, error) {
			asked = append(asked, url)
			if len(asked) < 2 {
				return http.Header{}, nil
			}
			return headerNaming("cloudflare"), nil
		}, 5).Await(t.Context(), "*.preview.acme.com", edge.KindCloudflare, nil, func(string) {})
		if err != nil {
			t.Fatalf("Await: %v", err)
		}
		if !probe.OK || probe.Edge != edge.KindCloudflare || probe.At.IsZero() {
			t.Fatalf("probe = %+v, want a passing probe stamped with the edge", probe)
		}
		want := "https://" + edge.LivenessProbeLabel + ".preview.acme.com/"
		if asked[0] != want {
			t.Errorf("probed %q, want an arbitrary label under the wildcard: %q", asked[0], want)
		}
	})

	t.Run("gives up bounded, naming what answered instead", func(t *testing.T) {
		t.Parallel()

		probe, err := testProber(func(context.Context, string) (http.Header, error) {
			return headerNaming("native"), nil
		}, 3).Await(t.Context(), "*.preview.acme.com", edge.KindCloudflare, nil, func(string) {})
		if err == nil {
			t.Fatal("Await err = nil, want a bounded refusal")
		}
		if probe.OK {
			t.Error("probe recorded as passing after a refusal")
		}
		for _, want := range []string{edge.HeaderEdge, "native", "cloudflare"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("a hostname with no ocel behind it says so", func(t *testing.T) {
		t.Parallel()

		_, err := testProber(func(context.Context, string) (http.Header, error) {
			return http.Header{}, nil
		}, 2).Await(t.Context(), "*.preview.acme.com", edge.KindCloudflare, nil, func(string) {})
		if err == nil || !strings.Contains(err.Error(), "other than ocel") {
			t.Fatalf("err = %v, want it to say something else is serving the hostname", err)
		}
	})

	t.Run("a request that never completes is carried into the refusal", func(t *testing.T) {
		t.Parallel()

		attempts := 0
		_, err := testProber(func(context.Context, string) (http.Header, error) {
			attempts++
			return nil, errors.New("no such host")
		}, 4).Await(t.Context(), "*.preview.acme.com", edge.KindCloudflare, nil, func(string) {})
		if err == nil || !strings.Contains(err.Error(), "no such host") {
			t.Fatalf("err = %v, want the transport failure carried through", err)
		}
		if attempts != 4 {
			t.Errorf("attempts = %d, want the probe bounded at 4", attempts)
		}
	})
}

func TestProberOutstandingRecords(t *testing.T) {
	t.Parallel()

	owed := edge.Record{Name: "*.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "entry.ocel.dev"}
	_, err := testProber(func(context.Context, string) (http.Header, error) {
		return http.Header{}, nil
	}, 2).Await(t.Context(), "*.preview.acme.com", edge.KindCloudflare, []edge.Record{owed}, func(string) {})
	if err == nil {
		t.Fatal("Await err = nil, want a bounded refusal")
	}
	for _, want := range []string{owed.Name, owed.Value, "outstanding"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want the record the user still owes named: %q", err, want)
		}
	}
}

func TestProberInterval(t *testing.T) {
	t.Parallel()

	every := 10 * time.Second
	for source, want := range map[float64]time.Duration{
		0.5: every,
		0:   every - time.Duration(probeJitter*float64(every)),
		1:   every + time.Duration(probeJitter*float64(every)),
	} {
		p := Prober{Every: every, Jitter: func() float64 { return source }}
		if got := p.interval(); got != want {
			t.Errorf("interval() at %v = %s, want %s", source, got, want)
		}
	}

	spread := Prober{Every: every}
	for range 20 {
		got := spread.interval()
		if got < every-time.Second || got > every+time.Second {
			t.Fatalf("interval() = %s, want it within a few percent of %s", got, every)
		}
	}
}

func TestHTTPGet(t *testing.T) {
	t.Parallel()

	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.Header().Set(edge.HeaderEdge, "cloudflare")
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	header, err := httpGet(t.Context(), srv.URL)
	if err != nil {
		t.Fatalf("httpGet: %v", err)
	}
	if !edge.ServedBy(header.Get(edge.HeaderEdge), edge.KindCloudflare) {
		t.Errorf("header = %q, want a 404 to count: the probe reads the header, not the status", header.Get(edge.HeaderEdge))
	}
	if method != http.MethodGet {
		t.Errorf("method = %s, want GET: a HEAD is answered by the edge's bypass branch", method)
	}
}
