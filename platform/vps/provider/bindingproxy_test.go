package vps_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/progress"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type storeSeen struct {
	mutex   sync.Mutex
	paths   []string
	signers []string
}

func (s *storeSeen) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mutex.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.signers = append(s.signers, r.Header.Get("Authorization"))
		s.mutex.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

type bearer string

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}

func servedProxy(t *testing.T, bindings []provider.Binding) (provider.BindingProxy, *storeSeen, *box) {
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

func servedGrants(t *testing.T, grants ...provider.BindingGrant) (provider.BindingProxy, *storeSeen, *box) {
	t.Helper()
	store := &storeSeen{}
	machine := &box{kept: sealedRootKey(), listening: store.serve(t), refuses: func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker inspect") {
			return session.Result{Stdout: "172.18.0.9 \n"}, true
		}
		return session.Result{}, false
	}}
	serve := over(machine).Hooks().ServeBindingProxy
	if serve == nil {
		t.Fatal("the vps provider sets no ServeBindingProxy hook, so a build never reaches a bucket")
	}
	proxy, err := serve(context.Background(), provider.BindingProxyRequest{Slug: "shop", Tier: environment.TierProduction, Env: "production", Grants: grants}, progress.Discard())
	if err != nil {
		t.Fatalf("ServeBindingProxy() error = %v", err)
	}
	t.Cleanup(proxy.Close)
	return proxy, store, machine
}

func TestTheVPSBindingProxyServesTheBucketsABuildBindsThroughAForwardToTheBoxsStoreAndNoOtherBucket(t *testing.T) {
	proxy, store, machine := servedProxy(t, []provider.Binding{bindingBucket()})
	client := bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(proxy.Sessions[0].SessionToken)}, proxy.Address)

	if _, err := client.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "prod-web-r0a1b2c3d-uploads", Key: "a.png"}); err != nil {
		t.Fatalf("Head of the bound bucket = %v, want it answered", err)
	}
	store.mutex.Lock()
	reached := slices.Clone(store.paths)
	store.mutex.Unlock()
	if len(reached) != 1 || reached[0] != "HEAD /prod-web-r0a1b2c3d-uploads/a.png" {
		t.Errorf("the store saw %v, want the one head the build asked for", reached)
	}
	if forwarded := machine.forwardedTo(); len(forwarded) != 1 || forwarded[0] != "172.18.0.9:9000" {
		t.Errorf("the box forwarded to %v, want the store container's own address and port", forwarded)
	}

	_, err := client.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "someone-elses", Key: "a.png"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("Head of a bucket the build does not bind = %v, want it refused: the store's root credential reaches every bucket on the box", err)
	}
}

func TestTheVPSBindingProxyRefusesACallThatPresentsNoSessionToken(t *testing.T) {
	proxy, _, _ := servedProxy(t, []provider.Binding{bindingBucket()})

	_, err := bucketv1connect.NewBucketServiceClient(http.DefaultClient, proxy.Address).
		Head(context.Background(), &bucketv1.HeadRequest{Bucket: "prod-web-r0a1b2c3d-uploads", Key: "a.png"})

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("Head without the token = %v, want it refused", err)
	}
}

func TestTheVPSBindingProxyNamesTheBindingsItHasNoServiceFor(t *testing.T) {
	proxy, _, _ := servedProxy(t, []provider.Binding{
		bindingBucket(),
		{Type: provider.BindingTopic, Name: "events"},
		{Type: provider.BindingTask, Name: "resize"},
	})

	if want := []string{"events", "resize"}; !slices.Equal(slices.Sorted(slices.Values(proxy.Unserved)), want) {
		t.Errorf("Unserved = %v, want %v: the build is not handed a record nothing answers for", proxy.Unserved, want)
	}
}

func TestTheVPSBindingProxyGrantsEachGranteeOnlyItsOwnBucketsAndAProjectWideGrantEveryBucket(t *testing.T) {
	manuals := provider.Binding{
		Type: provider.BindingBucket, Name: "manuals", Resource: "manuals",
		Properties: map[string]string{provider.PropertyBucket: "prod-web-r0a1b2c3d-manuals"},
	}
	proxy, _, _ := servedGrants(t,
		provider.BindingGrant{Grantee: "web", Bindings: []provider.Binding{bindingBucket()}},
		provider.BindingGrant{Grantee: "docs", Bindings: []provider.Binding{manuals}},
		provider.BindingGrant{Grantee: "", Bindings: []provider.Binding{bindingBucket(), manuals}},
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

	if _, err := as(web).Head(context.Background(), &bucketv1.HeadRequest{Bucket: "prod-web-r0a1b2c3d-uploads", Key: "a.png"}); err != nil {
		t.Errorf("web reaching its own bucket = %v, want it answered", err)
	}
	if _, err := as(web).Head(context.Background(), &bucketv1.HeadRequest{Bucket: "prod-web-r0a1b2c3d-manuals", Key: "a.png"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("web reaching docs' bucket = %v, want it refused: a build gets only the buckets its own app binds", err)
	}
	if _, err := as(docs).Head(context.Background(), &bucketv1.HeadRequest{Bucket: "prod-web-r0a1b2c3d-uploads", Key: "a.png"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("docs reaching web's bucket = %v, want it refused", err)
	}
	for _, bucket := range []string{"prod-web-r0a1b2c3d-uploads", "prod-web-r0a1b2c3d-manuals"} {
		if _, err := as(project).Head(context.Background(), &bucketv1.HeadRequest{Bucket: bucket, Key: "a.png"}); err != nil {
			t.Errorf("the project-wide grant reaching %s = %v, want it answered: the preBuild is meant to reach every bucket", bucket, err)
		}
	}
}
