package tracing_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/content"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestASuccessfulRequestIsAClientSpanWithItsStatusHeadersAndBodySizeButNotItsBodies(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		received = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-Requestid", "req-123")
		_, _ = io.WriteString(w, `{"Item":{"id":"web"}}`)
	}))
	defer server.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/table?consistent=true", strings.NewReader(`{"TableName":"ocel"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	resp, err := (&http.Client{Transport: tracing.NewTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if received != `{"TableName":"ocel"}` || string(body) != `{"Item":{"id":"web"}}` {
		t.Errorf("server received %q and caller read %q, want both whole", received, body)
	}
	span := recorded.Only(t)
	attrs := tracingtest.Attributes(span)
	if span.Name() != "POST "+strings.TrimPrefix(server.URL, "http://") {
		t.Errorf("span name = %q, want the method and host", span.Name())
	}
	for key, want := range map[string]string{
		"http.request.method":                   "POST",
		"url.full":                              server.URL + "/table?consistent=true",
		"http.request.header.content-type":      `["application/x-amz-json-1.0"]`,
		"http.response.status_code":             "200",
		"http.response.header.x-amzn-requestid": `["req-123"]`,
		"http.response.header.content-type":     `["application/json"]`,
		"http.response.body.size":               "21",
	} {
		if got := attrs[key].String(); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"http.request.body", "http.response.body"} {
		if _, recordedBody := attrs[key]; recordedBody {
			t.Errorf("%s = %q, want no body on a successful exchange", key, attrs[key].AsString())
		}
	}
}

func sendSucceeding(t *testing.T, ctx context.Context, contentType, sent, answered string) (received, read string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		received = string(raw)
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, answered)
	}))
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(sent))
	req.Header.Set("Content-Type", contentType)
	resp, err := (&http.Client{Transport: tracing.NewTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return received, string(body)
}

func TestUnderAFullTraceASuccessfulRequestRecordsTheBodiesSentAndGotBack(t *testing.T) {
	t.Setenv(tracing.ModeEnvVar, string(tracing.ModeFull))
	ctx, recorded := tracingtest.Record(t)

	received, read := sendSucceeding(t, ctx, "application/json", `{"TableName":"ocel"}`, `{"Item":{"id":"web"}}`)

	if received != `{"TableName":"ocel"}` || read != `{"Item":{"id":"web"}}` {
		t.Errorf("server received %q and caller read %q, want both whole", received, read)
	}
	attrs := tracingtest.Attributes(recorded.Only(t))
	if got := attrs["http.request.body"].AsString(); got != `{"TableName":"ocel"}` {
		t.Errorf("http.request.body = %q, want the body sent", got)
	}
	if got := attrs["http.response.body"].AsString(); got != `{"Item":{"id":"web"}}` {
		t.Errorf("http.response.body = %q, want the body got back", got)
	}
}

func TestUnderAFullTraceASuccessfulCallStillLeavesOutTheBodiesItsContextOmitsOrAGrantCarries(t *testing.T) {
	t.Setenv(tracing.ModeEnvVar, string(tracing.ModeFull))
	for name, send := range map[string]func(ctx context.Context) (context.Context, string, string){
		"a context that omits content": func(ctx context.Context) (context.Context, string, string) {
			return content.Omit(ctx), `{"Name":"/ocel/db"}`, `{"Parameter":{"Value":"s3cr3t"}}`
		},
		"an OAuth grant": func(ctx context.Context) (context.Context, string, string) {
			return ctx, "grant_type=client_credentials&client_secret=s3cr3t", `{"access_token":"s3cr3t"}`
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, recorded := tracingtest.Record(t)
			sendCtx, sent, answered := send(ctx)
			_, _ = sendSucceeding(t, sendCtx, "application/json", sent, answered)
			tracingtest.CheckWithoutBodies(t, recorded.Only(t), "s3cr3t")
		})
	}
}

func TestABinaryBodyIsNotRecordedEvenUnderAFullTrace(t *testing.T) {
	t.Setenv(tracing.ModeEnvVar, string(tracing.ModeFull))
	ctx, recorded := tracingtest.Record(t)
	_, _ = sendSucceeding(t, ctx, "application/octet-stream", "\x00\x01binary", "\x00\x02binary")
	tracingtest.CheckWithoutBodies(t, recorded.Only(t))
}

func TestAFailedResponseRecordsTheBodiesSentAndGotBackAndBothStillArriveWhole(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		received = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"__type":"ValidationException"}`)
	}))
	defer server.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{"TableName":"ocel"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	resp, err := (&http.Client{Transport: tracing.NewTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if received != `{"TableName":"ocel"}` || string(body) != `{"__type":"ValidationException"}` {
		t.Errorf("server received %q and caller read %q, want both whole", received, body)
	}
	span := recorded.Only(t)
	attrs := tracingtest.Attributes(span)
	if got := attrs["http.request.body"].AsString(); got != `{"TableName":"ocel"}` {
		t.Errorf("http.request.body = %q", got)
	}
	if got := attrs["http.response.body"].AsString(); got != `{"__type":"ValidationException"}` {
		t.Errorf("http.response.body = %q", got)
	}
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, want Error", span.Status().Code)
	}
}

type failingUpstream struct{}

func (failingUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	_, _ = io.ReadAll(req.Body)
	return nil, errors.New("connection reset by peer")
}

func TestARoundTripThatErrorsRecordsTheBodyItSent(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid/", strings.NewReader(`{"TableName":"ocel"}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := (&http.Client{Transport: &tracing.Transport{Base: failingUpstream{}}}).Do(req); err == nil {
		t.Fatal("Do() = nil, want the upstream's error")
	}
	if got := tracingtest.Attributes(recorded.Only(t))["http.request.body"].AsString(); got != `{"TableName":"ocel"}` {
		t.Errorf("http.request.body = %q, want the body sent", got)
	}
}

type cutOffUpstream struct{}

func (cutOffUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	_, _ = io.ReadAll(req.Body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(io.MultiReader(strings.NewReader(`{"Items":[`), iotest.ErrReader(io.ErrUnexpectedEOF))),
		Request:    req,
	}, nil
}

func TestAResponseCutOffMidBodyRecordsTheBodiesSentAndGotBack(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid/", strings.NewReader(`{"TableName":"ocel"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: &tracing.Transport{Base: cutOffUpstream{}}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadAll() = %v, want the upstream's cut-off", err)
	}
	_ = resp.Body.Close()

	span := recorded.Only(t)
	attrs := tracingtest.Attributes(span)
	if got := attrs["http.request.body"].AsString(); got != `{"TableName":"ocel"}` {
		t.Errorf("http.request.body = %q, want the body sent", got)
	}
	if got := attrs["http.response.body"].AsString(); got != `{"Items":[` {
		t.Errorf("http.response.body = %q, want what arrived before the cut-off", got)
	}
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, want Error", span.Status().Code)
	}
}

func failWith(status int, contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func TestAFailedRequestWhoseContextOmitsContentIsRecordedWithoutItsBodies(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(failWith(http.StatusBadRequest, "application/x-amz-json-1.1", `{"Parameter":{"Value":"s3cr3t"}}`))
	defer server.Close()

	req, _ := http.NewRequestWithContext(content.Omit(ctx), http.MethodPost, server.URL, strings.NewReader(`{"Name":"/ocel/db","WithDecryption":true}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	resp, err := (&http.Client{Transport: tracing.NewTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	attrs := tracingtest.Attributes(recorded.Only(t))
	for _, key := range []string{"http.request.body", "http.response.body"} {
		if _, recordedBody := attrs[key]; recordedBody {
			t.Errorf("%s = %q, want no body on the span", key, attrs[key].AsString())
		}
	}
}

func TestOnlyTheFirstPartOfALargeFailedBodyIsRecordedAndTheCallerReadsAllOfIt(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	large := strings.Repeat("a", tracing.RecordedBodyBytes+100)
	server := httptest.NewServer(failWith(http.StatusInternalServerError, "text/plain", large))
	defer server.Close()

	resp, err := get(ctx, &http.Client{Transport: tracing.NewTransport(nil)}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if len(body) != len(large) {
		t.Errorf("caller read %d bytes, want %d", len(body), len(large))
	}
	attrs := tracingtest.Attributes(recorded.Only(t))
	if got := len(attrs["http.response.body"].AsString()); got != tracing.RecordedBodyBytes {
		t.Errorf("recorded %d body bytes, want %d", got, tracing.RecordedBodyBytes)
	}
	if !attrs["http.response.body.truncated"].AsBool() {
		t.Error("http.response.body.truncated unset, want true")
	}
}

func TestABinaryBodyIsNotRecordedEvenWhenTheRequestFails(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(failWith(http.StatusInternalServerError, "application/octet-stream", "\x00\x01\x02"))
	defer server.Close()

	resp, err := get(ctx, &http.Client{Transport: tracing.NewTransport(nil)}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if _, ok := tracingtest.Attributes(recorded.Only(t))["http.response.body"]; ok {
		t.Error("recorded a binary body")
	}
}

type recordedRoundTrip struct {
	sent     io.ReadCloser
	returned io.ReadCloser
}

func (r *recordedRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	r.sent = req.Body
	r.returned = io.NopCloser(strings.NewReader(`{"error":"no"}`))
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: r.returned, Request: req}, nil
}

func TestATransportOutsideARecordedTraceHandsBodiesThroughUnwrapped(t *testing.T) {
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	unsampled := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID}))
	sent := io.NopCloser(strings.NewReader(`{"TableName":"ocel"}`))
	req, _ := http.NewRequestWithContext(unsampled, http.MethodPost, "http://example.invalid/", sent)
	req.Header.Set("Content-Type", "application/json")
	upstream := &recordedRoundTrip{}

	resp, err := (&tracing.Transport{Base: upstream}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if upstream.sent != sent || resp.Body != upstream.returned {
		t.Error("a request no trace records had its bodies wrapped, want them handed through as they were")
	}
}

func TestClosingAResponseWhileAReadIsBlockedOnItEndsTheSpanOnce(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "first chunk")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer server.Close()
	defer close(release)

	resp, err := get(ctx, &http.Client{Transport: tracing.NewTransport(nil)}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	firstRead := make(chan struct{})
	reading := make(chan struct{})
	go func() {
		defer close(reading)
		buf := make([]byte, 64)
		for read := 0; ; read++ {
			if read == 1 {
				close(firstRead)
			}
			if _, err := resp.Body.Read(buf); err != nil {
				return
			}
		}
	}()
	<-firstRead
	_ = resp.Body.Close()
	<-reading

	if got := tracingtest.Attributes(recorded.Only(t))["http.response.body.size"].AsInt64(); got != int64(len("first chunk")) {
		t.Errorf("http.response.body.size = %d, want the bytes read before the close", got)
	}
}

func TestAConnectionThatFailsIsAnEventNamingTheTypeOfItsError(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	_, _ = get(ctx, &http.Client{Transport: tracing.NewTransport(nil)}, url)

	for _, event := range recorded.Only(t).Events() {
		if event.Name != "connect.done" {
			continue
		}
		for _, keyValue := range event.Attributes {
			if keyValue.Key == "error" {
				t.Errorf("connect.done carries %s, want error.type", keyValue.Key)
			}
			if keyValue.Key == "error.type" && keyValue.Value.AsString() != "" {
				return
			}
		}
		t.Fatalf("connect.done attributes = %v, want error.type", event.Attributes)
	}
	t.Fatal("no connect.done event")
}

func TestAResponseClosedUnreadStillEndsItsSpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ignored")
	}))
	defer server.Close()

	resp, err := get(ctx, &http.Client{Transport: tracing.NewTransport(nil)}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	recorded.Only(t)
}

func TestAFailedRequestIsAnErrorSpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	if _, err := get(ctx, &http.Client{Transport: tracing.NewTransport(nil)}, url); err == nil {
		t.Fatal("request to a closed server succeeded")
	}
	if got := recorded.Only(t).Status().Code; got != codes.Error {
		t.Errorf("status = %v, want Error", got)
	}
}

type countingTransport struct{ calls int }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: http.Header{}}, nil
}

func TestATransportWithNoBaseSendsThroughWhateverTheDefaultIsWhenItSends(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	replacement := &countingTransport{}
	original := http.DefaultTransport
	http.DefaultTransport = replacement
	t.Cleanup(func() { http.DefaultTransport = original })

	resp, err := get(ctx, &http.Client{Transport: tracing.NewTransport(nil)}, "http://example.invalid/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if replacement.calls != 1 {
		t.Errorf("replacement default saw %d requests, want 1", replacement.calls)
	}
	recorded.Only(t)
}

func TestABaseTransportIsItsOwnCopyOfTheStandardLibrarysDefault(t *testing.T) {
	first, second := tracing.NewBaseTransport(), tracing.NewBaseTransport()
	if first == http.DefaultTransport || first == second {
		t.Error("tracing.NewBaseTransport() returned a shared transport, want a copy each call can change")
	}
	if first.Proxy == nil || first.DialContext == nil {
		t.Error("tracing.NewBaseTransport() lost the default's proxy or dialer")
	}
}

func TestAClientFromNewClientSendsEachRequestAsASpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	resp, err := get(ctx, tracing.NewClient(0), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got := tracingtest.Attributes(recorded.Only(t))["http.response.status_code"].AsInt64(); got != http.StatusNotFound {
		t.Errorf("http.response.status_code = %d, want the 404 the server answered", got)
	}
}

func TestAClientFromNewClientGivesUpOnARequestOutlivingItsTimeout(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	answered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-answered }))
	defer server.Close()
	defer close(answered)

	if _, err := get(ctx, tracing.NewClient(50*time.Millisecond), server.URL); err == nil {
		t.Fatal("get() = nil error, want the request given up once its timeout passed")
	}
	if span := recorded.Only(t); span.Status().Code != codes.Error {
		t.Errorf("status = %v, want the timed-out request an error span", span.Status().Code)
	}
}

func TestATransportThatNamesARequestStartsItsSpanUnderThatNameWithTheAttributesItGave(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	transport := &tracing.Transport{Name: func(req *http.Request) (string, []attribute.KeyValue, bool) {
		if req.URL.Path != "/v1/topics:publish" {
			return "", nil, false
		}
		return "pubsub/topics.publish", []attribute.KeyValue{attribute.String("rpc.method", "topics.publish")}, true
	}}

	for _, path := range []string{"/v1/topics:publish", "/healthz"} {
		resp, err := get(ctx, &http.Client{Transport: transport}, server.URL+path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	named := tracingtest.SpanNamed(t, recorded.Ended(), "pubsub/topics.publish")
	if got := tracingtest.Attributes(named)["rpc.method"].AsString(); got != "topics.publish" {
		t.Errorf("rpc.method = %q, want the attribute the transport gave", got)
	}
	tracingtest.SpanNamed(t, recorded.Ended(), "GET "+strings.TrimPrefix(server.URL, "http://"))
}

func TestWrappingATracedTransportAgainRecordsOneSpanPerRequest(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	resp, err := get(ctx, &http.Client{Transport: tracing.NewTransport(tracing.NewTransport(nil))}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	recorded.Only(t)
}

func get(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

const (
	signedJWT    = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJ3ZWJAcC5pYW0uZ3NlcnZpY2VhY2NvdW50LmNvbSJ9.c2lnbmVkLWJ5LWdvb2dsZQ"
	awsSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	awsSession   = "IQoJb3JpZ2luX2VjEOr//////////wEaCXVzLWVhc3QtMSJHMEUCIQ"
)

var grantExchanges = []tracingtest.Exchange{
	{
		Name: "an OAuth refresh at any host", Method: http.MethodPost, URL: "https://auth.example.com/oauth/token",
		RequestType: "application/x-www-form-urlencoded", RequestBody: "grant_type=refresh_token&refresh_token=1%2F%2F0gOldRefresh&client_id=web&client_secret=GOCSPX-web",
		ResponseType: "application/json", ResponseBody: `{"access_token":"gho_newAccess","refresh_token":"ghr_newRefresh","id_token":"` + signedJWT + `"}`,
		Secrets: []string{"0gOldRefresh", "GOCSPX-web", "gho_newAccess", "ghr_newRefresh", signedJWT},
	},
	{
		Name: "a PKCE code exchange at any host", Method: http.MethodPost, URL: "https://auth.example.com/oauth/token",
		RequestType: "application/x-www-form-urlencoded", RequestBody: "grant_type=authorization_code&code=4%2F0AeaYcode&code_verifier=dBjftJeZ4CVPmB92K27uhbUJU1p1r&redirect_uri=http%3A%2F%2F127.0.0.1%3A8085",
		ResponseType: "application/json", ResponseBody: `{"access_token":"gho_pkceAccess","token_type":"bearer"}`,
		Secrets: []string{"0AeaYcode", "dBjftJeZ4CVPmB92K27uhbUJU1p1r", "gho_pkceAccess"},
	},
}

var credentialResponses = []tracingtest.Exchange{
	{
		Name: "container credentials from a full URI", Method: http.MethodGet, URL: "http://127.0.0.1:51678/creds",
		ResponseType: "application/json",
		ResponseBody: `{"AccessKeyId":"ASIAEXAMPLEKEYID0003","SecretAccessKey":"` + awsSecretKey + `","Token":"` + awsSession + `","Expiration":"2026-10-27T13:00:00Z"}`,
		Secrets:      []string{awsSecretKey, awsSession},
	},
	{
		Name: "a JWT in an ordinary response", Method: http.MethodGet, URL: "https://api.example.com/whoami",
		ResponseType: "text/plain", ResponseBody: "signed in with " + signedJWT + " until noon",
		Secrets: []string{signedJWT},
	},
}

func TestAnOAuthGrantAtAnyHostIsTracedWithoutItsBodiesOrTheTokensTheyCarry(t *testing.T) {
	for _, exchange := range grantExchanges {
		t.Run(exchange.Name, func(t *testing.T) {
			tracingtest.CheckRecordedWithoutBodies(t, exchange, tracingtest.SendRefused(t, exchange))
		})
	}
}

func TestACredentialInAFailedResponsesBodyNeverReachesItsTrace(t *testing.T) {
	for _, exchange := range credentialResponses {
		t.Run(exchange.Name, func(t *testing.T) {
			exported := tracingtest.ExportedText(tracingtest.SendRefused(t, exchange))
			for _, secret := range exchange.Secrets {
				if strings.Contains(exported, secret) {
					t.Errorf("trace carries %q:\n%s", secret, exported)
				}
			}
		})
	}
}

func echoUpgradeBackend(t *testing.T) *url.URL {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n')
		_, _ = rw.WriteString("echo " + line)
		_ = rw.Flush()
	}))
	t.Cleanup(backend.Close)
	target, _ := url.Parse(backend.URL)
	return target
}

func echoThroughProxy(ctx context.Context, t *testing.T, proxy *httputil.ReverseProxy) {
	t.Helper()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r.WithContext(trace.ContextWithSpanContext(r.Context(), trace.SpanContextFromContext(ctx))))
	}))
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /hmr HTTP/1.1\r\nHost: web\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("proxy answered %s %q, want 101 Switching Protocols", resp.Status, raw)
	}
	_, _ = io.WriteString(conn, "hello\n")
	if line, _ := reader.ReadString('\n'); line != "echo hello\n" {
		t.Errorf("read %q back through the upgraded connection, want %q", line, "echo hello\n")
	}
}

func TestAnUpgradedConnectionThroughATracedTransportCarriesBytesBothWaysAndIsASpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	target := echoUpgradeBackend(t)

	echoThroughProxy(ctx, t, &httputil.ReverseProxy{
		Rewrite:   func(r *httputil.ProxyRequest) { r.SetURL(target) },
		Transport: tracing.NewTransport(nil),
	})

	attrs := tracingtest.Attributes(recorded.Only(t))
	if attrs["http.response.status_code"].AsInt64() != http.StatusSwitchingProtocols {
		t.Errorf("span attributes = %v, want status 101", attrs)
	}
	if _, recordedBody := attrs["http.response.body"]; recordedBody {
		t.Errorf("http.response.body = %q, want no body on an upgrade", attrs["http.response.body"].AsString())
	}
}

type idleClosingTransport struct {
	countingTransport
	closes int
}

func (c *idleClosingTransport) CloseIdleConnections() { c.closes++ }

func TestClosingAClientsIdleConnectionsReachesTheTransportItTracesThrough(t *testing.T) {
	base := &idleClosingTransport{}
	(&http.Client{Transport: &tracing.Transport{Base: base}}).CloseIdleConnections()

	defaultBase := &idleClosingTransport{}
	original := http.DefaultTransport
	http.DefaultTransport = defaultBase
	t.Cleanup(func() { http.DefaultTransport = original })
	(&http.Client{Transport: tracing.NewTransport(nil)}).CloseIdleConnections()

	if base.closes != 1 || defaultBase.closes != 1 {
		t.Errorf("base closed %d times and default %d, want each once", base.closes, defaultBase.closes)
	}
}

func TestACompressedFailedResponseIsNotRecordedAsText(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	_, _ = io.WriteString(zipper, `{"code":"permission_denied","message":"no"}`)
	_ = zipper.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := (&http.Client{Transport: tracing.NewTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if !bytes.Equal(body, compressed.Bytes()) {
		t.Error("caller did not read the compressed body unchanged")
	}
	attrs := tracingtest.Attributes(recorded.Only(t))
	if got, ok := attrs["http.response.body"]; ok {
		t.Errorf("recorded the compressed body as text: %q", got.AsString())
	}
	if got := attrs["http.request.body"].AsString(); got != "{}" {
		t.Errorf("http.request.body = %q, want {}", got)
	}
}

func TestAFailedBodyCutInsideACharacterIsRecordedAsValidText(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(failWith(http.StatusInternalServerError, "text/plain", strings.Repeat("a", tracing.RecordedBodyBytes-1)+"é"))
	defer server.Close()

	resp, err := get(ctx, &http.Client{Transport: tracing.NewTransport(nil)}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if got := tracingtest.Attributes(recorded.Only(t))["http.response.body"].AsString(); !utf8.ValidString(got) {
		t.Errorf("http.response.body ends %q, which is not valid UTF-8", got[len(got)-4:])
	}
}
