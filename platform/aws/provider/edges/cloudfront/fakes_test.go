package cloudfront

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore"
	kvstypes "github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	smithy "github.com/aws/smithy-go"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	fakeStateTable  = "ocel-state"
	fakeAssetBucket = "ocel-assets"
	fakeRegion      = "eu-west-1"
	fakeSecret      = "3f7a9c1e5b2d84600f1a2b3c4d5e6f70"
	fakeEntryURL    = "https://abcdef0123456789.lambda-url.eu-west-1.on.aws/"
	fakeEntryHost   = "abcdef0123456789.lambda-url.eu-west-1.on.aws"
	fakeAssetPrefix = "production/conformance/web/d1.f1/assets"
)

type trail struct {
	mu    sync.Mutex
	steps []string
}

func (t *trail) record(step string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append(t.steps, step)
}

func (t *trail) taken() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.steps)
}

type world struct {
	front  *fakeCloudFront
	store  *fakeKeyValueStore
	dynamo *fakeDynamo
	ssm    *fakeSSM
	cfn    *fakeCFN
	trail  *trail
}

func newWorld() *world {
	shared := &trail{}
	return &world{
		front:  newFakeCloudFront(shared),
		store:  newFakeKeyValueStore(shared),
		dynamo: newFakeDynamo(shared),
		ssm:    newFakeSSM(),
		cfn:    &fakeCFN{},
		trail:  shared,
	}
}

func (w *world) clients() Clients {
	return Clients{
		CloudFront:    w.front,
		KeyValueStore: w.store,
		Dynamo:        w.dynamo,
		SSM:           w.ssm,
		CFN:           w.cfn,
		Region:        fakeRegion,
	}
}

func (w *world) invalidationTargets(scope string) []string {
	body, _ := w.dynamo.items["ledger#"+scope+"\x00invalidation#"]["body"].(*ddbtypes.AttributeValueMemberB)
	if body == nil {
		return nil
	}
	var targets []string
	if err := json.Unmarshal(body.Value, &targets); err != nil {
		return nil
	}
	return targets
}

func (w *world) edge() *provider {
	return &provider{
		open: func(context.Context) (Clients, error) { return w.clients(), nil },
		settle: Settler{
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 5,
			Every:    time.Second,
			Jitter:   func() float64 { return 0.5 },
		},
	}
}

type fakeCFN struct {
	absent bool
}

func (f *fakeCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	if f.absent {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName: in.StackName,
		Outputs: []cfntypes.Output{
			{OutputKey: aws.String("StateTableName"), OutputValue: aws.String(fakeStateTable)},
			{OutputKey: aws.String("AssetBucketName"), OutputValue: aws.String(fakeAssetBucket)},
		},
	}}}, nil
}

type fakeSSM struct {
	absent bool
}

func newFakeSSM() *fakeSSM { return &fakeSSM{} }

func (f *fakeSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	if f.absent {
		return nil, &ssmtypes.ParameterNotFound{Message: aws.String("no parameter " + aws.ToString(in.Name))}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{
		Name:  in.Name,
		Value: aws.String(fakeSecret),
	}}, nil
}

type fakeStore struct {
	arn          string
	etag         string
	provisioning int
}

func (s *fakeStore) status() string {
	if s.provisioning > 0 {
		return "PROVISIONING"
	}
	return keyValueStoreReady
}

type fakeFunction struct {
	code      []byte
	etag      string
	config    *cftypes.FunctionConfig
	published bool
}

type fakeDistribution struct {
	id      string
	domain  string
	etag    string
	config  *cftypes.DistributionConfig
	rollout int
	polls   int
}

func (d *fakeDistribution) status() string {
	if d.polls < d.rollout {
		return "InProgress"
	}
	return deployedStatus
}

type fakeCloudFront struct {
	mu    sync.Mutex
	trail *trail
	next  int
	calls []string

	stores        map[string]*fakeStore
	functions     map[string]*fakeFunction
	cachePolicies map[string]string
	headerPolicy  map[string]*cftypes.ResponseHeadersPolicyConfig
	accessControl map[string]string
	distributions map[string]*fakeDistribution

	createDistributionErr error
	aliasErr              error
	statusThrottles       int
	rollout               int
	cachePolicyPageSize   int
	storeProvisions       int
}

func newFakeCloudFront(shared *trail) *fakeCloudFront {
	return &fakeCloudFront{
		trail:         shared,
		rollout:       1,
		stores:        map[string]*fakeStore{},
		functions:     map[string]*fakeFunction{},
		cachePolicies: map[string]string{},
		headerPolicy:  map[string]*cftypes.ResponseHeadersPolicyConfig{},
		accessControl: map[string]string{},
		distributions: map[string]*fakeDistribution{},
	}
}

func (f *fakeCloudFront) record(call string) {
	f.calls = append(f.calls, call)
	f.trail.record(call)
}

func (f *fakeCloudFront) mutations() []string {
	var out []string
	for _, call := range f.calls {
		verb, _, _ := strings.Cut(call, " ")
		if strings.HasPrefix(verb, "Get") || strings.HasPrefix(verb, "List") || strings.HasPrefix(verb, "Describe") {
			continue
		}
		out = append(out, call)
	}
	return out
}

func (f *fakeCloudFront) count(verb string) int {
	n := 0
	for _, call := range f.calls {
		if call == verb || strings.HasPrefix(call, verb+" ") {
			n++
		}
	}
	return n
}

func (f *fakeCloudFront) id(prefix string) string {
	f.next++
	return prefix + strconv.Itoa(f.next)
}

func (f *fakeCloudFront) named(comment string) *fakeDistribution {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range slices.Sorted(maps.Keys(f.distributions)) {
		if aws.ToString(f.distributions[id].config.Comment) == comment {
			return f.distributions[id]
		}
	}
	return nil
}

const cloudFrontCommentCeiling = 128

func overlongComment(field, value string) error {
	if len(value) <= cloudFrontCommentCeiling {
		return nil
	}
	return &cftypes.InvalidArgument{Message: aws.String(fmt.Sprintf(
		"%s is %d characters; CloudFront allows %d", field, len(value), cloudFrontCommentCeiling))}
}

func (f *fakeCloudFront) CreateKeyValueStore(_ context.Context, in *cloudfront.CreateKeyValueStoreInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateKeyValueStoreOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("CreateKeyValueStore " + name)
	if err := overlongComment("key value store comment", aws.ToString(in.Comment)); err != nil {
		return nil, err
	}
	arn := "arn:aws:cloudfront::123456789012:key-value-store/" + name
	held := &fakeStore{arn: arn, etag: "kvs-1", provisioning: f.storeProvisions}
	f.stores[name] = held
	return &cloudfront.CreateKeyValueStoreOutput{
		ETag:          aws.String("kvs-1"),
		KeyValueStore: &cftypes.KeyValueStore{ARN: aws.String(arn), Name: in.Name, Status: aws.String(held.status())},
	}, nil
}

func (f *fakeCloudFront) DescribeKeyValueStore(_ context.Context, in *cloudfront.DescribeKeyValueStoreInput, _ ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("DescribeKeyValueStore " + name)
	held, ok := f.stores[name]
	if !ok {
		return nil, &cftypes.EntityNotFound{Message: aws.String("no key value store " + name)}
	}
	if held.provisioning > 0 {
		held.provisioning--
	}
	return &cloudfront.DescribeKeyValueStoreOutput{
		ETag:          aws.String(held.etag),
		KeyValueStore: &cftypes.KeyValueStore{ARN: aws.String(held.arn), Name: in.Name, Status: aws.String(held.status())},
	}, nil
}

func (f *fakeCloudFront) DeleteKeyValueStore(_ context.Context, in *cloudfront.DeleteKeyValueStoreInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteKeyValueStoreOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("DeleteKeyValueStore " + name)
	if _, ok := f.stores[name]; !ok {
		return nil, &cftypes.EntityNotFound{Message: aws.String("no key value store " + name)}
	}
	delete(f.stores, name)
	return &cloudfront.DeleteKeyValueStoreOutput{}, nil
}

func (f *fakeCloudFront) CreateFunction(_ context.Context, in *cloudfront.CreateFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateFunctionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("CreateFunction " + name)
	if err := overlongComment("function comment", aws.ToString(in.FunctionConfig.Comment)); err != nil {
		return nil, err
	}
	f.functions[name] = &fakeFunction{code: slices.Clone(in.FunctionCode), etag: "fn-1", config: in.FunctionConfig}
	return &cloudfront.CreateFunctionOutput{ETag: aws.String("fn-1")}, nil
}

func (f *fakeCloudFront) GetFunction(_ context.Context, in *cloudfront.GetFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetFunctionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("GetFunction " + name)
	held, ok := f.functions[name]
	if !ok {
		return nil, &cftypes.NoSuchFunctionExists{Message: aws.String("no function " + name)}
	}
	return &cloudfront.GetFunctionOutput{ETag: aws.String(held.etag), FunctionCode: slices.Clone(held.code)}, nil
}

func (f *fakeCloudFront) DescribeFunction(_ context.Context, in *cloudfront.DescribeFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.DescribeFunctionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("DescribeFunction " + name + " " + string(in.Stage))
	held, ok := f.functions[name]
	if !ok {
		return nil, &cftypes.NoSuchFunctionExists{Message: aws.String("no function " + name)}
	}
	if in.Stage == cftypes.FunctionStageLive && !held.published {
		return nil, &cftypes.NoSuchFunctionExists{Message: aws.String("function " + name + " is not published")}
	}
	return &cloudfront.DescribeFunctionOutput{
		ETag: aws.String(held.etag),
		FunctionSummary: &cftypes.FunctionSummary{
			Name:             in.Name,
			FunctionConfig:   held.config,
			FunctionMetadata: &cftypes.FunctionMetadata{FunctionARN: aws.String("arn:aws:cloudfront::123456789012:function/" + name)},
		},
	}, nil
}

func (f *fakeCloudFront) UpdateFunction(_ context.Context, in *cloudfront.UpdateFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.UpdateFunctionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("UpdateFunction " + name)
	if err := overlongComment("function comment", aws.ToString(in.FunctionConfig.Comment)); err != nil {
		return nil, err
	}
	held, ok := f.functions[name]
	if !ok {
		return nil, &cftypes.NoSuchFunctionExists{Message: aws.String("no function " + name)}
	}
	if aws.ToString(in.IfMatch) != held.etag {
		return nil, &cftypes.InvalidIfMatchVersion{Message: aws.String("stale etag for " + name)}
	}
	held.code = slices.Clone(in.FunctionCode)
	held.config = in.FunctionConfig
	held.etag = f.id("fn-")
	return &cloudfront.UpdateFunctionOutput{ETag: aws.String(held.etag)}, nil
}

func (f *fakeCloudFront) PublishFunction(_ context.Context, in *cloudfront.PublishFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.PublishFunctionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("PublishFunction " + name)
	held, ok := f.functions[name]
	if !ok {
		return nil, &cftypes.NoSuchFunctionExists{Message: aws.String("no function " + name)}
	}
	if aws.ToString(in.IfMatch) != held.etag {
		return nil, &cftypes.InvalidIfMatchVersion{Message: aws.String("stale etag for " + name)}
	}
	held.published = true
	return &cloudfront.PublishFunctionOutput{FunctionSummary: &cftypes.FunctionSummary{
		Name:             in.Name,
		FunctionMetadata: &cftypes.FunctionMetadata{FunctionARN: aws.String("arn:aws:cloudfront::123456789012:function/" + name)},
	}}, nil
}

func (f *fakeCloudFront) DeleteFunction(_ context.Context, in *cloudfront.DeleteFunctionInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteFunctionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("DeleteFunction " + name)
	if _, ok := f.functions[name]; !ok {
		return nil, &cftypes.NoSuchFunctionExists{Message: aws.String("no function " + name)}
	}
	delete(f.functions, name)
	return &cloudfront.DeleteFunctionOutput{}, nil
}

func (f *fakeCloudFront) ListCachePolicies(_ context.Context, in *cloudfront.ListCachePoliciesInput, _ ...func(*cloudfront.Options)) (*cloudfront.ListCachePoliciesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListCachePolicies")
	ids := slices.Sorted(maps.Keys(f.cachePolicies))
	if after := aws.ToString(in.Marker); after != "" {
		at := slices.Index(ids, after)
		if at < 0 {
			return nil, fmt.Errorf("fake cloudfront got the marker %q, which it never handed out", after)
		}
		ids = ids[at+1:]
	}
	page, more := ids, ""
	if size := f.cachePolicyPageSize; size > 0 && len(ids) > size {
		page = ids[:size]
		more = page[len(page)-1]
	}
	items := make([]cftypes.CachePolicySummary, 0, len(page))
	for _, id := range page {
		items = append(items, cftypes.CachePolicySummary{CachePolicy: &cftypes.CachePolicy{
			Id:                aws.String(id),
			CachePolicyConfig: &cftypes.CachePolicyConfig{Name: aws.String(f.cachePolicies[id])},
		}})
	}
	list := &cftypes.CachePolicyList{Items: items}
	if more != "" {
		list.NextMarker = aws.String(more)
	}
	return &cloudfront.ListCachePoliciesOutput{CachePolicyList: list}, nil
}

func (f *fakeCloudFront) CreateCachePolicy(_ context.Context, in *cloudfront.CreateCachePolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateCachePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.CachePolicyConfig.Name)
	f.record("CreateCachePolicy " + name)
	if err := overlongComment("cache policy comment", aws.ToString(in.CachePolicyConfig.Comment)); err != nil {
		return nil, err
	}
	id := f.id("cache-")
	f.cachePolicies[id] = name
	return &cloudfront.CreateCachePolicyOutput{CachePolicy: &cftypes.CachePolicy{
		Id:                aws.String(id),
		CachePolicyConfig: in.CachePolicyConfig,
	}}, nil
}

func (f *fakeCloudFront) DeleteCachePolicy(_ context.Context, in *cloudfront.DeleteCachePolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteCachePolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.Id)
	f.record("DeleteCachePolicy " + id)
	if _, ok := f.cachePolicies[id]; !ok {
		return nil, &cftypes.NoSuchCachePolicy{Message: aws.String("no cache policy " + id)}
	}
	delete(f.cachePolicies, id)
	return &cloudfront.DeleteCachePolicyOutput{}, nil
}

func (f *fakeCloudFront) ListResponseHeadersPolicies(_ context.Context, _ *cloudfront.ListResponseHeadersPoliciesInput, _ ...func(*cloudfront.Options)) (*cloudfront.ListResponseHeadersPoliciesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListResponseHeadersPolicies")
	items := make([]cftypes.ResponseHeadersPolicySummary, 0, len(f.headerPolicy))
	for _, id := range slices.Sorted(maps.Keys(f.headerPolicy)) {
		items = append(items, cftypes.ResponseHeadersPolicySummary{ResponseHeadersPolicy: &cftypes.ResponseHeadersPolicy{
			Id:                          aws.String(id),
			ResponseHeadersPolicyConfig: f.headerPolicy[id],
		}})
	}
	return &cloudfront.ListResponseHeadersPoliciesOutput{
		ResponseHeadersPolicyList: &cftypes.ResponseHeadersPolicyList{Items: items},
	}, nil
}

func (f *fakeCloudFront) CreateResponseHeadersPolicy(_ context.Context, in *cloudfront.CreateResponseHeadersPolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateResponseHeadersPolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.ResponseHeadersPolicyConfig.Name)
	f.record("CreateResponseHeadersPolicy " + name)
	if err := overlongComment("response headers policy comment", aws.ToString(in.ResponseHeadersPolicyConfig.Comment)); err != nil {
		return nil, err
	}
	id := f.id("headers-")
	f.headerPolicy[id] = in.ResponseHeadersPolicyConfig
	return &cloudfront.CreateResponseHeadersPolicyOutput{ResponseHeadersPolicy: &cftypes.ResponseHeadersPolicy{
		Id:                          aws.String(id),
		ResponseHeadersPolicyConfig: in.ResponseHeadersPolicyConfig,
	}}, nil
}

func (f *fakeCloudFront) DeleteResponseHeadersPolicy(_ context.Context, in *cloudfront.DeleteResponseHeadersPolicyInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteResponseHeadersPolicyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.Id)
	f.record("DeleteResponseHeadersPolicy " + id)
	if _, ok := f.headerPolicy[id]; !ok {
		return nil, &cftypes.NoSuchResponseHeadersPolicy{Message: aws.String("no response headers policy " + id)}
	}
	delete(f.headerPolicy, id)
	return &cloudfront.DeleteResponseHeadersPolicyOutput{}, nil
}

func (f *fakeCloudFront) ListOriginAccessControls(_ context.Context, _ *cloudfront.ListOriginAccessControlsInput, _ ...func(*cloudfront.Options)) (*cloudfront.ListOriginAccessControlsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListOriginAccessControls")
	items := make([]cftypes.OriginAccessControlSummary, 0, len(f.accessControl))
	for _, id := range slices.Sorted(maps.Keys(f.accessControl)) {
		items = append(items, cftypes.OriginAccessControlSummary{
			Id:   aws.String(id),
			Name: aws.String(f.accessControl[id]),
		})
	}
	return &cloudfront.ListOriginAccessControlsOutput{
		OriginAccessControlList: &cftypes.OriginAccessControlList{Items: items},
	}, nil
}

func (f *fakeCloudFront) CreateOriginAccessControl(_ context.Context, in *cloudfront.CreateOriginAccessControlInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateOriginAccessControlOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.OriginAccessControlConfig.Name)
	f.record("CreateOriginAccessControl " + name)
	id := f.id("oac-")
	f.accessControl[id] = name
	return &cloudfront.CreateOriginAccessControlOutput{OriginAccessControl: &cftypes.OriginAccessControl{
		Id:                        aws.String(id),
		OriginAccessControlConfig: in.OriginAccessControlConfig,
	}}, nil
}

func (f *fakeCloudFront) DeleteOriginAccessControl(_ context.Context, in *cloudfront.DeleteOriginAccessControlInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteOriginAccessControlOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.Id)
	f.record("DeleteOriginAccessControl " + id)
	if _, ok := f.accessControl[id]; !ok {
		return nil, &cftypes.NoSuchOriginAccessControl{Message: aws.String("no origin access control " + id)}
	}
	delete(f.accessControl, id)
	return &cloudfront.DeleteOriginAccessControlOutput{}, nil
}

func (f *fakeCloudFront) ListDistributions(_ context.Context, _ *cloudfront.ListDistributionsInput, _ ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListDistributions")
	items := make([]cftypes.DistributionSummary, 0, len(f.distributions))
	for _, id := range slices.Sorted(maps.Keys(f.distributions)) {
		held := f.distributions[id]
		items = append(items, cftypes.DistributionSummary{
			Id:         aws.String(held.id),
			DomainName: aws.String(held.domain),
			Comment:    held.config.Comment,
			Aliases:    held.config.Aliases,
			Enabled:    held.config.Enabled,
		})
	}
	return &cloudfront.ListDistributionsOutput{DistributionList: &cftypes.DistributionList{Items: items}}, nil
}

func (f *fakeCloudFront) CreateDistribution(_ context.Context, in *cloudfront.CreateDistributionInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateDistributionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	comment := aws.ToString(in.DistributionConfig.Comment)
	f.record("CreateDistribution " + comment)
	if f.createDistributionErr != nil {
		return nil, f.createDistributionErr
	}
	id := f.id("E")
	held := &fakeDistribution{
		id:      id,
		domain:  strings.ToLower(id) + ".cloudfront.net",
		etag:    f.id("dist-"),
		config:  in.DistributionConfig,
		rollout: f.rollout,
	}
	f.distributions[id] = held
	return &cloudfront.CreateDistributionOutput{
		ETag: aws.String(held.etag),
		Distribution: &cftypes.Distribution{
			Id:                 aws.String(held.id),
			DomainName:         aws.String(held.domain),
			Status:             aws.String(held.status()),
			DistributionConfig: in.DistributionConfig,
		},
	}, nil
}

func (f *fakeCloudFront) GetDistribution(_ context.Context, in *cloudfront.GetDistributionInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetDistributionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.Id)
	f.record("GetDistribution " + id)
	if f.statusThrottles > 0 {
		f.statusThrottles--
		return nil, throttlingError()
	}
	held, ok := f.distributions[id]
	if !ok {
		return nil, &cftypes.NoSuchDistribution{Message: aws.String("no distribution " + id)}
	}
	status := held.status()
	held.polls++
	return &cloudfront.GetDistributionOutput{
		ETag: aws.String(held.etag),
		Distribution: &cftypes.Distribution{
			Id:                 aws.String(held.id),
			DomainName:         aws.String(held.domain),
			Status:             aws.String(status),
			DistributionConfig: held.config,
		},
	}, nil
}

func (f *fakeCloudFront) GetDistributionConfig(_ context.Context, in *cloudfront.GetDistributionConfigInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetDistributionConfigOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.Id)
	f.record("GetDistributionConfig " + id)
	held, ok := f.distributions[id]
	if !ok {
		return nil, &cftypes.NoSuchDistribution{Message: aws.String("no distribution " + id)}
	}
	return &cloudfront.GetDistributionConfigOutput{
		ETag:               aws.String(held.etag),
		DistributionConfig: clonedConfig(held.config),
	}, nil
}

func clonedConfig(config *cftypes.DistributionConfig) *cftypes.DistributionConfig {
	if config == nil {
		return nil
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		panic(err)
	}
	var cloned cftypes.DistributionConfig
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		panic(err)
	}
	return &cloned
}

func (f *fakeCloudFront) UpdateDistribution(_ context.Context, in *cloudfront.UpdateDistributionInput, _ ...func(*cloudfront.Options)) (*cloudfront.UpdateDistributionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.Id)
	f.record("UpdateDistribution " + id)
	held, ok := f.distributions[id]
	if !ok {
		return nil, &cftypes.NoSuchDistribution{Message: aws.String("no distribution " + id)}
	}
	if aws.ToString(in.IfMatch) != held.etag {
		return nil, &cftypes.PreconditionFailed{Message: aws.String("stale etag for " + id)}
	}
	if f.aliasErr != nil {
		return nil, f.aliasErr
	}
	held.config = in.DistributionConfig
	held.etag = f.id("dist-")
	held.polls = 0
	held.rollout = f.rollout
	return &cloudfront.UpdateDistributionOutput{
		ETag:         aws.String(held.etag),
		Distribution: &cftypes.Distribution{Id: in.Id, DomainName: aws.String(held.domain)},
	}, nil
}

func (f *fakeCloudFront) DeleteDistribution(_ context.Context, in *cloudfront.DeleteDistributionInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteDistributionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.Id)
	f.record("DeleteDistribution " + id)
	held, ok := f.distributions[id]
	if !ok {
		return nil, &cftypes.NoSuchDistribution{Message: aws.String("no distribution " + id)}
	}
	if aws.ToString(in.IfMatch) != held.etag {
		return nil, &cftypes.PreconditionFailed{Message: aws.String("stale etag for " + id)}
	}
	if aws.ToBool(held.config.Enabled) {
		return nil, &cftypes.DistributionNotDisabled{Message: aws.String("distribution " + id + " still serves")}
	}
	delete(f.distributions, id)
	return &cloudfront.DeleteDistributionOutput{}, nil
}

type fakeKeyValueStore struct {
	mu    sync.Mutex
	trail *trail
	next  int

	items       map[string]map[string]string
	conflicts   int
	updateErr   error
	listErr     error
	listPage    int
	throttles   int
	describeErr error
	calls       []string
}

func newFakeKeyValueStore(shared *trail) *fakeKeyValueStore {
	return &fakeKeyValueStore{trail: shared, items: map[string]map[string]string{}}
}

func (f *fakeKeyValueStore) record(call string) {
	f.calls = append(f.calls, call)
	f.trail.record(call)
}

func (f *fakeKeyValueStore) etagFor(arn string) string {
	return arn + "#" + strconv.Itoa(f.next)
}

func (f *fakeKeyValueStore) held(arn string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.items[arn])
}

func (f *fakeKeyValueStore) count(verb string) int {
	n := 0
	for _, call := range f.calls {
		if call == verb {
			n++
		}
	}
	return n
}

func (f *fakeKeyValueStore) DescribeKeyValueStore(_ context.Context, in *cloudfrontkeyvaluestore.DescribeKeyValueStoreInput, _ ...func(*cloudfrontkeyvaluestore.Options)) (*cloudfrontkeyvaluestore.DescribeKeyValueStoreOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	arn := aws.ToString(in.KvsARN)
	f.record("kvs.DescribeKeyValueStore")
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	return &cloudfrontkeyvaluestore.DescribeKeyValueStoreOutput{
		ETag:   aws.String(f.etagFor(arn)),
		KvsARN: in.KvsARN,
	}, nil
}

func (f *fakeKeyValueStore) UpdateKeys(_ context.Context, in *cloudfrontkeyvaluestore.UpdateKeysInput, _ ...func(*cloudfrontkeyvaluestore.Options)) (*cloudfrontkeyvaluestore.UpdateKeysOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	arn := aws.ToString(in.KvsARN)
	f.record("kvs.UpdateKeys")
	if f.throttles > 0 {
		f.throttles--
		return nil, throttlingError()
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if aws.ToString(in.IfMatch) != f.etagFor(arn) {
		return nil, &kvstypes.ConflictException{Message: aws.String("stale version of " + arn)}
	}
	if f.conflicts > 0 {
		f.conflicts--
		f.next++
		return nil, &kvstypes.ConflictException{Message: aws.String("another writer moved " + arn)}
	}
	held, ok := f.items[arn]
	if !ok {
		held = map[string]string{}
		f.items[arn] = held
	}
	for _, put := range in.Puts {
		held[aws.ToString(put.Key)] = aws.ToString(put.Value)
	}
	for _, drop := range in.Deletes {
		delete(held, aws.ToString(drop.Key))
	}
	f.next++
	return &cloudfrontkeyvaluestore.UpdateKeysOutput{ETag: aws.String(f.etagFor(arn))}, nil
}

func (f *fakeKeyValueStore) GetKey(_ context.Context, in *cloudfrontkeyvaluestore.GetKeyInput, _ ...func(*cloudfrontkeyvaluestore.Options)) (*cloudfrontkeyvaluestore.GetKeyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	arn := aws.ToString(in.KvsARN)
	key := aws.ToString(in.Key)
	f.record("kvs.GetKey " + key)
	value, ok := f.items[arn][key]
	if !ok {
		return nil, &kvstypes.ResourceNotFoundException{Message: aws.String("no key " + key)}
	}
	return &cloudfrontkeyvaluestore.GetKeyOutput{Key: in.Key, Value: aws.String(value)}, nil
}

func (f *fakeKeyValueStore) ListKeys(_ context.Context, in *cloudfrontkeyvaluestore.ListKeysInput, _ ...func(*cloudfrontkeyvaluestore.Options)) (*cloudfrontkeyvaluestore.ListKeysOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	arn := aws.ToString(in.KvsARN)
	f.record("kvs.ListKeys")
	if f.listErr != nil {
		return nil, f.listErr
	}
	keys := slices.Sorted(maps.Keys(f.items[arn]))
	from := 0
	if token := aws.ToString(in.NextToken); token != "" {
		from, _ = strconv.Atoi(token)
	}
	size := int(aws.ToInt32(in.MaxResults))
	if f.listPage > 0 && f.listPage < size {
		size = f.listPage
	}
	to := min(from+size, len(keys))
	out := &cloudfrontkeyvaluestore.ListKeysOutput{}
	for _, key := range keys[from:to] {
		out.Items = append(out.Items, kvstypes.ListKeysResponseListItem{
			Key:   aws.String(key),
			Value: aws.String(f.items[arn][key]),
		})
	}
	if to < len(keys) {
		out.NextToken = aws.String(strconv.Itoa(to))
	}
	return out, nil
}

type throttle struct{ code string }

func (t throttle) Error() string { return t.code }

func (t throttle) ErrorCode() string { return t.code }

func (t throttle) ErrorMessage() string { return "slow down" }

func (t throttle) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

func throttlingError() error { return throttle{code: "ThrottlingException"} }

type fakeDynamo struct {
	mu       sync.Mutex
	trail    *trail
	items    map[string]map[string]ddbtypes.AttributeValue
	calls    []string
	pageSize int

	putErr error
}

func newFakeDynamo(shared *trail) *fakeDynamo {
	return &fakeDynamo{trail: shared, items: map[string]map[string]ddbtypes.AttributeValue{}, pageSize: 1}
}

func (f *fakeDynamo) record(call string) {
	f.calls = append(f.calls, call)
	f.trail.record(call)
}

func dynamoKey(item map[string]ddbtypes.AttributeValue) string {
	pk, _ := item["pk"].(*ddbtypes.AttributeValueMemberS)
	sk, _ := item["sk"].(*ddbtypes.AttributeValueMemberS)
	if pk == nil || sk == nil {
		return ""
	}
	return pk.Value + "\x00" + sk.Value
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &dynamodb.GetItemOutput{Item: f.items[dynamoKey(in.Key)]}, nil
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return nil, f.putErr
	}
	key := dynamoKey(in.Item)
	held, err := conditionHolds(in, f.items[key])
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("condition on " + key)}
	}
	f.record("PutItem " + key)
	f.items[key] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func conditionHolds(in *dynamodb.PutItemInput, present map[string]ddbtypes.AttributeValue) (bool, error) {
	condition := aws.ToString(in.ConditionExpression)
	switch condition {
	case "":
		return true, nil
	case "attribute_not_exists(#pk)":
		return len(present) == 0, nil
	case "#rev = :rev":
		want, ok := in.ExpressionAttributeValues[":rev"].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return false, fmt.Errorf("fake dynamo needs the revision the condition %q compares against", condition)
		}
		got, ok := present["rev"].(*ddbtypes.AttributeValueMemberS)
		return ok && got.Value == want.Value, nil
	default:
		return false, fmt.Errorf("fake dynamo does not speak the condition %q", condition)
	}
}

func (f *fakeDynamo) TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	return nil, fmt.Errorf("fake dynamo writes no transaction: nothing this suite drives writes a pair")
}

func (f *fakeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := dynamoKey(in.Key)
	held, present := f.items[key]
	if aws.ToString(in.ConditionExpression) != "" {
		want, ok := in.ExpressionAttributeValues[":rev"].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return nil, fmt.Errorf("fake dynamo needs the revision the condition %q compares against", aws.ToString(in.ConditionExpression))
		}
		got, matched := held["rev"].(*ddbtypes.AttributeValueMemberS)
		if !present {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("no " + key)}
		}
		if !matched || got.Value != want.Value {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("condition on " + key), Item: held}
		}
	}
	f.record("DeleteItem " + key)
	delete(f.items, key)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	condition := aws.ToString(in.KeyConditionExpression)
	pk, _ := in.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	if pk == nil {
		return nil, fmt.Errorf("fake dynamo needs a partition, got %q", condition)
	}
	prefix := ""
	switch condition {
	case "#pk = :pk":
	case "#pk = :pk AND begins_with(#sk, :prefix)":
		value, _ := in.ExpressionAttributeValues[":prefix"].(*ddbtypes.AttributeValueMemberS)
		if value == nil {
			return nil, fmt.Errorf("fake dynamo needs a prefix for %q", condition)
		}
		prefix = value.Value
	default:
		return nil, fmt.Errorf("fake dynamo does not speak the key condition %q", condition)
	}

	var items []map[string]ddbtypes.AttributeValue
	for _, key := range slices.Sorted(maps.Keys(f.items)) {
		partition, sort, _ := strings.Cut(key, "\x00")
		if partition != pk.Value || !strings.HasPrefix(sort, prefix) {
			continue
		}
		if after := dynamoKey(in.ExclusiveStartKey); after != "" && key <= after {
			continue
		}
		items = append(items, f.items[key])
		if len(items) == f.pageSize {
			return &dynamodb.QueryOutput{Items: items, LastEvaluatedKey: lastKey(f.items[key])}, nil
		}
	}
	return &dynamodb.QueryOutput{Items: items}, nil
}

func lastKey(item map[string]ddbtypes.AttributeValue) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{"pk": item["pk"], "sk": item["sk"]}
}

func ownState(t *testing.T, stack edge.EdgeStack) private {
	t.Helper()

	var own private
	if err := stack.State().Adapter.Into(&own); err != nil {
		t.Fatalf("read the state the edge keeps to itself: %v", err)
	}
	return own
}
