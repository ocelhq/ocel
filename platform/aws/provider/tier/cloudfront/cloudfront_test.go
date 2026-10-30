package cloudfront_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront/resolver"
	cloudfrontpart "github.com/ocelhq/ocel/platform/aws/provider/tier/cloudfront"
)

const accountID = "123456789012"

type function struct {
	config    *cftypes.FunctionConfig
	code      []byte
	published []byte
	etag      int
}

type fakeCloudFront struct {
	stores            map[string]string
	provisioningReads int
	provisioning      map[string]int
	functions         map[string]*function
	caches            map[string]*cftypes.CachePolicyConfig
	headers           map[string]*cftypes.ResponseHeadersPolicyConfig
	access            map[string]*cftypes.OriginAccessControlConfig
	calls             []string
	next              int
}

func newAccount() *fakeCloudFront {
	return &fakeCloudFront{
		stores:       map[string]string{},
		provisioning: map[string]int{},
		functions:    map[string]*function{},
		caches:       map[string]*cftypes.CachePolicyConfig{},
		headers:      map[string]*cftypes.ResponseHeadersPolicyConfig{},
		access:       map[string]*cftypes.OriginAccessControlConfig{},
	}
}

func (a *fakeCloudFront) id(prefix string) string {
	a.next++
	return fmt.Sprintf("%s%d", prefix, a.next)
}

func (a *fakeCloudFront) record(call string) { a.calls = append(a.calls, call) }

func (a *fakeCloudFront) CreateKeyValueStore(_ context.Context, in *cloudfront.CreateKeyValueStoreInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateKeyValueStoreOutput, error) {
	a.record("CreateKeyValueStore")
	name := aws.ToString(in.Name)
	if _, ok := a.stores[name]; ok {
		return nil, &cftypes.EntityAlreadyExists{Message: aws.String("exists")}
	}
	a.stores[name] = "arn:aws:cloudfront::" + accountID + ":key-value-store/" + a.id("kvs-")
	a.provisioning[name] = a.provisioningReads
	return &cloudfront.CreateKeyValueStoreOutput{KeyValueStore: &cftypes.KeyValueStore{ARN: aws.String(a.stores[name]), Status: aws.String(a.statusOf(name))}}, nil
}

func (a *fakeCloudFront) statusOf(name string) string {
	if a.provisioning[name] > 0 {
		return "PROVISIONING"
	}
	return "READY"
}

func (a *fakeCloudFront) DescribeKeyValueStore(_ context.Context, in *cloudfront.DescribeKeyValueStoreInput, _ ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error) {
	name := aws.ToString(in.Name)
	arn, ok := a.stores[name]
	if !ok {
		return nil, &cftypes.EntityNotFound{Message: aws.String("no store")}
	}
	status := a.statusOf(name)
	if a.provisioning[name] > 0 {
		a.provisioning[name]--
	}
	return &cloudfront.DescribeKeyValueStoreOutput{KeyValueStore: &cftypes.KeyValueStore{ARN: aws.String(arn), Status: aws.String(status)}, ETag: aws.String("e")}, nil
}

func (a *fakeCloudFront) DeleteKeyValueStore(_ context.Context, in *cloudfront.DeleteKeyValueStoreInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteKeyValueStoreOutput, error) {
	for name, fn := range a.functions {
		if fn.config.KeyValueStoreAssociations != nil {
			return nil, &cftypes.CannotDeleteEntityWhileInUse{Message: aws.String("function " + name + " reads the store")}
		}
	}
	delete(a.stores, aws.ToString(in.Name))
	return &cloudfront.DeleteKeyValueStoreOutput{}, nil
}

func etagOf(fn *function) *string { return aws.String(fmt.Sprintf("etag-%d", fn.etag)) }

func (a *fakeCloudFront) CreateFunction(_ context.Context, in *cloudfront.CreateFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateFunctionOutput, error) {
	a.record("CreateFunction " + aws.ToString(in.Name))
	if stores := in.FunctionConfig.KeyValueStoreAssociations; stores != nil {
		for _, item := range stores.Items {
			for name, arn := range a.stores {
				if arn == aws.ToString(item.KeyValueStoreARN) && a.provisioning[name] > 0 {
					return nil, &cftypes.InvalidArgument{Message: aws.String("The parameter KeyValueStoreAssociationArn is invalid; " + arn + " cannot be associated before the resource is provisioned.")}
				}
			}
		}
	}
	fn := &function{config: in.FunctionConfig, code: in.FunctionCode, etag: 1}
	a.functions[aws.ToString(in.Name)] = fn
	return &cloudfront.CreateFunctionOutput{ETag: etagOf(fn)}, nil
}

func (a *fakeCloudFront) DescribeFunction(_ context.Context, in *cloudfront.DescribeFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.DescribeFunctionOutput, error) {
	fn, ok := a.functions[aws.ToString(in.Name)]
	if !ok {
		return nil, &cftypes.NoSuchFunctionExists{Message: aws.String("no function")}
	}
	return &cloudfront.DescribeFunctionOutput{ETag: etagOf(fn)}, nil
}

func (a *fakeCloudFront) UpdateFunction(_ context.Context, in *cloudfront.UpdateFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.UpdateFunctionOutput, error) {
	a.record("UpdateFunction " + aws.ToString(in.Name))
	fn := a.functions[aws.ToString(in.Name)]
	if aws.ToString(in.IfMatch) != aws.ToString(etagOf(fn)) {
		return nil, &cftypes.PreconditionFailed{Message: aws.String("stale etag")}
	}
	fn.config, fn.code = in.FunctionConfig, in.FunctionCode
	fn.etag++
	return &cloudfront.UpdateFunctionOutput{ETag: etagOf(fn)}, nil
}

func (a *fakeCloudFront) PublishFunction(_ context.Context, in *cloudfront.PublishFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.PublishFunctionOutput, error) {
	name := aws.ToString(in.Name)
	fn := a.functions[name]
	if aws.ToString(in.IfMatch) != aws.ToString(etagOf(fn)) {
		return nil, &cftypes.PreconditionFailed{Message: aws.String("stale etag")}
	}
	fn.published = fn.code
	return &cloudfront.PublishFunctionOutput{FunctionSummary: &cftypes.FunctionSummary{
		FunctionMetadata: &cftypes.FunctionMetadata{FunctionARN: aws.String("arn:aws:cloudfront::" + accountID + ":function/" + name)},
	}}, nil
}

func (a *fakeCloudFront) DeleteFunction(_ context.Context, in *cloudfront.DeleteFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteFunctionOutput, error) {
	delete(a.functions, aws.ToString(in.Name))
	return &cloudfront.DeleteFunctionOutput{}, nil
}

func (a *fakeCloudFront) CreateCachePolicy(_ context.Context, in *cloudfront.CreateCachePolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateCachePolicyOutput, error) {
	a.record("CreateCachePolicy")
	for _, existing := range a.caches {
		if aws.ToString(existing.Name) == aws.ToString(in.CachePolicyConfig.Name) {
			return nil, &cftypes.CachePolicyAlreadyExists{Message: aws.String("exists")}
		}
	}
	id := a.id("cache-")
	a.caches[id] = in.CachePolicyConfig
	return &cloudfront.CreateCachePolicyOutput{CachePolicy: &cftypes.CachePolicy{Id: aws.String(id)}}, nil
}

func (a *fakeCloudFront) GetCachePolicy(_ context.Context, in *cloudfront.GetCachePolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetCachePolicyOutput, error) {
	if _, ok := a.caches[aws.ToString(in.Id)]; !ok {
		return nil, &cftypes.NoSuchCachePolicy{Message: aws.String("no policy")}
	}
	return &cloudfront.GetCachePolicyOutput{ETag: aws.String("e")}, nil
}

func (a *fakeCloudFront) UpdateCachePolicy(_ context.Context, in *cloudfront.UpdateCachePolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.UpdateCachePolicyOutput, error) {
	a.record("UpdateCachePolicy")
	a.caches[aws.ToString(in.Id)] = in.CachePolicyConfig
	return &cloudfront.UpdateCachePolicyOutput{}, nil
}

func (a *fakeCloudFront) ListCachePolicies(_ context.Context, _ *cloudfront.ListCachePoliciesInput, _ ...func(*cloudfront.Options)) (*cloudfront.ListCachePoliciesOutput, error) {
	a.record("ListCachePolicies")
	var items []cftypes.CachePolicySummary
	for id, config := range a.caches {
		items = append(items, cftypes.CachePolicySummary{CachePolicy: &cftypes.CachePolicy{Id: aws.String(id), CachePolicyConfig: config}})
	}
	return &cloudfront.ListCachePoliciesOutput{CachePolicyList: &cftypes.CachePolicyList{Items: items}}, nil
}

func (a *fakeCloudFront) DeleteCachePolicy(_ context.Context, in *cloudfront.DeleteCachePolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteCachePolicyOutput, error) {
	delete(a.caches, aws.ToString(in.Id))
	return &cloudfront.DeleteCachePolicyOutput{}, nil
}

func (a *fakeCloudFront) CreateResponseHeadersPolicy(_ context.Context, in *cloudfront.CreateResponseHeadersPolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateResponseHeadersPolicyOutput, error) {
	id := a.id("headers-")
	a.headers[id] = in.ResponseHeadersPolicyConfig
	return &cloudfront.CreateResponseHeadersPolicyOutput{ResponseHeadersPolicy: &cftypes.ResponseHeadersPolicy{Id: aws.String(id)}}, nil
}

func (a *fakeCloudFront) GetResponseHeadersPolicy(_ context.Context, in *cloudfront.GetResponseHeadersPolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetResponseHeadersPolicyOutput, error) {
	if _, ok := a.headers[aws.ToString(in.Id)]; !ok {
		return nil, &cftypes.NoSuchResponseHeadersPolicy{Message: aws.String("no policy")}
	}
	return &cloudfront.GetResponseHeadersPolicyOutput{ETag: aws.String("e")}, nil
}

func (a *fakeCloudFront) UpdateResponseHeadersPolicy(_ context.Context, in *cloudfront.UpdateResponseHeadersPolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.UpdateResponseHeadersPolicyOutput, error) {
	a.headers[aws.ToString(in.Id)] = in.ResponseHeadersPolicyConfig
	return &cloudfront.UpdateResponseHeadersPolicyOutput{}, nil
}

func (a *fakeCloudFront) ListResponseHeadersPolicies(context.Context, *cloudfront.ListResponseHeadersPoliciesInput, ...func(*cloudfront.Options)) (*cloudfront.ListResponseHeadersPoliciesOutput, error) {
	return &cloudfront.ListResponseHeadersPoliciesOutput{ResponseHeadersPolicyList: &cftypes.ResponseHeadersPolicyList{}}, nil
}

func (a *fakeCloudFront) DeleteResponseHeadersPolicy(_ context.Context, in *cloudfront.DeleteResponseHeadersPolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteResponseHeadersPolicyOutput, error) {
	delete(a.headers, aws.ToString(in.Id))
	return &cloudfront.DeleteResponseHeadersPolicyOutput{}, nil
}

func (a *fakeCloudFront) CreateOriginAccessControl(_ context.Context, in *cloudfront.CreateOriginAccessControlInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateOriginAccessControlOutput, error) {
	id := a.id("oac-")
	a.access[id] = in.OriginAccessControlConfig
	return &cloudfront.CreateOriginAccessControlOutput{OriginAccessControl: &cftypes.OriginAccessControl{Id: aws.String(id)}}, nil
}

func (a *fakeCloudFront) GetOriginAccessControl(_ context.Context, in *cloudfront.GetOriginAccessControlInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetOriginAccessControlOutput, error) {
	if _, ok := a.access[aws.ToString(in.Id)]; !ok {
		return nil, &cftypes.NoSuchOriginAccessControl{Message: aws.String("no oac")}
	}
	return &cloudfront.GetOriginAccessControlOutput{ETag: aws.String("e")}, nil
}

func (a *fakeCloudFront) UpdateOriginAccessControl(_ context.Context, in *cloudfront.UpdateOriginAccessControlInput, _ ...func(*cloudfront.Options)) (*cloudfront.UpdateOriginAccessControlOutput, error) {
	a.access[aws.ToString(in.Id)] = in.OriginAccessControlConfig
	return &cloudfront.UpdateOriginAccessControlOutput{}, nil
}

func (a *fakeCloudFront) ListOriginAccessControls(context.Context, *cloudfront.ListOriginAccessControlsInput, ...func(*cloudfront.Options)) (*cloudfront.ListOriginAccessControlsOutput, error) {
	return &cloudfront.ListOriginAccessControlsOutput{OriginAccessControlList: &cftypes.OriginAccessControlList{}}, nil
}

func (a *fakeCloudFront) DeleteOriginAccessControl(_ context.Context, in *cloudfront.DeleteOriginAccessControlInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteOriginAccessControlOutput, error) {
	delete(a.access, aws.ToString(in.Id))
	return &cloudfront.DeleteOriginAccessControlOutput{}, nil
}

func spec(tier environment.Tier) cloudfrontpart.Spec {
	return cloudfrontpart.Spec{Namespace: account.Namespace("ocel"), Tier: tier}
}

func TestEnsureMakesEverythingADistributionSharesAndRecordsHowToFindIt(t *testing.T) {
	t.Parallel()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		a := newAccount()
		names := cloudfrontpart.NamesFor(account.Namespace("ocel"), tier)

		resources, err := cloudfrontpart.Ensure(context.Background(), a, spec(tier), nil)
		if err != nil {
			t.Fatalf("Ensure() = %v", err)
		}

		set := cloudfrontpart.SetOf(names, resources)
		if !set.Complete() {
			t.Fatalf("SetOf(resources) = %+v, want every piece a distribution names", set)
		}
		if set.RoutesStore != a.stores[names.RoutesStore] {
			t.Errorf("routes store = %q, want the one made under %s", set.RoutesStore, names.RoutesStore)
		}
		resolverFn := a.functions[names.Resolver]
		if resolverFn == nil || string(resolverFn.published) != string(resolver.Code()) {
			t.Fatalf("resolver %s is not published with the code this build embeds", names.Resolver)
		}
		if got := resolverFn.config.KeyValueStoreAssociations.Items; len(got) != 1 || aws.ToString(got[0].KeyValueStoreARN) != set.RoutesStore {
			t.Errorf("resolver reads %v, want the routes store it was made beside", got)
		}
		if body := a.functions[names.EmptyBody]; body == nil || string(body.published) != string(resolver.EmptyBodyCode()) {
			t.Errorf("empty-body function %s is not published with the code this build embeds", names.EmptyBody)
		}
		cache := a.caches[set.CachePolicy]
		if got := cache.ParametersInCacheKeyAndForwardedToOrigin.HeadersConfig.Headers.Items; slices.Contains(got, "Host") {
			t.Errorf("cache policy keys on %v, which forwards the viewer's Host to a function URL that answers it 403", got)
		}
		headers := a.headers[set.HeadersPolicy]
		var removed []string
		for _, item := range headers.RemoveHeadersConfig.Items {
			removed = append(removed, aws.ToString(item.Header))
		}
		for _, header := range []string{cloudfrontpart.CacheTagHeader, edge.HeaderEmptyBody} {
			if !slices.Contains(removed, header) {
				t.Errorf("removed headers = %v, want %s among them: nothing of ocel's own reaches a browser", removed, header)
			}
		}
		noindex := false
		for _, item := range headers.CustomHeadersConfig.Items {
			noindex = noindex || aws.ToString(item.Header) == "x-robots-tag"
			if aws.ToString(item.Header) == router.HeaderRouter && aws.ToString(item.Value) != cloudfrontpart.Kind {
				t.Errorf("%s = %q, want %q", router.HeaderRouter, aws.ToString(item.Value), cloudfrontpart.Kind)
			}
		}
		if want := tier == environment.TierPreview; noindex != want {
			t.Errorf("the %s headers policy marks answers noindex = %t, want %t: only previews are kept out of search engines", tier, noindex, want)
		}
		access := a.access[set.AssetAccess]
		if access.OriginAccessControlOriginType != cftypes.OriginAccessControlOriginTypesS3 || access.SigningBehavior != cftypes.OriginAccessControlSigningBehaviorsAlways {
			t.Errorf("asset access = %+v, want every read of the asset bucket signed", access)
		}
	}
}

func TestEnsureOnAFreshAccountWaitsForTheRoutesStoreBeforeTheResolverReadsIt(t *testing.T) {
	t.Parallel()
	a := newAccount()
	a.provisioningReads = 1
	names := cloudfrontpart.NamesFor(account.Namespace("ocel"), environment.TierProduction)

	resources, err := cloudfrontpart.Ensure(context.Background(), a, spec(environment.TierProduction), nil)
	if err != nil {
		t.Fatalf("Ensure() = %v, want the resolver made once its routes store is provisioned", err)
	}
	if set := cloudfrontpart.SetOf(names, resources); set.RoutesStore != a.stores[names.RoutesStore] {
		t.Errorf("routes store = %q, want the one made under %s", set.RoutesStore, names.RoutesStore)
	}
}

func TestEnsureAgainUpdatesWhatTheRecordNamesRatherThanMakingMore(t *testing.T) {
	t.Parallel()
	a := newAccount()
	first, err := cloudfrontpart.Ensure(context.Background(), a, spec(environment.TierProduction), nil)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	second, err := cloudfrontpart.Ensure(context.Background(), a, spec(environment.TierProduction), first)
	if err != nil {
		t.Fatalf("Ensure() again = %v", err)
	}

	if !slices.Equal(first, second) {
		t.Errorf("resources moved from %v to %v, want the same pieces moved forward in place", first, second)
	}
	if len(a.caches) != 1 || len(a.headers) != 1 || len(a.access) != 1 || len(a.stores) != 1 {
		t.Errorf("made %d cache, %d headers, %d access and %d store, want one of each", len(a.caches), len(a.headers), len(a.access), len(a.stores))
	}
	if slices.Contains(a.calls, "ListCachePolicies") {
		t.Errorf("calls = %v, want the recorded cache policy reached by id, not found by listing the account's", a.calls)
	}
}

func TestEnsureFindsACachePolicyItMadeWhoseRecordWasLost(t *testing.T) {
	t.Parallel()
	a := newAccount()
	first, err := cloudfrontpart.Ensure(context.Background(), a, spec(environment.TierProduction), nil)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	again, err := cloudfrontpart.Ensure(context.Background(), a, spec(environment.TierProduction), nil)
	if err != nil {
		t.Fatalf("Ensure() with no record = %v", err)
	}

	names := cloudfrontpart.NamesFor(account.Namespace("ocel"), environment.TierProduction)
	if got, want := cloudfrontpart.SetOf(names, again).CachePolicy, cloudfrontpart.SetOf(names, first).CachePolicy; got != want || len(a.caches) != 1 {
		t.Errorf("cache policy = %q of %d, want the one already made, %q", got, len(a.caches), want)
	}
}

func TestRemoveTakesEveryPieceDownFunctionsBeforeTheStoreTheyRead(t *testing.T) {
	t.Parallel()
	a := newAccount()
	resources, err := cloudfrontpart.Ensure(context.Background(), a, spec(environment.TierPreview), nil)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	for range 2 {
		if err := cloudfrontpart.Remove(context.Background(), a, spec(environment.TierPreview), resources); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
	}

	if len(a.functions)+len(a.stores)+len(a.caches)+len(a.headers)+len(a.access) != 0 {
		t.Errorf("left functions %v stores %v caches %v headers %v access %v, want none", a.functions, a.stores, a.caches, a.headers, a.access)
	}
}

func TestDigestMovesWithTheResolverCode(t *testing.T) {
	t.Parallel()
	production, preview := cloudfrontpart.Digest(spec(environment.TierProduction)), cloudfrontpart.Digest(spec(environment.TierPreview))

	if production == preview || len(production) != 64 {
		t.Errorf("digests %q and %q, want a distinct sha256 per tier", production, preview)
	}
}

func TestEveryCommentFitsTheLengthCloudFrontAccepts(t *testing.T) {
	t.Parallel()
	a := newAccount()
	if _, err := cloudfrontpart.Ensure(context.Background(), a, spec(environment.TierPreview), nil); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	const limit = 128
	var comments []string
	for _, fn := range a.functions {
		comments = append(comments, aws.ToString(fn.config.Comment))
	}
	for _, config := range a.caches {
		comments = append(comments, aws.ToString(config.Comment))
	}
	for _, config := range a.headers {
		comments = append(comments, aws.ToString(config.Comment))
	}
	for _, comment := range comments {
		if len(comment) > limit {
			t.Errorf("comment %q is %d characters; CloudFront refuses one over %d", comment, len(comment), limit)
		}
	}
}
