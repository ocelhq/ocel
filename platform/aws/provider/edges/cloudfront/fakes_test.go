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

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	cloudfrontpart "github.com/ocelhq/ocel/platform/aws/provider/tier/cloudfront"
)

const (
	fakeStateTable        = "ocel-state"
	fakeBucketTag         = "k3x9q2"
	fakeEdgeBucket        = "ocel-prod-edge-" + fakeBucketTag
	fakePreviewEdgeBucket = "ocel-preview-edge-" + fakeBucketTag
	fakeRegion            = "eu-west-1"
	fakeSecret            = "3f7a9c1e5b2d84600f1a2b3c4d5e6f70"
	fakeEntryURL          = "https://abcdef0123456789.lambda-url.eu-west-1.on.aws/"
	fakeEntryHost         = "abcdef0123456789.lambda-url.eu-west-1.on.aws"
	fakeStorageCoordinate = "conformance/prod/web/r1a2b3c4d"
	fakeAssetOriginPath   = "/assets/conformance/prod/web/r1a2b3c4d"
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
	parts  *fakeParts
}

type fakeParts struct {
	mu      sync.Mutex
	absent  bool
	ensured []string
}

func (w *world) records() records.Store {
	return awsports.KeyValues{Dynamo: w.dynamo, Tables: awsports.Table(fakeStateTable)}
}

func fakePartResources(tier environment.Tier) []provider.PartResource {
	return []provider.PartResource{
		{Kind: cloudfrontpart.KindRoutesStore, ID: fakeRoutesARN(tier)},
		{Kind: cloudfrontpart.KindFunction, ID: fakeResolverARN(tier)},
		{Kind: cloudfrontpart.KindFunction, ID: fakeEmptyBodyARN(tier)},
		{Kind: cloudfrontpart.KindCachePolicy, ID: "cache-" + string(tier)},
		{Kind: cloudfrontpart.KindHeadersPolicy, ID: "headers-" + string(tier)},
		{Kind: cloudfrontpart.KindAssetAccess, ID: "oac-" + string(tier)},
	}
}

func (w *world) ensurePart(ctx context.Context, tier environment.Tier, part, addedBy, addedWith string, say func(string)) ([]provider.PartResource, error) {
	w.parts.mu.Lock()
	defer w.parts.mu.Unlock()
	w.parts.ensured = append(w.parts.ensured, part+" for "+addedBy+addedWith)
	if w.parts.absent {
		return nil, nil
	}
	if say != nil {
		say("added the " + part + " part")
	}
	resources := fakePartResources(tier)
	if err := stackrecords.MarkBootstrapPartReady(ctx, w.records(), tier, part, stackrecords.BootstrapPart{AddedBy: addedBy, AddedWith: addedWith, Resources: resources}); err != nil {
		return nil, err
	}
	return resources, nil
}

func (w *world) ledgerItems() int {
	n := 0
	for key := range w.dynamo.items {
		recorded := false
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			recorded = recorded || strings.HasPrefix(key, awsports.PartitionKey(records.Partition{Tier: tier, Root: stackrecords.RootBootstrap}))
		}
		if !recorded {
			n++
		}
	}
	return n
}

func (w *world) forgetPart(t *testing.T) {
	t.Helper()
	w.parts.mu.Lock()
	defer w.parts.mu.Unlock()
	w.parts.absent = true
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if err := stackrecords.ForgetBootstrapPart(context.Background(), w.records(), tier, cloudfrontpart.Part); err != nil {
			t.Fatal(err)
		}
	}
}

func (w *world) forgetDistributionRecords() {
	w.dynamo.mu.Lock()
	defer w.dynamo.mu.Unlock()
	for key := range w.dynamo.items {
		if strings.Contains(key, "\x00distribution") {
			delete(w.dynamo.items, key)
		}
	}
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
		parts:  &fakeParts{},
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

func (w *world) invalidationTargets(in records.Partition) []string {
	value, _ := w.dynamo.items[awsports.PartitionKey(in)+"\x00invalidation#"]["value"].(*ddbtypes.AttributeValueMemberS)
	if value == nil {
		return nil
	}
	var targets []string
	if err := json.Unmarshal([]byte(value.Value), &targets); err != nil {
		return nil
	}
	return targets
}

func (w *world) edge() *cloudFront {
	return &cloudFront{
		ns:         defaultNamespace,
		ensurePart: w.ensurePart,
		open:       func(context.Context) (Clients, error) { return w.clients(), nil },
		pacing: Rollout{
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 5,
			Every:    time.Second,
			Jitter:   func() float64 { return 0.5 },
		},
	}
}

func fakeBucketOutputs(tier environment.Tier) map[string]string {
	return map[string]string{
		"StateBucketName":    defaultNamespace.BootstrapBucketName(tier, naming.StorageState, fakeBucketTag),
		"ArtifactBucketName": defaultNamespace.BootstrapBucketName(tier, naming.StorageArtifacts, fakeBucketTag),
		"EdgeBucketName":     defaultNamespace.BootstrapBucketName(tier, naming.StorageEdge, fakeBucketTag),
		"CacheBucketName":    defaultNamespace.BootstrapBucketName(tier, naming.StorageCache, fakeBucketTag),
	}
}

type fakeCFN struct {
	absent bool
}

func (f *fakeCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	if f.absent {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	outputs := map[string]string{"StateTableName": fakeStateTable}
	switch aws.ToString(in.StackName) {
	case coreStackName:
		maps.Copy(outputs, fakeBucketOutputs(environment.TierProduction))
	case previewCoreStackName:
		maps.Copy(outputs, fakeBucketOutputs(environment.TierPreview))
	default:
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	out := make([]cfntypes.Output, 0, len(outputs))
	for _, key := range slices.Sorted(maps.Keys(outputs)) {
		out = append(out, cfntypes.Output{OutputKey: aws.String(key), OutputValue: aws.String(outputs[key])})
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{StackName: in.StackName, Outputs: out}}}, nil
}

type fakeSSM struct {
	absent bool
	secret account.OriginSecret
}

func newFakeSSM() *fakeSSM {
	return &fakeSSM{secret: account.OriginSecret{Current: fakeSecret, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
}

func (f *fakeSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	if f.absent {
		return nil, &ssmtypes.ParameterNotFound{Message: aws.String("no parameter " + aws.ToString(in.Name))}
	}
	raw, err := json.Marshal(f.secret)
	if err != nil {
		return nil, err
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{
		Name:  in.Name,
		Value: aws.String(string(raw)),
	}}, nil
}

type fakeStore struct {
	arn  string
	etag string
}

type fakeDistribution struct {
	id      string
	domain  string
	etag    string
	config  *cftypes.DistributionConfig
	tags    map[string]string
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
	distributions map[string]*fakeDistribution
	updates       []*cftypes.DistributionConfig

	createDistributionErr error
	aliasErr              error
	statusThrottles       int
	rollout               int
}

func newFakeCloudFront(shared *trail) *fakeCloudFront {
	f := &fakeCloudFront{
		trail:         shared,
		rollout:       1,
		stores:        map[string]*fakeStore{},
		distributions: map[string]*fakeDistribution{},
	}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		name := defaultNamespace.EdgeRoutesStoreName(tier)
		f.stores[name] = &fakeStore{arn: fakeRoutesARN(tier), etag: "kvs-1"}
	}
	return f
}

func fakeRoutesARN(tier environment.Tier) string {
	return "arn:aws:cloudfront::123456789012:key-value-store/" + defaultNamespace.EdgeRoutesStoreName(tier)
}

func fakeResolverARN(tier environment.Tier) string {
	return "arn:aws:cloudfront::123456789012:function/" + defaultNamespace.EdgeResolverName(tier)
}

func fakeEmptyBodyARN(tier environment.Tier) string {
	return "arn:aws:cloudfront::123456789012:function/" + defaultNamespace.EdgeEmptyBodyName(tier)
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

func (f *fakeCloudFront) DescribeKeyValueStore(_ context.Context, in *cloudfront.DescribeKeyValueStoreInput, _ ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	f.record("DescribeKeyValueStore " + name)
	store, ok := f.stores[name]
	if !ok {
		return nil, &cftypes.EntityNotFound{Message: aws.String("no key value store " + name)}
	}
	return &cloudfront.DescribeKeyValueStoreOutput{
		ETag:          aws.String(store.etag),
		KeyValueStore: &cftypes.KeyValueStore{ARN: aws.String(store.arn), Name: in.Name, Status: aws.String("READY")},
	}, nil
}

func (f *fakeCloudFront) ListDistributions(_ context.Context, _ *cloudfront.ListDistributionsInput, _ ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListDistributions")
	items := make([]cftypes.DistributionSummary, 0, len(f.distributions))
	for _, id := range slices.Sorted(maps.Keys(f.distributions)) {
		existing := f.distributions[id]
		items = append(items, cftypes.DistributionSummary{
			Id:         aws.String(existing.id),
			DomainName: aws.String(existing.domain),
			Comment:    existing.config.Comment,
			Aliases:    existing.config.Aliases,
			Enabled:    existing.config.Enabled,
		})
	}
	return &cloudfront.ListDistributionsOutput{DistributionList: &cftypes.DistributionList{Items: items}}, nil
}

func (f *fakeCloudFront) CreateDistributionWithTags(_ context.Context, in *cloudfront.CreateDistributionWithTagsInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateDistributionWithTagsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	config := in.DistributionConfigWithTags.DistributionConfig
	comment := aws.ToString(config.Comment)
	f.record("CreateDistribution " + comment)
	if f.createDistributionErr != nil {
		return nil, f.createDistributionErr
	}
	for _, existing := range f.distributions {
		if aws.ToString(existing.config.CallerReference) == aws.ToString(config.CallerReference) {
			return nil, &cftypes.DistributionAlreadyExists{Message: aws.String("a distribution with this caller reference exists")}
		}
	}
	id := f.id("E")
	created := &fakeDistribution{
		id:      id,
		domain:  strings.ToLower(id) + ".cloudfront.net",
		etag:    f.id("dist-"),
		config:  config,
		tags:    map[string]string{},
		rollout: f.rollout,
	}
	if tags := in.DistributionConfigWithTags.Tags; tags != nil {
		for _, tag := range tags.Items {
			created.tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}
	f.distributions[id] = created
	return &cloudfront.CreateDistributionWithTagsOutput{
		ETag: aws.String(created.etag),
		Distribution: &cftypes.Distribution{
			Id:                 aws.String(created.id),
			DomainName:         aws.String(created.domain),
			Status:             aws.String(created.status()),
			DistributionConfig: config,
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
	existing, ok := f.distributions[id]
	if !ok {
		return nil, &cftypes.NoSuchDistribution{Message: aws.String("no distribution " + id)}
	}
	status := existing.status()
	existing.polls++
	return &cloudfront.GetDistributionOutput{
		ETag: aws.String(existing.etag),
		Distribution: &cftypes.Distribution{
			Id:                 aws.String(existing.id),
			DomainName:         aws.String(existing.domain),
			Status:             aws.String(status),
			DistributionConfig: existing.config,
		},
	}, nil
}

func (f *fakeCloudFront) GetDistributionConfig(_ context.Context, in *cloudfront.GetDistributionConfigInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetDistributionConfigOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.Id)
	f.record("GetDistributionConfig " + id)
	existing, ok := f.distributions[id]
	if !ok {
		return nil, &cftypes.NoSuchDistribution{Message: aws.String("no distribution " + id)}
	}
	return &cloudfront.GetDistributionConfigOutput{
		ETag:               aws.String(existing.etag),
		DistributionConfig: clonedConfig(existing.config),
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
	existing, ok := f.distributions[id]
	if !ok {
		return nil, &cftypes.NoSuchDistribution{Message: aws.String("no distribution " + id)}
	}
	if aws.ToString(in.IfMatch) != existing.etag {
		return nil, &cftypes.PreconditionFailed{Message: aws.String("stale etag for " + id)}
	}
	if f.aliasErr != nil {
		return nil, f.aliasErr
	}
	f.updates = append(f.updates, in.DistributionConfig)
	existing.config = in.DistributionConfig
	existing.etag = f.id("dist-")
	existing.polls = 0
	existing.rollout = f.rollout
	return &cloudfront.UpdateDistributionOutput{
		ETag:         aws.String(existing.etag),
		Distribution: &cftypes.Distribution{Id: in.Id, DomainName: aws.String(existing.domain)},
	}, nil
}

func (f *fakeCloudFront) DeleteDistribution(_ context.Context, in *cloudfront.DeleteDistributionInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteDistributionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.Id)
	f.record("DeleteDistribution " + id)
	existing, ok := f.distributions[id]
	if !ok {
		return nil, &cftypes.NoSuchDistribution{Message: aws.String("no distribution " + id)}
	}
	if aws.ToString(in.IfMatch) != existing.etag {
		return nil, &cftypes.PreconditionFailed{Message: aws.String("stale etag for " + id)}
	}
	if aws.ToBool(existing.config.Enabled) {
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
	updateErrs  []error
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

func (f *fakeKeyValueStore) itemsOf(arn string) map[string]string {
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
	if len(f.updateErrs) > 0 {
		err := f.updateErrs[0]
		f.updateErrs = f.updateErrs[1:]
		return nil, err
	}
	if aws.ToString(in.IfMatch) != f.etagFor(arn) {
		return nil, &kvstypes.ConflictException{Message: aws.String("stale version of " + arn)}
	}
	if f.conflicts > 0 {
		f.conflicts--
		f.next++
		return nil, &kvstypes.ConflictException{Message: aws.String("another writer moved " + arn)}
	}
	entries, ok := f.items[arn]
	if !ok {
		entries = map[string]string{}
		f.items[arn] = entries
	}
	for _, put := range in.Puts {
		entries[aws.ToString(put.Key)] = aws.ToString(put.Value)
	}
	for _, drop := range in.Deletes {
		delete(entries, aws.ToString(drop.Key))
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

	putErr    error
	beforePut func(key string, items map[string]map[string]ddbtypes.AttributeValue)
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
	if f.beforePut != nil {
		f.beforePut(key, f.items)
	}
	satisfied, err := conditionHolds(in, f.items[key])
	if err != nil {
		return nil, err
	}
	if !satisfied {
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
	item, present := f.items[key]
	if aws.ToString(in.ConditionExpression) != "" {
		want, ok := in.ExpressionAttributeValues[":rev"].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return nil, fmt.Errorf("fake dynamo needs the revision the condition %q compares against", aws.ToString(in.ConditionExpression))
		}
		got, matched := item["rev"].(*ddbtypes.AttributeValueMemberS)
		if !present {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("no " + key)}
		}
		if !matched || got.Value != want.Value {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("condition on " + key), Item: item}
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
	if err := stack.State().Private.Into(&own); err != nil {
		t.Fatalf("read the state the edge keeps to itself: %v", err)
	}
	return own
}

func openRouter(shared edge.EdgeStack) fake.PromotingStack {
	s := shared.(*stack)
	c, err := s.clients(context.Background())
	if err != nil {
		panic(err)
	}
	return fake.PromotingStack{Stack: routerStack{s: s}, Ledger: s.openLedger(c)}
}

func removePointer(ctx context.Context, stack edge.EdgeStack, removal router.PointerRemoval, progress progress.Log) (router.PruneResult, error) {
	if err := openRouter(stack).RemovePointer(ctx, removal, progress); err != nil {
		return router.PruneResult{}, err
	}
	return openRouter(stack).Ledger.RemovePointer(ctx, removal.Pointer)
}
