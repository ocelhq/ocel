package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/packages/pagination"
	"github.com/cloudflare/cloudflare-go/v4/r2"
	"github.com/cloudflare/cloudflare-go/v4/shared"
	"github.com/cloudflare/cloudflare-go/v4/user"

	"github.com/ocelhq/ocel/cloud/edge"
)

const testAccountID = "acct-123"

type fakeBuckets struct {
	existing map[string]bool
	created  []r2.BucketNewParams
	newErr   error
}

func (f *fakeBuckets) Get(_ context.Context, name string, _ r2.BucketGetParams, _ ...option.RequestOption) (*r2.Bucket, error) {
	if f.existing[name] {
		return &r2.Bucket{Name: name}, nil
	}
	return nil, &cf.Error{StatusCode: http.StatusNotFound}
}

func (f *fakeBuckets) New(_ context.Context, params r2.BucketNewParams, _ ...option.RequestOption) (*r2.Bucket, error) {
	if f.newErr != nil {
		return nil, f.newErr
	}
	f.created = append(f.created, params)
	return &r2.Bucket{Name: params.Name.Value}, nil
}

type fakeTokens struct {
	existing    []shared.Token
	listErr     error
	newErr      error
	minted      []user.TokenNewParams
	value       string
	verifyFails int
	verifyCalls int
}

func (f *fakeTokens) List(_ context.Context, query user.TokenListParams, _ ...option.RequestOption) (*pagination.V4PagePaginationArray[shared.Token], error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if query.Page.Value > 1 {
		return &pagination.V4PagePaginationArray[shared.Token]{}, nil
	}
	return &pagination.V4PagePaginationArray[shared.Token]{Result: f.existing}, nil
}

func (f *fakeTokens) New(_ context.Context, body user.TokenNewParams, _ ...option.RequestOption) (*user.TokenNewResponse, error) {
	if f.newErr != nil {
		return nil, f.newErr
	}
	f.minted = append(f.minted, body)
	return &user.TokenNewResponse{ID: "token-id", Name: body.Name.Value, Value: f.value}, nil
}

func (f *fakeTokens) Verify(_ context.Context, _ ...option.RequestOption) (*user.TokenVerifyResponse, error) {
	f.verifyCalls++
	if f.verifyCalls <= f.verifyFails {
		return nil, &cf.Error{StatusCode: http.StatusForbidden}
	}
	return &user.TokenVerifyResponse{ID: "token-id"}, nil
}

type fakeGroups struct {
	err error
}

func (f *fakeGroups) List(_ context.Context, query user.TokenPermissionGroupListParams, _ ...option.RequestOption) (*pagination.SinglePage[user.TokenPermissionGroupListResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	if query.Name.Value != cacheStorePermissionGroup {
		return &pagination.SinglePage[user.TokenPermissionGroupListResponse]{}, nil
	}
	return &pagination.SinglePage[user.TokenPermissionGroupListResponse]{
		Result: []user.TokenPermissionGroupListResponse{{ID: "group-id", Name: cacheStorePermissionGroup}},
	}, nil
}

// newTestStore wires the cache store to fakes and drops the propagation wait, so
// the retry path runs at test speed.
func newTestStore(buckets *fakeBuckets, tokens *fakeTokens, groups *fakeGroups) cacheStore {
	if buckets.existing == nil {
		buckets.existing = map[string]bool{}
	}
	return cacheStore{buckets: buckets, tokens: tokens, groups: groups, sleep: func(time.Duration) {}}
}

func offerValues(t *testing.T, out edge.BootstrapOutput) map[string]string {
	t.Helper()
	for _, offer := range out.Offers {
		if offer.Kind == edge.OfferCacheStore {
			return offer.Values
		}
	}
	t.Fatalf("no %q offer in %v", edge.OfferCacheStore, out.Offers)
	return nil
}

func TestCacheStoreBootstrap_CreatesBucketAndMintsScopedToken(t *testing.T) {
	buckets := &fakeBuckets{}
	tokens := &fakeTokens{value: "token-value"}
	store := newTestStore(buckets, tokens, &fakeGroups{})

	out, err := store.bootstrap(context.Background(), testAccountID, edge.ClassProduction)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if out.Trust != edge.TrustExternal {
		t.Errorf("Trust = %q, want %q", out.Trust, edge.TrustExternal)
	}

	if len(buckets.created) != 1 {
		t.Fatalf("created %d buckets, want 1", len(buckets.created))
	}
	bucket := buckets.created[0].Name.Value
	if bucket != cacheStoreName(edge.ClassProduction) {
		t.Errorf("created bucket %q, want %q", bucket, cacheStoreName(edge.ClassProduction))
	}

	if len(tokens.minted) != 1 {
		t.Fatalf("minted %d tokens, want 1", len(tokens.minted))
	}
	policies := tokens.minted[0].Policies.Value
	if len(policies) != 1 {
		t.Fatalf("token carries %d policies, want 1", len(policies))
	}
	resources := policies[0].Resources.Value
	wantResource := "com.cloudflare.edge.r2.bucket." + testAccountID + "_default_" + bucket
	if _, ok := resources[wantResource]; !ok {
		t.Errorf("token policy resources = %v, want it scoped to %q", resources, wantResource)
	}
	if len(resources) != 1 {
		t.Errorf("token policy resources = %v, want exactly the one bucket", resources)
	}

	values := offerValues(t, out)
	sum := sha256.Sum256([]byte("token-value"))
	want := map[string]string{
		edge.OfferKeyBucket:          bucket,
		edge.OfferKeyEndpoint:        "https://" + testAccountID + ".r2.cloudflarestorage.com",
		edge.OfferKeyRegion:          r2Region,
		edge.OfferKeyAccessKeyID:     "token-id",
		edge.OfferKeySecretAccessKey: hex.EncodeToString(sum[:]),
	}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("offer[%q] = %q, want %q", k, values[k], v)
		}
	}
	if out.Values[valueKeyCacheBucket] != bucket {
		t.Errorf("Values[%q] = %q, want %q", valueKeyCacheBucket, out.Values[valueKeyCacheBucket], bucket)
	}
}

func TestCacheStoreBootstrap_SeparatesProductionAndPreview(t *testing.T) {
	names := map[edge.Class]string{}
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		buckets := &fakeBuckets{}
		tokens := &fakeTokens{value: "token-value"}
		out, err := newTestStore(buckets, tokens, &fakeGroups{}).bootstrap(context.Background(), testAccountID, class)
		if err != nil {
			t.Fatalf("bootstrap %s: %v", class, err)
		}
		names[class] = offerValues(t, out)[edge.OfferKeyBucket]
		if got := tokens.minted[0].Name.Value; got != names[class] {
			t.Errorf("%s minted token %q, want it named for its own bucket %q", class, got, names[class])
		}
	}
	if names[edge.ClassProduction] == names[edge.ClassPreview] {
		t.Errorf("production and preview share the bucket %q; each class needs its own", names[edge.ClassProduction])
	}
}

func TestCacheStoreBootstrap_ReusesExistingBucketAndToken(t *testing.T) {
	name := cacheStoreName(edge.ClassProduction)
	buckets := &fakeBuckets{existing: map[string]bool{name: true}}
	tokens := &fakeTokens{existing: []shared.Token{{ID: "already-minted", Name: name}}}

	out, err := newTestStore(buckets, tokens, &fakeGroups{}).bootstrap(context.Background(), testAccountID, edge.ClassProduction)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(buckets.created) != 0 {
		t.Errorf("created %v, want the existing bucket reused", buckets.created)
	}
	if len(tokens.minted) != 0 {
		t.Errorf("minted %d tokens, want the existing one reused", len(tokens.minted))
	}

	values := offerValues(t, out)
	if values[edge.OfferKeyAccessKeyID] != "already-minted" {
		t.Errorf("offer access key id = %q, want the existing token's id", values[edge.OfferKeyAccessKeyID])
	}
	if _, ok := values[edge.OfferKeySecretAccessKey]; ok {
		t.Error("a reused token has no readable value, so the offer must carry no secret")
	}
}

func TestCacheStoreBootstrap_MissingMintPermissionNamesTheTemplate(t *testing.T) {
	tokens := &fakeTokens{value: "token-value", newErr: &cf.Error{StatusCode: http.StatusForbidden}}

	_, err := newTestStore(&fakeBuckets{}, tokens, &fakeGroups{}).bootstrap(context.Background(), testAccountID, edge.ClassProduction)
	if err == nil {
		t.Fatal("expected an error when the operator's token cannot mint")
	}
	for _, want := range []string{"API Tokens Write", "Create Additional Tokens", "reissue"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("diagnostic must mention %q, got: %v", want, err)
		}
	}
}

func TestCacheStoreBootstrap_ToleratesTokenPropagationDelay(t *testing.T) {
	tokens := &fakeTokens{value: "token-value", verifyFails: 2}

	out, err := newTestStore(&fakeBuckets{}, tokens, &fakeGroups{}).bootstrap(context.Background(), testAccountID, edge.ClassProduction)
	if err != nil {
		t.Fatalf("a 403 while a freshly minted token propagates is not fatal: %v", err)
	}
	if tokens.verifyCalls != 3 {
		t.Errorf("verified %d times, want a retry until the token propagates", tokens.verifyCalls)
	}
	if offerValues(t, out)[edge.OfferKeySecretAccessKey] == "" {
		t.Error("offer must carry the minted secret once the token propagates")
	}
}

func TestCacheStoreBootstrap_MintedButUnusableNamesTheRecoveryPath(t *testing.T) {
	name := cacheStoreName(edge.ClassProduction)
	tokens := &fakeTokens{value: "token-value", verifyFails: tokenPropagationAttempts}

	_, err := newTestStore(&fakeBuckets{}, tokens, &fakeGroups{}).bootstrap(context.Background(), testAccountID, edge.ClassProduction)
	if err == nil {
		t.Fatal("expected an error when the minted token never becomes usable")
	}
	// The token exists in Cloudflare but nothing downstream stored it: name it and
	// the recovery, or the next run reuses a credential no one holds.
	for _, want := range []string{name, "token-id", "delete"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("diagnostic must mention %q, got: %v", want, err)
		}
	}
}

func TestCacheStoreBootstrap_UnknownClassErrors(t *testing.T) {
	_, err := newTestStore(&fakeBuckets{}, &fakeTokens{}, &fakeGroups{}).bootstrap(context.Background(), testAccountID, edge.Class("staging"))
	if err == nil {
		t.Fatal("expected an error for a substrate class with no cache store")
	}
}
