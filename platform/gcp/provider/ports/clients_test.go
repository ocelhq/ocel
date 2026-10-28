package ports_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/storage"
	pubsub "google.golang.org/api/pubsub/v1"
	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func answerTopic(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"messageIds":["1"]}`))
}

func refuseTopic(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":{"code":400,"message":"attribute order is reserved"}}`))
}

func TestAnEmulatedRESTCallThatFailsIsOneSpanCarryingWhatItSentAndGotBack(t *testing.T) {
	ctx, spans := tracingtest.Record(t)
	server := httptest.NewServer(http.HandlerFunc(refuseTopic))
	t.Cleanup(server.Close)
	service, err := ports.NewRESTService(context.Background(), pubsub.NewService, server.URL, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Projects.Topics.Publish("projects/acme/topics/orders", &pubsub.PublishRequest{
		Messages: []*pubsub.PubsubMessage{{Attributes: map[string]string{"order": "o-17"}}},
	}).Context(ctx).Do(); err == nil {
		t.Fatal("Publish succeeded, want the emulator's refusal")
	}

	sent := spans.Only(t)
	if body := tracingtest.Attributes(sent)["http.request.body"]; !strings.Contains(body.AsString(), "o-17") {
		t.Errorf("the publish's span recorded request body %q, want the message it sent", body.AsString())
	}
	if body := tracingtest.Attributes(sent)["http.response.body"]; !strings.Contains(body.AsString(), "reserved") {
		t.Errorf("the publish's span recorded response body %q, want what Pub/Sub answered", body.AsString())
	}
}

func TestAGoogleRESTCallIsOneSpanCarryingTheProjectItBills(t *testing.T) {
	googletest.UseUserCredential(t)
	googletest.ServeAsGoogle(t, answerTopic)
	ctx, spans := tracingtest.Record(t)
	service, err := ports.NewRESTService(context.Background(), pubsub.NewService, "", "acme-billing")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Projects.Topics.Publish("projects/acme/topics/orders", &pubsub.PublishRequest{}).Context(ctx).Do(); err != nil {
		t.Fatal(err)
	}

	sent := spans.Only(t)
	if billed := tracingtest.Attributes(sent)["http.request.header.x-goog-user-project"]; !slices.Contains(billed.AsStringSlice(), "acme-billing") {
		t.Errorf("the publish's span recorded quota project %q, want acme-billing", billed.AsStringSlice())
	}
}

func TestASecretManagerCallIsOneSpanThatRecordsNoBody(t *testing.T) {
	googletest.UseUserCredential(t)
	googletest.ServeAsGoogle(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"projects/acme/secrets/key/versions/1","payload":{"data":"aHVudGVyMg=="}}`))
	})
	ctx, spans := tracingtest.Record(t)
	service, err := ports.NewRESTService(context.Background(), secretmanager.NewService, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Projects.Secrets.Versions.Access("projects/acme/secrets/key/versions/1").Context(ctx).Do(); err != nil {
		t.Fatal(err)
	}

	for _, keyValue := range spans.Only(t).Attributes() {
		if strings.Contains(keyValue.Value.String(), "aHVudGVyMg==") {
			t.Errorf("the access's span holds %s = %s, the secret it read", keyValue.Key, keyValue.Value.String())
		}
	}
}

func TestACloudStorageObjectReadIsASpanThatRecordsNoBody(t *testing.T) {
	googletest.UseUserCredential(t)
	googletest.ServeAsGoogle(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("DATABASE_PASSWORD hunter2"))
	})
	ctx, spans := tracingtest.Record(t)
	options, err := ports.NewStorageOptions(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	client, err := storage.NewClient(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	reader, err := client.Bucket("acme-assets").Object(".env").NewReader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()

	for _, span := range spans.Ended() {
		for _, keyValue := range span.Attributes() {
			if strings.Contains(keyValue.Value.String(), "hunter2") {
				t.Errorf("span %s holds %s = %s, what the object holds", span.Name(), keyValue.Key, keyValue.Value.String())
			}
		}
	}
}

func TestGoogleRESTCallsMadeAtOnceKeepTheirConnectionsForTheNextOnes(t *testing.T) {
	const calls = 10
	googletest.UseUserCredential(t)
	var (
		mu          sync.Mutex
		connections = map[string]bool{}
		inFlight    atomic.Pointer[sync.WaitGroup]
	)
	googletest.ServeAsGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		connections[r.RemoteAddr] = true
		mu.Unlock()
		arrived := inFlight.Load()
		arrived.Done()
		arrived.Wait()
		answerTopic(w, r)
	})
	service, err := ports.NewRESTService(context.Background(), pubsub.NewService, "", "")
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		arrived := &sync.WaitGroup{}
		arrived.Add(calls)
		inFlight.Store(arrived)
		var published sync.WaitGroup
		for range calls {
			published.Go(func() {
				if _, err := service.Projects.Topics.Publish("projects/acme/topics/orders", &pubsub.PublishRequest{}).Do(); err != nil {
					t.Error(err)
				}
			})
		}
		published.Wait()
	}

	if len(connections) != calls {
		t.Errorf("two rounds of %d calls at once opened %d connections, want %d: the second round reuses the first's, as Google's clients keep up to 100 idle connections a host", calls, len(connections), calls)
	}
}
