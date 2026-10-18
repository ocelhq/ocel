package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	awsresources "github.com/ocelhq/ocel/platform/aws/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
)

type mockedEngine struct {
	outputs   auto.OutputMap
	mocks     sdk.MockResourceMonitor
	pending   *site.PendingSets
	mu        sync.Mutex
	ran       []string
	torndown  []string
	sequence  []string
	previewed []provider.Change
	upErr     func(stack string) error
	downErr   func(stack string) error
	state     *engineState
}

type pushingAssetSets struct {
	inner   sdk.MockResourceMonitor
	pending *site.PendingSets
}

func (p pushingAssetSets) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == site.AssetSetToken && p.pending != nil {
		if _, err := p.pending.Push(context.Background(), args.Inputs["stack"].StringValue(), args.Inputs["set"].StringValue()); err != nil {
			return "", nil, err
		}
	}
	return p.inner.NewResource(args)
}

func (p pushingAssetSets) Call(args sdk.MockCallArgs) (resource.PropertyMap, error) {
	return p.inner.Call(args)
}

func stacksWith(cfg Config, engine *mockedEngine) *Stacks {
	if engine == nil {
		return newStacks(fixed(cfg), &Realized{}, nil)
	}
	stacks := newStacks(fixed(cfg), &Realized{}, engine)
	engine.pending = stacks.pending
	return stacks
}

func (e *mockedEngine) stacks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.ran)
}

var _ pulumi.Engine = (*mockedEngine)(nil)

func (e *mockedEngine) Up(_ context.Context, setup pulumi.WorkspaceSpec, log progress.Log) (auto.OutputMap, error) {
	var monitor sdk.MockResourceMonitor = standInCloud{}
	if e.mocks != nil {
		monitor = e.mocks
	}
	declared := recordDeclared(pushingAssetSets{inner: monitor, pending: e.pending})
	if e.state != nil {
		if err := e.state.lock(setup.Stack); err != nil {
			return nil, err
		}
		defer e.state.unlock(setup.Stack)
		e.state.hold(setup.Stack)
	}
	if err := sdk.RunErr(setup.Program, sdk.WithMocks("shop", setup.Stack, declared)); err != nil {
		return nil, err
	}
	if e.upErr != nil {
		if err := e.upErr(setup.Stack); err != nil {
			return nil, err
		}
	}
	if e.state != nil {
		e.state.apply(setup.Stack, declared.declared, log)
	}
	e.mu.Lock()
	e.ran = append(e.ran, setup.Stack)
	e.sequence = append(e.sequence, "up "+setup.Stack)
	e.mu.Unlock()
	return e.outputs, nil
}

func (e *mockedEngine) Preview(_ context.Context, setup pulumi.WorkspaceSpec, op pulumi.Operation, _ progress.Log) ([]provider.Change, error) {
	if e.state != nil {
		return e.previewState(setup, op)
	}
	if op == pulumi.OperationDestroy {
		rows := make([]provider.Change, 0, len(e.previewed))
		for _, row := range e.previewed {
			row.Action = provider.ActionDelete
			rows = append(rows, row)
		}
		return rows, nil
	}
	watcher := &previewing{inner: standInCloud{}}
	if e.mocks != nil {
		watcher.inner = e.mocks
	}
	if err := sdk.RunErr(setup.Program, sdk.WithMocks("shop", setup.Stack, watcher)); err != nil {
		return nil, err
	}
	e.previewed = watcher.rows
	return watcher.rows, nil
}

func (e *mockedEngine) previewState(setup pulumi.WorkspaceSpec, op pulumi.Operation) ([]provider.Change, error) {
	if op == pulumi.OperationDestroy {
		return e.state.teardown(setup.Stack), nil
	}
	var monitor sdk.MockResourceMonitor = standInCloud{}
	if e.mocks != nil {
		monitor = e.mocks
	}
	declared := recordDeclared(monitor)
	if err := sdk.RunErr(setup.Program, sdk.WithMocks("shop", setup.Stack, declared)); err != nil {
		return nil, err
	}
	return e.state.diff(setup.Stack, declared.declared), nil
}

type previewing struct {
	inner sdk.MockResourceMonitor
	mu    sync.Mutex
	rows  []provider.Change
}

func (p *previewing) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	p.mu.Lock()
	p.rows = append(p.rows, provider.Change{
		Kind:   args.TypeToken,
		Name:   args.Name,
		Action: provider.ActionCreate,
	})
	p.mu.Unlock()
	return p.inner.NewResource(args)
}

func (p *previewing) Call(args sdk.MockCallArgs) (resource.PropertyMap, error) {
	return p.inner.Call(args)
}

func (e *mockedEngine) Destroy(_ context.Context, setup pulumi.WorkspaceSpec, _ progress.Log) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.downErr != nil {
		if err := e.downErr(setup.Stack); err != nil {
			return err
		}
	}
	e.torndown = append(e.torndown, setup.Stack)
	e.sequence = append(e.sequence, "destroy "+setup.Stack)
	if e.state != nil {
		e.state.forget(setup.Stack)
	}
	return nil
}

func (e *mockedEngine) operations() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.sequence)
}

func (e *mockedEngine) torn() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.torndown)
}

func (e *mockedEngine) Outputs(context.Context, pulumi.WorkspaceSpec) (auto.OutputMap, error) {
	return e.outputs, nil
}

func (*mockedEngine) Unlock(context.Context, pulumi.WorkspaceSpec) error { return nil }

type standInCloud struct{}

func (standInCloud) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	state := args.Inputs
	for key, value := range masterUserSecret(args) {
		state[key] = value
	}
	return args.Name + "-id", state, nil
}

func (standInCloud) Call(sdk.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func provisionedOutputs() auto.OutputMap {
	return auto.OutputMap{
		"c-postgres": auto.OutputValue{Value: map[string]any{
			component.OutputKeyHost:      "shop-prod.cluster.eu-west-1.rds.amazonaws.com",
			component.OutputKeyPort:      float64(5432),
			postgres.OutputKeyDatabase:   "shop",
			postgres.OutputKeyUsername:   "ocel",
			postgres.OutputKeyClusterARN: "arn:aws:rds:eu-west-1:111122223333:cluster:shop",
			postgres.OutputKeyResourceID: "cluster-SHOP",
		}},
		"c-bucket":   auto.OutputValue{Value: map[string]any{bucket.OutputKeyBucket: "shop-prod-uploads"}},
		"c-kv":       kvOutput("c-kv"),
		"c-realtime": realtimeOutput("c-realtime"),
	}
}

func conformingStacks(engine *mockedEngine) *Stacks {
	return stacksPlacingInto(engine, &fakeArtifactStore{})
}

func stacksPlacingInto(engine *mockedEngine, uploader *fakeArtifactStore) *Stacks {
	return stacksWith(conformingConfig(uploader), engine)
}

func conformingConfig(uploader *fakeArtifactStore) Config {
	params := newFakeParameters()
	params.values[conformanceKVToken("c-kv")] = "a-kv-token"
	keys := newFakeSigningKeys()
	keys.values[conformanceSigningKey("c-realtime")] = conformanceSeed
	return Config{
		Slug:           "conformance",
		Region:         "eu-west-1",
		BackendURL:     "s3://ocel-state/conformance",
		Passphrase:     "a-passphrase",
		PulumiProject:  "ocel-conformance",
		StateTable:     "ocel-state",
		StateTableARN:  "arn:aws:dynamodb:eu-west-1:111122223333:table/ocel-state",
		AppBoundaryARN: "arn:aws:iam::111122223333:policy/ocel-app-boundary",
		ArtifactBucket: conformanceArtifactBucket,
		Objects:        uploader,
		Parameters:     params,
		KVTokenRoot:    conformanceKVTokenRoot,
		SigningKeys:    keys,
		SigningKeyRoot: conformanceSigningKeyRoot,
	}
}

const conformanceArtifactBucket = "ocel-artifacts"

type shippedArtifacts struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newShippedArtifacts() *shippedArtifacts {
	return &shippedArtifacts{objects: map[string][]byte{}}
}

func (s *shippedArtifacts) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == bucketObjectType {
		blob, err := args.Inputs["source"].AssetValue().Bytes()
		if err != nil {
			return "", nil, err
		}
		s.write(args.Inputs["bucket"].StringValue()+"/"+args.Inputs["key"].StringValue(), blob)
	}
	return standInCloud{}.NewResource(args)
}

func (s *shippedArtifacts) Call(args sdk.MockCallArgs) (resource.PropertyMap, error) {
	return standInCloud{}.Call(args)
}

func (s *shippedArtifacts) write(at string, blob []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[at] = blob
}

func (s *shippedArtifacts) at(ref provider.ArtifactRef) (string, error) {
	if ref.Bucket != provider.StoreFunctions {
		return "", fmt.Errorf("this run keeps no %q store", ref.Bucket)
	}
	return conformanceArtifactBucket + "/" + ref.Key, nil
}

func (s *shippedArtifacts) Put(_ context.Context, ref provider.ArtifactRef, body io.Reader) error {
	at, err := s.at(ref)
	if err != nil {
		return err
	}
	blob, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.write(at, blob)
	return nil
}

func (s *shippedArtifacts) Has(_ context.Context, ref provider.ArtifactRef) (bool, error) {
	at, err := s.at(ref)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, present := s.objects[at]
	return present, nil
}

func (s *shippedArtifacts) Open(_ context.Context, ref provider.ArtifactRef) (io.ReadCloser, error) {
	at, err := s.at(ref)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, present := s.objects[at]
	if !present {
		return nil, fmt.Errorf("no artifact at %s", at)
	}
	return io.NopCloser(bytes.NewReader(slices.Clone(blob))), nil
}

func (s *shippedArtifacts) RemovePrefix(_ context.Context, _ environment.Tier, prefix string, _ progress.Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for at := range s.objects {
		if strings.HasPrefix(at, conformanceArtifactBucket+"/"+prefix) {
			delete(s.objects, at)
		}
	}
	return nil
}

func TestStacksRunTheConformancePortTier(t *testing.T) {
	store := newShippedArtifacts()
	engine := &mockedEngine{outputs: provisionedOutputs(), mocks: store, state: newEngineState()}
	conformance.RunStacks(t, provider.Facts{Bindings: awsresources.Types(), StoresArtifacts: true}, conformingStacks(engine), store, nil)
}

func TestProvisioningAnInfraStackRunsTheAWSProgramAndDecodesEveryBinding(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: provisionedOutputs()}
	result, err := conformingStacks(engine).Provision(context.Background(), provider.StackSpec{
		Ref:      provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")},
		Kind:     provider.StackInfra,
		Recorded: true,
		Resources: []provider.Resource{
			{Name: "c-postgres", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}},
			{Name: "c-bucket", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(engine.ran) != 1 {
		t.Fatalf("the engine ran %d stacks, want the one the plan named", len(engine.ran))
	}
	for _, binding := range result.Bindings {
		if err := provider.VerifyProperties(binding); err != nil {
			t.Errorf("Provision() returned a binding providerserver refuses to record: %v", err)
		}
	}
	if got := result.Bindings[0]; got.Properties[provider.PropertyPassword] != "" || got.PasswordSecret() != "" || !got.HasIAMAuthentication() {
		t.Errorf("the postgres binding carries a password of %d bytes, names %q as its secret and authenticates by IAM: %v, want no password, no secret and IAM authentication",
			len(got.Properties[provider.PropertyPassword]), got.PasswordSecret(), got.HasIAMAuthentication())
	}
}

type lambdaCodeRecorder struct {
	standInCloud
	mu   sync.Mutex
	code map[string]payloads.Placement
}

func (r *lambdaCodeRecorder) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == "aws:lambda/function:Function" {
		r.mu.Lock()
		if r.code == nil {
			r.code = map[string]payloads.Placement{}
		}
		r.code[args.Name] = payloads.Placement{
			Bucket: stringInput(args.Inputs, "s3Bucket"),
			Key:    stringInput(args.Inputs, "s3Key"),
		}
		r.mu.Unlock()
	}
	return r.standInCloud.NewResource(args)
}

func stringInput(inputs resource.PropertyMap, key string) string {
	value, present := inputs[resource.PropertyKey(key)]
	if !present || !value.IsString() {
		return ""
	}
	return value.StringValue()
}

func TestProvisioningABucketPlacesTheUploadCompleterItDeclares(t *testing.T) {
	t.Parallel()

	uploader := &fakeArtifactStore{}
	recorder := &lambdaCodeRecorder{}
	engine := &mockedEngine{outputs: provisionedOutputs(), mocks: recorder}
	if _, err := stacksPlacingInto(engine, uploader).Provision(context.Background(), provider.StackSpec{
		Ref:      provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")},
		Kind:     provider.StackInfra,
		Recorded: true,
		Resources: []provider.Resource{
			{Name: "c-bucket", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}},
		},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	at := payloads.At("ocel-artifacts", bucket.UploadCompleterKeyPrefix, payloads.UploadCompleter())
	if !slices.Contains(uploader.puts, at.Key) {
		t.Errorf("uploaded %v, want the upload completer placed at %s", uploader.puts, at.Key)
	}
	want := payloads.Placement{Bucket: at.Bucket, Key: at.Key}
	if placed := recorder.code["bucket-c-bucket-upload-completer"]; placed != want {
		t.Errorf("the upload completer lambda declares code at %+v, want %+v", placed, want)
	}
}
