package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/progress"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

type listedBuckets struct {
	mutex sync.Mutex
	paths []string
}

func (l *listedBuckets) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mutex.Lock()
		l.paths = append(l.paths, r.Method+" "+r.URL.Path)
		l.mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

type bearer string

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}

func servedProxy(t *testing.T, bindings []provider.Binding) (provider.BindingProxy, *listedBuckets) {
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

func servedGrants(t *testing.T, grants ...provider.BindingGrant) (provider.BindingProxy, *listedBuckets) {
	t.Helper()
	store := &listedBuckets{}
	p := pushing(t, store.serve(t))
	serve := p.Hooks().ServeBindingProxy
	if serve == nil {
		t.Fatal("the gcp provider sets no ServeBindingProxy hook, so a build never reaches a bucket")
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

func TestTheGCPBindingProxyServesTheBucketsABuildBindsAndNoOtherBucket(t *testing.T) {
	proxy, store := servedProxy(t, []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads")})
	client := bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(proxy.Sessions[0].SessionToken)}, proxy.Address)

	if _, err := client.List(context.Background(), &bucketv1.ListRequest{Bucket: "shop-uploads"}); err != nil {
		t.Fatalf("List of the bound bucket = %v, want it answered", err)
	}
	store.mutex.Lock()
	reached := slices.Clone(store.paths)
	store.mutex.Unlock()
	if len(reached) != 1 || reached[0] != "GET /storage/v1/b/shop-uploads/o" {
		t.Errorf("the store saw %v, want the one listing the build asked for", reached)
	}

	_, err := client.List(context.Background(), &bucketv1.ListRequest{Bucket: "someone-elses"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("List of a bucket the build does not bind = %v, want it refused: the deploy credentials reach far more than the app's grant", err)
	}
}

func TestTheGCPBindingProxyRefusesACallThatPresentsNoSessionToken(t *testing.T) {
	proxy, _ := servedProxy(t, []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads")})

	_, err := bucketv1connect.NewBucketServiceClient(http.DefaultClient, proxy.Address).
		List(context.Background(), &bucketv1.ListRequest{Bucket: "shop-uploads"})

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("List without the token = %v, want it refused", err)
	}
}

func TestTheGCPBindingProxyNamesTheBindingsItHasNoServiceFor(t *testing.T) {
	proxy, _ := servedProxy(t, []provider.Binding{
		bucketBinding("bucket--uploads", "shop-uploads"),
		{Type: provider.BindingTopic, Name: "topic--events"},
		{Type: provider.BindingRealtime, Name: "realtime--chat"},
	})

	if want := []string{"realtime--chat", "topic--events"}; !slices.Equal(slices.Sorted(slices.Values(proxy.Unserved)), want) {
		t.Errorf("Unserved = %v, want %v: the build is not handed a record nothing answers for", proxy.Unserved, want)
	}
}

func TestTheGCPBindingProxyGrantsEachGranteeOnlyItsOwnBucketsAndAProjectWideGrantEveryBucket(t *testing.T) {
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

	if _, err := as(web).List(context.Background(), &bucketv1.ListRequest{Bucket: "shop-uploads"}); err != nil {
		t.Errorf("web reaching its own bucket = %v, want it answered", err)
	}
	if _, err := as(web).List(context.Background(), &bucketv1.ListRequest{Bucket: "shop-manuals"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("web reaching docs' bucket = %v, want it refused: a build gets only the buckets its own app binds", err)
	}
	if _, err := as(docs).List(context.Background(), &bucketv1.ListRequest{Bucket: "shop-uploads"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("docs reaching web's bucket = %v, want it refused", err)
	}
	for _, bucket := range []string{"shop-uploads", "shop-manuals"} {
		if _, err := as(project).List(context.Background(), &bucketv1.ListRequest{Bucket: bucket}); err != nil {
			t.Errorf("the project-wide grant reaching %s = %v, want it answered: the preBuild is meant to reach every bucket", bucket, err)
		}
	}
}
