package tracing_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing"
)

type receivedExport struct {
	path, authorization, origin, contentType, body string
}

func TestTheCollectorProxyForwardsAnExportUnderTheCollectorsTokenWithoutItsOrigin(t *testing.T) {
	t.Parallel()
	received := make(chan receivedExport, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- receivedExport{r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("Origin"), r.Header.Get("Content-Type"), string(body)}
	}))
	t.Cleanup(collector.Close)
	proxy, err := tracing.NewCollectorProxy(tracing.Collector{URL: collector.URL + tracing.CollectorPath, Token: "run-token"})
	if err != nil {
		t.Fatalf("NewCollectorProxy() = %v", err)
	}
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)

	req, _ := http.NewRequest(http.MethodPost, front.URL+tracing.CollectorPath+"?Authorization=Bearer+relay-token", strings.NewReader("spans"))
	req.Header.Set("Content-Type", tracing.CollectorMediaType)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Authorization", "Bearer relay-token")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	want := receivedExport{"/v1/traces", "Bearer run-token", "", "application/x-protobuf", "spans"}
	if got := <-received; got != want {
		t.Errorf("the collector received %+v, want %+v", got, want)
	}
}

func TestTheCollectorProxyRefusesAnythingButAnExport(t *testing.T) {
	t.Parallel()
	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the collector was reached")
	}))
	t.Cleanup(collector.Close)
	proxy, err := tracing.NewCollectorProxy(tracing.Collector{URL: collector.URL + tracing.CollectorPath, Token: "run-token"})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)

	for _, asked := range []struct{ method, path string }{{http.MethodGet, tracing.CollectorPath}, {http.MethodPost, "/v1/logs"}} {
		req, _ := http.NewRequest(asked.method, front.URL+asked.path, nil)
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("%s %s = %d, want a refusal", asked.method, asked.path, resp.StatusCode)
		}
	}
}
