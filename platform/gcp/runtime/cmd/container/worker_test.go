//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/processenv"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	variables "github.com/ocelhq/ocel/platform/gcp/provider/resources/variables/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const emulatorEndpointVariable = "OCEL_FLOCI_GCP_ENDPOINT"

func flociEndpoint(t *testing.T) string {
	t.Helper()
	endpoint := os.Getenv(emulatorEndpointVariable)
	if endpoint == "" {
		t.Fatal("no floci-gcp emulator in the environment: a worker records its runs in Firestore")
	}
	return endpoint
}

func pinned(t *testing.T, endpoint string) (variables.Manifest, topics.Store) {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().String()))
	manifest := variables.Manifest{
		Project: "floci-local", Region: "europe-west1", Namespace: "ocel", Slug: "shop", Tier: string(environment.TierPreview), Endpoint: endpoint,
		Tasks: &variables.Tasks{
			Environment: "t" + hex.EncodeToString(sum[:5]),
			Topics: map[string]provider.TopicSpec{"resize": {Consumers: []provider.ConsumerSpec{{
				Name: "resize", Worker: "worker", Exclusive: true,
				Retry: provider.RetryPolicy{MaxAttempts: 3, MinDelay: time.Second, MaxDelay: time.Minute},
			}}}},
			DelayQueue: "projects/floci-local/locations/europe-west1/queues/ocel-preview-delays",
			Account:    "ocel-preview@floci-local.iam.gserviceaccount.com",
			PublishURL: endpoint,
		},
	}
	manifest.Databases = map[string]string{manifest.Slug: ports.SharedDatabase("ocel", environment.TierPreview)}
	clients := &ports.Clients{Namespace: "ocel", Project: manifest.Project, Region: manifest.Region, Endpoint: endpoint, ProjectDatabases: manifest.Databases}
	projectKeyMade(t, clients, environment.TierPreview, manifest.Slug)
	scope := topics.Scope{Slug: manifest.Slug, Tier: environment.TierPreview, Environment: manifest.Tasks.Environment}
	return manifest, topics.Store{Clients: clients, Cipher: ports.Envelope{Clients: clients}, Scope: scope}
}

func projectKeyMade(t *testing.T, clients *ports.Clients, tier environment.Tier, project string) {
	t.Helper()
	client, err := clients.KMS()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
		Parent:    "projects/" + clients.Project + "/locations/" + clients.Region,
		KeyRingId: clients.KeyRing(),
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		t.Fatalf("make the key ring: %v", err)
	}
	_, err = client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      clients.KeyRingPath(),
		CryptoKeyId: ports.ProjectKey(tier, project),
		CryptoKey:   &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT},
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		t.Fatalf("make the key %s seals its runs under: %v", project, err)
	}
}

func rendered(t *testing.T, manifest variables.Manifest) string {
	t.Helper()
	raw, err := variables.Render(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return variables.EnvVar + "=" + string(raw)
}

func TestLiveAWorkerAnswersEachPushByRunningItsConsumerAndRecordingTheRun(t *testing.T) {
	endpoint := flociEndpoint(t)
	manifest, store := pinned(t, endpoint)
	r := launch(t, "worker", processenv.WorkerEnvVar+"=worker", rendered(t, manifest))

	push, err := json.Marshal(map[string]any{"message": map[string]any{
		"data":        []byte(`{"size":2}`),
		"attributes":  map[string]string{topics.MessageAttribute: "01K0000000000000000000000A"},
		"messageId":   "17",
		"publishTime": time.Now().UTC().Format(time.RFC3339Nano),
	}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+r.port+topics.PushPath("resize", "resize"), bytes.NewReader(push))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker never acknowledged the push: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	run, err := store.ReadRun(context.Background(), "resize", "01K0000000000000000000000A-resize")
	if err != nil {
		t.Fatalf("ReadRun() = %v, want the run the push started", err)
	}
	if run.Status != provider.RunCompleted || string(run.Output) != `"resized"` {
		t.Errorf("the run is %s with output %s, want it completed with the worker's answer", run.Status, run.Output)
	}
	if code := r.await(t); code != 0 {
		t.Errorf("run = %d, want 0", code)
	}
}

func TestLiveAnAppPinnedTasksTriggersThemThroughItsBindingProxy(t *testing.T) {
	endpoint := flociEndpoint(t)
	manifest, store := pinned(t, endpoint)
	deployment := deploymentOf(manifest)
	topology := topics.Topology{Names: deployment.Names, Topics: deployment.Declared, Tags: topics.TierTags{
		Ensure: func(context.Context, string) error { return nil },
		Remove: func(context.Context, string, func(context.Context) (bool, error)) error { return nil },
	}}
	if err := topology.Ensure(context.Background(), deployment.Clients); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = topology.Remove(context.Background(), deployment.Clients) })
	r := launch(t, "serve", rendered(t, manifest))
	body := r.awaitFront(t, "")
	if body.Runtime == "" || body.Token == "" {
		t.Fatalf("the app was handed %s=%q and a token %q, want the binding proxy it triggers tasks through", processenv.RuntimeAddressEnvVar, body.Runtime, body.Token)
	}

	client := taskv1connect.NewTaskServiceClient(http.DefaultClient, body.Runtime, connect.WithInterceptors(bearer{token: body.Token}))
	resp, err := client.Trigger(context.Background(), &taskv1.TriggerRequest{Task: "resize", Payload: []byte(`{"size":3}`)})
	if err != nil {
		t.Fatalf("Trigger() through the proxy = %v", err)
	}
	run, err := store.ReadRun(context.Background(), "resize", resp.GetId())
	if err != nil || len(run.Payload) == 0 {
		t.Errorf("ReadRun(%s) = %+v, %v, want the run the trigger recorded", resp.GetId(), run, err)
	}
	if code := r.quit(t, ""); code != 0 {
		t.Errorf("run = %d, want 0", code)
	}
}

type bearer struct{ token string }

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+b.token)
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
