package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/realtime/v1/realtimev1connect"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

func proxyEnvValue(t *testing.T, env []string, key string) string {
	t.Helper()
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && name == key {
			return value
		}
	}
	t.Fatalf("env %q has no %s", env, key)
	return ""
}

var testSessionPrefix = naming.SessionKeyPrefix("shop", "prod")

type declared struct {
	bindings []live.Binding
	values   map[string]string
}

func (d declared) Bindings() []live.Binding { return d.bindings }

func (d declared) Value(key string) string { return d.values[key] }

func (d declared) Generation() uint32 { return 1 }

func binds(bindings ...live.Binding) declared { return declared{bindings: bindings} }

func TestServeProxy(t *testing.T) {
	t.Run("a deployment whose bindings all go direct serves nothing", func(t *testing.T) {
		served, err := serveProxy(context.Background(), binds(live.Binding{Name: "db--main", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES}), proxyConfig{table: "state", sessionPrefix: testSessionPrefix})
		if err != nil {
			t.Fatalf("serveProxy: %v", err)
		}
		if served.env != nil || served.errs != nil {
			t.Fatalf("serveProxy = %q, want no proxy for a deployment that reaches postgres directly", served.env)
		}
	})

	t.Run("a bucket with nowhere to keep its sessions fails by name", func(t *testing.T) {
		_, err := serveProxy(context.Background(), binds(live.Binding{Name: "bucket--uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}), proxyConfig{sessionPrefix: testSessionPrefix})
		if err == nil || !strings.Contains(err.Error(), stateTableEnvVar) {
			t.Fatalf("serveProxy err = %v, want it to name %s", err, stateTableEnvVar)
		}
	})

	t.Run("a bucket whose sessions have no key scope refuses to serve", func(t *testing.T) {
		_, err := serveProxy(context.Background(), binds(live.Binding{Name: "bucket--uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}), proxyConfig{table: "state"})
		if err == nil || !strings.Contains(err.Error(), sessionPrefixEnvVar) {
			t.Fatalf("serveProxy err = %v, want it to name %s", err, sessionPrefixEnvVar)
		}
	})

	t.Run("a bucket is served in-process, reachable only with the token the child is handed", func(t *testing.T) {
		t.Setenv("AWS_REGION", "us-east-1")
		t.Setenv("AWS_ACCESS_KEY_ID", "test")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

		served, err := serveProxy(context.Background(), binds(live.Binding{Name: "bucket--uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}), proxyConfig{table: "state", sessionPrefix: testSessionPrefix})
		if err != nil {
			t.Fatalf("serveProxy: %v", err)
		}
		if served.errs == nil {
			t.Fatal("serveProxy returned no channel to deliver the proxy's terminal error")
		}

		env := served.env
		addr := proxyEnvValue(t, env, processenv.RuntimeAddressEnvVar)
		if !strings.HasPrefix(addr, "http://127.0.0.1:") {
			t.Fatalf("%s = %q, want a loopback address the sandbox alone can reach", processenv.RuntimeAddressEnvVar, addr)
		}
		token := proxyEnvValue(t, env, localrpc.SessionTokenEnvVar)
		if token == "" {
			t.Fatalf("%s is empty, so the proxy is open to anything in the sandbox", localrpc.SessionTokenEnvVar)
		}

		client := bucketv1connect.NewBucketServiceClient(http.DefaultClient, addr)
		_, err = client.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{Bucket: "uploads"})
		var connectErr *connect.Error
		if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
			t.Fatalf("unauthenticated PresignUpload err = %v, want CodeUnauthenticated", err)
		}

		bearer := bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearerToken(token)}, addr)
		_, err = bearer.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{Bucket: "uploads"})
		if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeUnauthenticated {
			t.Fatalf("PresignUpload with the token exported into the child's environment = %v, want the proxy to answer it", err)
		}
	})
}

func TestABucketBoundToAStoreIsSignedForThatStore(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	values := declared{
		bindings: []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}},
		values: map[string]string{
			"OCEL_RESOURCE_BUCKET_uploads": `{"name":"ocel:bucket.uploads","bucket":{"bucket":"acme","endpoint":"https://abc.r2.cloudflarestorage.com","region":"auto","accessKeyId":"AKID","secretAccessKey":"secret"}}`,
		},
	}

	served, err := serveProxy(context.Background(), values, proxyConfig{table: "state", sessionPrefix: testSessionPrefix})
	if err != nil {
		t.Fatalf("serveProxy: %v", err)
	}
	env := served.env
	client := bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearerToken(proxyEnvValue(t, env, localrpc.SessionTokenEnvVar))}, proxyEnvValue(t, env, processenv.RuntimeAddressEnvVar))
	signed, err := client.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket: "OCEL_RESOURCE_BUCKET_uploads", Key: "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.Contains(signed.GetTarget().GetUrl(), "r2.cloudflarestorage.com") {
		t.Errorf("signed url = %q, want it signed for the store the record names, not the account's s3", signed.GetTarget().GetUrl())
	}
	if granted := grantedBuckets(values)(); len(granted) != 0 {
		t.Errorf("granted = %v, want the store-bound bucket kept off the account's own backend", granted)
	}
}

func TestATaskIsServedThroughTheProxyOnlyWithTheQueueTopologyBesideTheCode(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	values := binds(live.Binding{Name: "resize", Type: bindingsv1.BindingType_BINDING_TYPE_TASK})

	if _, err := serveProxy(context.Background(), values, proxyConfig{}); err == nil || !strings.Contains(err.Error(), queues.FilePath) {
		t.Fatalf("serveProxy with no queue topology = %v, want an error naming %s", err, queues.FilePath)
	}
	topology := &queues.Topology{Table: "state", KeyPrefix: "PROJECT#shop#ENV#prod#TASKS#", Topics: map[string]queues.Topic{}}
	served, err := serveProxy(context.Background(), values, proxyConfig{queues: topology})
	if err != nil {
		t.Fatalf("serveProxy: %v", err)
	}
	if served.engine == nil {
		t.Fatal("serveProxy built no engine for a deployment that binds a task")
	}
	client := taskv1connect.NewTaskServiceClient(&http.Client{Transport: bearerToken(proxyEnvValue(t, served.env, localrpc.SessionTokenEnvVar))}, proxyEnvValue(t, served.env, processenv.RuntimeAddressEnvVar))
	_, err = client.Trigger(context.Background(), &taskv1.TriggerRequest{Task: "resize"})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("a trigger of a task the topology does not declare = %v, want NotFound from the engine behind the proxy", err)
	}
}

func TestARealtimeBindingIsPublishedThroughTheProxy(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	values := binds(live.Binding{Name: "realtime--app", Key: "OCEL_RESOURCE_REALTIME_app", Type: bindingsv1.BindingType_BINDING_TYPE_REALTIME})

	served, err := serveProxy(context.Background(), values, proxyConfig{})
	if err != nil {
		t.Fatalf("serveProxy: %v", err)
	}
	client := realtimev1connect.NewRealtimeServiceClient(&http.Client{Transport: bearerToken(proxyEnvValue(t, served.env, localrpc.SessionTokenEnvVar))}, proxyEnvValue(t, served.env, processenv.RuntimeAddressEnvVar))
	_, err = client.Publish(context.Background(), &realtimev1.PublishRequest{Realtime: "other", Channel: "/other/status", Event: `{"v":1}`})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a publish on a resource this deployment does not bind = %v, want FailedPrecondition from the realtime service behind the proxy", err)
	}
}

type bearerToken string

func (b bearerToken) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}
