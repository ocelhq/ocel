package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/progress"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

func TestAGCPProviderForwardsPorts(t *testing.T) {
	t.Parallel()

	p, err := NewProvider(Options{Project: "acme-prod", Region: "europe-west1"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Hooks().BindingAccess == nil {
		t.Error("Hooks().BindingAccess = nil, so a build gets no bindings to the databases and caches on the tier's network")
	}
}

func TestForwardingNoBindingTouchesNoCloud(t *testing.T) {
	t.Parallel()

	forwards, err := (&Provider{}).ForwardPorts(context.Background(), provider.PortForwardRequest{}, nil)

	if err != nil || len(forwards) != 0 {
		t.Errorf("ForwardPorts() of no binding = %v, %v, want nothing forwarded and nothing reached", forwards, err)
	}
}

func TestABindingThatNamesNoHostAndPortIsRefusedBeforeAnyBastionIsMade(t *testing.T) {
	t.Parallel()
	server := &googletest.Run{}
	p := providerServing(server, t)

	forwards, err := p.ForwardPorts(context.Background(), provider.PortForwardRequest{
		Tier:     "production",
		Bindings: []provider.Binding{{Type: provider.BindingPostgres, Name: "orders", Properties: map[string]string{provider.PropertyHost: "10.240.0.5"}}},
	}, nil)

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || len(forwards) != 0 {
		t.Fatalf("ForwardPorts() = %v, %v, want an invalid refusal naming the binding", forwards, err)
	}
	if server.Serving() != nil {
		t.Error("a bastion was made for a binding no port can be forwarded to")
	}
}

func TestOneIdentityTokenServesEveryConnectionOfABuildUntilItIsOldEnoughToRenew(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	var minted []string
	tokens := &identityTokens{
		audience: "ocel-production-bastion",
		now:      func() time.Time { return now },
		prove: func(_ context.Context, audience string) (envsource.IdentityProof, error) {
			minted = append(minted, audience)
			return envsource.IdentityProof{IDToken: "token-" + string(rune('a'+len(minted)-1))}, nil
		},
	}

	first, _ := tokens.mintOrReuse(context.Background())
	now = now.Add(identityTokenLife - time.Minute)
	again, _ := tokens.mintOrReuse(context.Background())
	now = now.Add(2 * time.Minute)
	renewed, _ := tokens.mintOrReuse(context.Background())

	if first != "token-a" || again != "token-a" || renewed != "token-b" {
		t.Errorf("tokens = %q, %q, %q, want one token reused until it is older than %s", first, again, renewed, identityTokenLife)
	}
	if len(minted) != 2 || minted[0] != "ocel-production-bastion" {
		t.Errorf("tokens were minted for %v, want two, each for the bastion's own audience", minted)
	}
}

func TestAForwardIsOpenedAgainWhileCloudRunStillRefusesAnInvokerGrantItJustMade(t *testing.T) {
	t.Parallel()
	var tries int
	b := bastion{
		waited: func(context.Context, int) bool { return true },
		open: func(context.Context, relay.Link) (*relay.Forward, error) {
			tries++
			if tries < 3 {
				return nil, refusal.Refuse(refusal.CodeNotReady, "the bastion refused the connection (HTTP 403)")
			}
			return &relay.Forward{}, nil
		},
	}

	forward, err := b.openForward(context.Background(), relay.Link{})

	if err != nil || forward == nil || tries != 3 {
		t.Errorf("openForward() = %v, %v after %d tries, want the forward on the third", forward, err, tries)
	}
}

func TestAForwardThatNeverComesReadyIsGivenUpOnAfterABoundedNumberOfTries(t *testing.T) {
	t.Parallel()
	var tries int
	b := bastion{
		waited: func(context.Context, int) bool { return true },
		open: func(context.Context, relay.Link) (*relay.Forward, error) {
			tries++
			return nil, refusal.Refuse(refusal.CodeNotReady, "the bastion refused the connection")
		},
	}

	_, err := b.openForward(context.Background(), relay.Link{})

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeNotReady || tries != forwardOpenAttempts {
		t.Errorf("openForward() = %v after %d tries, want a not-ready refusal after %d", err, tries, forwardOpenAttempts)
	}
}

func TestAFailureThatWaitingCannotMendIsNotRetried(t *testing.T) {
	t.Parallel()
	var tries int
	broken := errors.New("mint an identity token: the credential is a user's own login")
	b := bastion{
		waited: func(context.Context, int) bool { return true },
		open: func(context.Context, relay.Link) (*relay.Forward, error) {
			tries++
			return nil, broken
		},
	}

	_, err := b.openForward(context.Background(), relay.Link{})

	if !errors.Is(err, broken) || tries != 1 {
		t.Errorf("openForward() = %v after %d tries, want the failure after one", err, tries)
	}
}

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
	serve := p.Hooks().BindingAccess
	if serve == nil {
		t.Fatal("the gcp provider sets no ServeBindingProxy hook, so a build never reaches a bucket")
	}
	proxy, err := serve.ServeProxy(context.Background(), provider.BindingProxyRequest{Slug: "shop", Tier: environment.TierProduction, Env: "production", Grants: grants}, progress.Discard())
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
