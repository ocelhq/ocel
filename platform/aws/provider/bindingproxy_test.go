package aws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/progress"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type storeSeen struct {
	mutex sync.Mutex
	paths []string
}

func (s *storeSeen) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mutex.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.mutex.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

type bearer string

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}

func servedProxy(t *testing.T, bindings []provider.Binding) (provider.BindingProxy, *storeSeen) {
	t.Helper()
	store := &storeSeen{}
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String(store.serve(t))}
	p := NewProvider(Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace)
	if _, err := p.deployed.resolve(environment.TierProduction, func() (bootstrap.Deployed, error) {
		return bootstrap.Deployed{StateBucket: "state", ArtifactBucket: "artifacts", AssetBucket: "assets", StateTable: "state-table", VariablesTable: "variables"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	serve := p.Hooks().ServeBindingProxy
	if serve == nil {
		t.Fatal("the aws provider sets no ServeBindingProxy hook, so a build never reaches a bucket")
	}
	proxy, err := serve(context.Background(), provider.BindingProxyRequest{Slug: "shop", Tier: environment.TierProduction, Env: "production", Bindings: bindings}, progress.Discard())
	if err != nil {
		t.Fatalf("ServeBindingProxy() error = %v", err)
	}
	t.Cleanup(proxy.Close)
	return proxy, store
}

func bucketBinding(name, bucket string) provider.Binding {
	return provider.Binding{Type: provider.BindingBucket, Name: name, Properties: map[string]string{provider.PropertyBucket: bucket}}
}

func TestTheAWSBindingProxyServesTheBucketsABuildBindsUnderTheDeployCredentialsAndNoOtherBucket(t *testing.T) {
	proxy, store := servedProxy(t, []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads")})
	client := bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(proxy.SessionToken)}, proxy.Address)

	if _, err := client.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-uploads", Key: "a.png"}); err != nil {
		t.Fatalf("Head of the bound bucket = %v, want it answered", err)
	}
	store.mutex.Lock()
	reached := slices.Clone(store.paths)
	store.mutex.Unlock()
	if len(reached) != 1 || reached[0] != "HEAD /shop-uploads/a.png" {
		t.Errorf("the store saw %v, want the one head the build asked for", reached)
	}

	_, err := client.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "someone-elses", Key: "a.png"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("Head of a bucket the build does not bind = %v, want it refused: the deploy credentials reach far more than the app's grant", err)
	}
}

func TestTheAWSBindingProxyRefusesACallThatPresentsNoSessionToken(t *testing.T) {
	proxy, _ := servedProxy(t, []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads")})

	_, err := bucketv1connect.NewBucketServiceClient(http.DefaultClient, proxy.Address).
		Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-uploads", Key: "a.png"})

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("Head without the token = %v, want it refused", err)
	}
}

func TestTheAWSBindingProxyNamesTheBindingsItHasNoServiceFor(t *testing.T) {
	proxy, _ := servedProxy(t, []provider.Binding{
		bucketBinding("bucket--uploads", "shop-uploads"),
		{Type: provider.BindingTopic, Name: "topic--events"},
		{Type: provider.BindingTask, Name: "task--resize"},
	})

	if want := []string{"task--resize", "topic--events"}; !slices.Equal(slices.Sorted(slices.Values(proxy.Unserved)), want) {
		t.Errorf("Unserved = %v, want %v: the build is not handed a record nothing answers for", proxy.Unserved, want)
	}
}
