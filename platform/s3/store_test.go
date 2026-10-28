package s3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

type answeredStore struct {
	mu       sync.Mutex
	requests []string
}

func (a *answeredStore) serve(t *testing.T, createAnswer string) Store {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.RequestURI())
		a.mu.Unlock()
		if r.Method == http.MethodPut && r.URL.RawQuery == "" && createAnswer != "" {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + createAnswer + `</Code><Message>the bucket exists</Message></Error>`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return Store{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "AKID", SecretAccessKey: "secret", PathStyle: true}
}

func (a *answeredStore) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

func TestEnsureBucketRefusesABucketAnotherAccountOwns(t *testing.T) {
	var answered answeredStore
	store := answered.serve(t, "BucketAlreadyExists")

	err := store.EnsureBucket(context.Background(), "taken")
	if !errors.Is(err, ErrBucketTaken) {
		t.Fatalf("EnsureBucket() = %v, want ErrBucketTaken", err)
	}
	if !strings.Contains(err.Error(), "taken") {
		t.Errorf("EnsureBucket() = %q, want the bucket named", err)
	}
}

func TestEnsureBucketAcceptsABucketTheseCredentialsAlreadyOwn(t *testing.T) {
	var answered answeredStore
	store := answered.serve(t, "BucketAlreadyOwnedByYou")

	if err := store.EnsureBucket(context.Background(), "mine"); err != nil {
		t.Fatalf("EnsureBucket() = %v, want a bucket already owned accepted", err)
	}
	for _, request := range answered.sent() {
		if strings.Contains(request, "cors") {
			t.Errorf("EnsureBucket sent %q, want the bucket's upload origins left to SetUploadOrigins", request)
		}
	}
}

func TestAnObjectPutIsTracedWithoutItsContents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>PreconditionFailed</Code><Message>object moved</Message></Error>`))
	}))
	t.Cleanup(server.Close)
	store := Store{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "AKID", SecretAccessKey: "secret", PathStyle: true}
	ctx, spans := tracingtest.Record(t)

	_, err := store.Client().PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String("mine"),
		Key:         aws.String("doc.json"),
		Body:        strings.NewReader(`{"contents":"the object's own words"}`),
		ContentType: aws.String("application/json"),
		IfMatch:     aws.String(`"etag"`),
	})
	if err == nil {
		t.Fatal("PutObject() = nil, want the precondition refusal")
	}
	ended := spans.Ended()
	if len(ended) == 0 {
		t.Fatal("PutObject ended no span, want the HTTP call traced")
	}
	for _, span := range ended {
		attrs := tracingtest.Attributes(span)
		if attrs["http.response.status_code"].AsInt64() != http.StatusPreconditionFailed {
			t.Errorf("span %q attributes = %v, want the refused call's status", span.Name(), attrs)
		}
		if exported := tracingtest.ExportedText(span); strings.Contains(exported, "the object's own words") {
			t.Errorf("trace carries the object's contents:\n%s", exported)
		}
	}
}

func TestARefusedBucketCallRecordsTheRefusal(t *testing.T) {
	var answered answeredStore
	store := answered.serve(t, "BucketAlreadyExists")
	ctx, spans := tracingtest.Record(t)

	_ = store.EnsureBucket(ctx, "taken")

	span := spans.Only(t)
	if body := tracingtest.Attributes(span)["http.response.body"].AsString(); !strings.Contains(body, "BucketAlreadyExists") {
		t.Errorf("http.response.body = %q, want the refusal recorded", body)
	}
}

func TestSetUploadOriginsWithNoOriginsRemovesTheRules(t *testing.T) {
	var answered answeredStore
	store := answered.serve(t, "")

	if err := store.SetUploadOrigins(context.Background(), "mine", nil); err != nil {
		t.Fatalf("SetUploadOrigins(nil) = %v", err)
	}
	sent := answered.sent()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], http.MethodDelete+" /mine?cors") {
		t.Errorf("SetUploadOrigins(nil) sent %v, want the bucket's CORS rules deleted", sent)
	}
}

func TestTheStoreClientTracesOverTheSDKsTunedTransport(t *testing.T) {
	t.Parallel()
	client := Store{Endpoint: "http://127.0.0.1:1", Region: "us-east-1"}.Client()
	sender, ok := client.Options().HTTPClient.(objectContentClient)
	if !ok {
		t.Fatalf("HTTPClient = %T, want the client that omits object content", client.Options().HTTPClient)
	}
	traced, ok := sender.transport.(*tracing.Transport)
	if !ok {
		t.Fatalf("transport = %T, want a *tracing.Transport", sender.transport)
	}
	base, ok := traced.Base.(*http.Transport)
	if !ok {
		t.Fatalf("base = %T, want the SDK's *http.Transport", traced.Base)
	}
	if base.MaxIdleConnsPerHost != 10 {
		t.Errorf("MaxIdleConnsPerHost = %d, want the SDK's 10 rather than http.DefaultTransport's 2", base.MaxIdleConnsPerHost)
	}
}
