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
	return servedGrants(t, provider.BindingGrant{Grantee: "web", Bindings: bindings})
}

func tokenOf(t *testing.T, proxy provider.BindingProxy, grantee string) string {
	t.Helper()
	for _, session := range proxy.Sessions {
		if session.Grantee == grantee {
			return session.SessionToken
		}
	}
	t.Fatalf("the proxy minted no token for %q: %+v", grantee, proxy.Sessions)
	return ""
}

func servedGrants(t *testing.T, grants ...provider.BindingGrant) (provider.BindingProxy, *storeSeen) {
	t.Helper()
	store := &storeSeen{}
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String(store.serve(t))}
	p := NewProvider(Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace)
	if _, err := p.deployed.resolve(environment.TierProduction, func() (bootstrap.Reading, error) {
		return bootstrap.Reading{Deployed: bootstrap.Deployed{StateBucket: "state", ArtifactBucket: "artifacts", AssetBucket: "assets", StateTable: "state-table", VariablesTable: "variables"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	serve := p.Hooks().ServeBindingProxy
	if serve == nil {
		t.Fatal("the aws provider sets no ServeBindingProxy hook, so a build never reaches a bucket")
	}
	proxy, err := serve(context.Background(), provider.BindingProxyRequest{Slug: "shop", Tier: environment.TierProduction, Env: "production", Grants: grants}, progress.Discard())
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
	client := bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(proxy.Sessions[0].SessionToken)}, proxy.Address)

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

func TestTheAWSBindingProxyForABuildThatBindsNoBucketServesNothingAndAsksAWSNothing(t *testing.T) {
	store := &storeSeen{}
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String(store.serve(t))}
	p := NewProvider(Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace)

	proxy, err := p.ServeBindingProxy(context.Background(), provider.BindingProxyRequest{Slug: "shop", Tier: environment.TierProduction, Env: "production", Grants: []provider.BindingGrant{{Grantee: "web", Bindings: []provider.Binding{
		{Type: provider.BindingTopic, Name: "topic--events"},
	}}}}, progress.Discard())
	if err != nil {
		t.Fatalf("ServeBindingProxy() error = %v", err)
	}

	if proxy.Address != "" || proxy.Close != nil {
		t.Errorf("ServeBindingProxy() = %+v, want no proxy for a build that binds no bucket", proxy)
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if len(store.paths) != 0 {
		t.Errorf("AWS was asked %v, want nothing: the bootstrap is read only to serve a bucket", store.paths)
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

func TestTheAWSBindingProxyGrantsEachGranteeOnlyItsOwnBucketsAndAProjectWideGrantEveryBucket(t *testing.T) {
	proxy, _ := servedGrants(t,
		provider.BindingGrant{Grantee: "web", Bindings: []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads")}},
		provider.BindingGrant{Grantee: "docs", Bindings: []provider.Binding{bucketBinding("bucket--manuals", "shop-manuals")}},
		provider.BindingGrant{Grantee: "", Bindings: []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads"), bucketBinding("bucket--manuals", "shop-manuals")}},
	)
	web := tokenOf(t, proxy, "web")
	docs := tokenOf(t, proxy, "docs")
	project := tokenOf(t, proxy, "")
	if web == docs || web == project || docs == project {
		t.Fatalf("the grantees share a token: %+v", proxy.Sessions)
	}
	as := func(token string) bucketv1connect.BucketServiceClient {
		return bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(token)}, proxy.Address)
	}

	if _, err := as(web).Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-uploads", Key: "a.png"}); err != nil {
		t.Errorf("web reaching its own bucket = %v, want it answered", err)
	}
	if _, err := as(web).Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-manuals", Key: "a.png"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("web reaching docs' bucket = %v, want it refused: a build gets only the buckets its own app binds", err)
	}
	if _, err := as(docs).Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-uploads", Key: "a.png"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("docs reaching web's bucket = %v, want it refused", err)
	}
	for _, bucket := range []string{"shop-uploads", "shop-manuals"} {
		if _, err := as(project).Head(context.Background(), &bucketv1.HeadRequest{Bucket: bucket, Key: "a.png"}); err != nil {
			t.Errorf("the project-wide grant reaching %s = %v, want it answered: the preBuild is meant to reach every bucket", bucket, err)
		}
	}
}
