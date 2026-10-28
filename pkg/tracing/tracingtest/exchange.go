package tracingtest

import (
	"io"
	"net/http"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/redactedspan"
)

type Exchange struct {
	Name         string
	Method, URL  string
	RequestType  string
	RequestBody  string
	ResponseType string
	ResponseBody string
	Secrets      []string
}

func SendRefused(t testing.TB, exchange Exchange) sdktrace.ReadOnlySpan {
	t.Helper()
	return SendRefusedThrough(t, exchange, tracing.NewTransport)
}

func SendRefusedThrough(t testing.TB, exchange Exchange, sender func(upstream http.RoundTripper) http.RoundTripper) sdktrace.ReadOnlySpan {
	t.Helper()
	ctx, spans := Record(t)
	var body io.Reader
	if exchange.RequestBody != "" {
		body = strings.NewReader(exchange.RequestBody)
	}
	req, err := http.NewRequestWithContext(ctx, exchange.Method, exchange.URL, body)
	if err != nil {
		t.Fatal(err)
	}
	if exchange.RequestType != "" {
		req.Header.Set("Content-Type", exchange.RequestType)
	}
	resp, err := (&http.Client{Transport: sender(refusingUpstream{exchange})}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	read, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(read) != exchange.ResponseBody {
		t.Fatalf("caller read %q, want the whole body", read)
	}
	return spans.Only(t)
}

func ExportedText(span sdktrace.ReadOnlySpan) string {
	exported := redactedspan.New(span, &tracing.SecretValues{})
	var out strings.Builder
	out.WriteString(exported.Name() + "\n" + exported.Status().Description + "\n")
	for _, keyValue := range exported.Attributes() {
		out.WriteString(string(keyValue.Key) + "=" + keyValue.Value.String() + "\n")
	}
	for _, event := range exported.Events() {
		out.WriteString(event.Name + "\n")
		for _, keyValue := range event.Attributes {
			out.WriteString(string(keyValue.Key) + "=" + keyValue.Value.String() + "\n")
		}
	}
	return out.String()
}

func CheckRecordedWithoutBodies(t testing.TB, exchange Exchange, span sdktrace.ReadOnlySpan) {
	t.Helper()
	CheckWithoutBodies(t, span, exchange.Secrets...)
	attrs := Attributes(span)
	if attrs["http.request.method"].AsString() != exchange.Method || attrs["url.full"].AsString() != exchange.URL ||
		attrs["http.response.status_code"].AsInt64() != http.StatusBadRequest ||
		attrs["http.response.body.size"].AsInt64() != int64(len(exchange.ResponseBody)) {
		t.Errorf("span attributes = %v, want the method, URL, status and body size kept", attrs)
	}
}

func CheckWithoutBodies(t testing.TB, span sdktrace.ReadOnlySpan, secrets ...string) {
	t.Helper()
	attrs := Attributes(span)
	for _, key := range []string{"http.request.body", "http.response.body"} {
		if _, recordedBody := attrs[key]; recordedBody {
			t.Errorf("span %q records %s = %q, want no body on the span", span.Name(), key, attrs[key].AsString())
		}
	}
	exported := ExportedText(span)
	for _, secret := range secrets {
		if strings.Contains(exported, secret) {
			t.Errorf("span %q carries %q:\n%s", span.Name(), secret, exported)
		}
	}
}

type refusingUpstream struct{ exchange Exchange }

func (u refusingUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.ReadAll(req.Body)
	}
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": {u.exchange.ResponseType}},
		Body:       io.NopCloser(strings.NewReader(u.exchange.ResponseBody)),
		Request:    req,
	}, nil
}
