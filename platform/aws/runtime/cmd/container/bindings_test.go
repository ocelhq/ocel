package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/realtime/v1/realtimev1connect"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type fixedRecords struct {
	bindings []live.Binding
	values   map[string]string
}

func (f fixedRecords) Bindings() []live.Binding { return f.bindings }
func (f fixedRecords) Value(key string) string  { return f.values[key] }
func (f fixedRecords) Generation() uint32       { return 1 }

func envValue(env []string, key string) string {
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && name == key {
			return value
		}
	}
	return ""
}

type bearerToken string

func (b bearerToken) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}

func TestAContainerAppBoundToATaskTriggersItThroughTheProxyOnlyWithTheQueueTopologyHandedToIt(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	values := fixedRecords{bindings: []live.Binding{{Name: "resize", Type: bindingsv1.BindingType_BINDING_TYPE_TASK}}}

	if _, err := serveBindings(context.Background(), values, "127.0.0.1:1", ""); err == nil || !strings.Contains(err.Error(), queues.EnvVar) {
		t.Fatalf("serveBindings with no queue topology = %v, want an error naming %s", err, queues.EnvVar)
	}
	topology := `{"table":"state","keyPrefix":"PROJECT#shop#ENV#prod#TASKS#","topics":{}}`
	served, err := serveBindings(context.Background(), values, "127.0.0.1:1", topology)
	if err != nil {
		t.Fatalf("serveBindings: %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })
	client := taskv1connect.NewTaskServiceClient(&http.Client{Transport: bearerToken(envValue(served.Env(""), localrpc.SessionTokenEnvVar))}, envValue(served.Env(""), processenv.RuntimeAddressEnvVar))
	_, err = client.Trigger(context.Background(), &taskv1.TriggerRequest{Task: "resize"})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("a trigger of a task the topology does not declare = %v, want NotFound from the engine behind the proxy", err)
	}
}

func TestAContainerAppBoundToNeitherABucketNorATaskServesNoProxy(t *testing.T) {
	served, err := serveBindings(context.Background(), fixedRecords{bindings: []live.Binding{{Name: "db", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES}}}, "127.0.0.1:1", "")
	if err != nil || len(served.Env("")) != 0 {
		t.Fatalf("serveBindings = %+v, %v, want no proxy for an app that reaches nothing through it", served, err)
	}
}

func TestAContainerAppBoundToABucketIsServedTheProxyAndNeverHandedTheStoresKey(t *testing.T) {
	served, err := serveBindings(context.Background(), fixedRecords{
		bindings: []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}},
		values: map[string]string{
			"OCEL_RESOURCE_BUCKET_uploads": `{"name":"ocel:bucket.uploads","bucket":{"bucket":"acme","endpoint":"https://abc.r2.cloudflarestorage.com","region":"auto","accessKeyId":"AKID","secretAccessKey":"r2-secret"}}`,
		},
	}, "127.0.0.1:1", "")
	if err != nil {
		t.Fatalf("serveBindings: %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })
	if envValue(served.Env(""), processenv.RuntimeAddressEnvVar) == "" {
		t.Fatal("the app is handed no proxy, and it reaches a bound bucket only through one")
	}
	for _, entry := range served.Env("") {
		if strings.Contains(entry, "r2-secret") {
			t.Errorf("the app is handed %q: the store's key is the proxy's alone", entry)
		}
	}
}

func TestAContainerAppBoundToARealtimeChannelPublishesThroughTheProxy(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	values := fixedRecords{bindings: []live.Binding{{Name: "chat", Type: bindingsv1.BindingType_BINDING_TYPE_REALTIME}}}

	served, err := serveBindings(context.Background(), values, "127.0.0.1:1", "")
	if err != nil {
		t.Fatalf("serveBindings: %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })
	address := envValue(served.Env(""), processenv.RuntimeAddressEnvVar)
	if address == "" {
		t.Fatal("the app is handed no proxy, and it publishes to a realtime channel only through one")
	}
	client := realtimev1connect.NewRealtimeServiceClient(&http.Client{Transport: bearerToken(envValue(served.Env(""), localrpc.SessionTokenEnvVar))}, address)
	_, err = client.Publish(context.Background(), &realtimev1.PublishRequest{Realtime: "chat", Channel: "/chat/room", Event: `{}`})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a publish to a channel whose record never arrived = %v, want FailedPrecondition from the realtime service behind the proxy", err)
	}
}
