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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type mockedEngine struct {
	outputs   auto.OutputMap
	mocks     sdk.MockResourceMonitor
	mu        sync.Mutex
	ran       []string
	previewed []providerkit.Change
}

func (e *mockedEngine) stacks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.ran)
}

var _ kitpulumi.Engine = (*mockedEngine)(nil)

func (e *mockedEngine) Up(_ context.Context, setup kitpulumi.Setup, _ providerkit.Reporter) (auto.OutputMap, error) {
	var monitor sdk.MockResourceMonitor = standInCloud{}
	if e.mocks != nil {
		monitor = e.mocks
	}
	if err := sdk.RunErr(setup.Program, sdk.WithMocks("shop", setup.Stack, monitor)); err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.ran = append(e.ran, setup.Stack)
	e.mu.Unlock()
	return e.outputs, nil
}

func (e *mockedEngine) Preview(_ context.Context, setup kitpulumi.Setup, op kitpulumi.Op, _ providerkit.Reporter) ([]providerkit.Change, error) {
	if op == kitpulumi.OpDestroy {
		rows := make([]providerkit.Change, 0, len(e.previewed))
		for _, row := range e.previewed {
			row.Action = providerkit.ActionDelete
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

type previewing struct {
	inner sdk.MockResourceMonitor
	mu    sync.Mutex
	rows  []providerkit.Change
}

func (p *previewing) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	p.mu.Lock()
	p.rows = append(p.rows, providerkit.Change{
		Kind:   args.TypeToken,
		Name:   args.Name,
		Action: providerkit.ActionCreate,
	})
	p.mu.Unlock()
	return p.inner.NewResource(args)
}

func (p *previewing) Call(args sdk.MockCallArgs) (resource.PropertyMap, error) {
	return p.inner.Call(args)
}

func (e *mockedEngine) Destroy(context.Context, kitpulumi.Setup, providerkit.Reporter) error {
	return nil
}

func (e *mockedEngine) Outputs(context.Context, kitpulumi.Setup) (auto.OutputMap, error) {
	return e.outputs, nil
}

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

type standInSecrets struct{}

func (standInSecrets) GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String(`{"password":"a-master-password"}`)}, nil
}

func provisionedOutputs() auto.OutputMap {
	return auto.OutputMap{
		"c-postgres": auto.OutputValue{Value: map[string]any{
			outputKeyHost:      "shop-prod.cluster.eu-west-1.rds.amazonaws.com",
			outputKeyPort:      float64(5432),
			outputKeyDatabase:  "shop",
			outputKeyUsername:  "ocel",
			outputKeySecretARN: "arn:aws:secretsmanager:eu-west-1:111122223333:secret:shop",
		}},
		"c-bucket": auto.OutputValue{Value: map[string]any{outputKeyBucket: "shop-prod-uploads"}},
	}
}

func conformingReleaser(engine kitpulumi.Engine) *Releaser {
	return releaserPlacingInto(engine, &fakeUploader{})
}

func releaserPlacingInto(engine kitpulumi.Engine, uploader *fakeUploader) *Releaser {
	cfg := Config{
		Slug:           "conformance",
		Region:         "eu-west-1",
		BackendURL:     "s3://ocel-state/conformance",
		Passphrase:     "a-passphrase",
		PulumiProject:  "ocel-conformance",
		Secrets:        standInSecrets{},
		StateTable:     "ocel-state",
		StateTableARN:  "arn:aws:dynamodb:eu-west-1:111122223333:table/ocel-state",
		AppBoundaryARN: "arn:aws:iam::111122223333:policy/ocel-app-boundary",
		ArtifactBucket: conformanceArtifactBucket,
		Uploader:       uploader,
	}
	return newReleaser(fixed(cfg), &Realized{}, engine)
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

func (s *shippedArtifacts) at(ref providerkit.ArtifactRef) (string, error) {
	if ref.Bucket != providerkit.StoreFunctions {
		return "", fmt.Errorf("this run keeps no %q store", ref.Bucket)
	}
	return conformanceArtifactBucket + "/" + ref.Key, nil
}

func (s *shippedArtifacts) Put(_ context.Context, ref providerkit.ArtifactRef, body io.Reader) error {
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

func (s *shippedArtifacts) Has(_ context.Context, ref providerkit.ArtifactRef) (bool, error) {
	at, err := s.at(ref)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, held := s.objects[at]
	return held, nil
}

func (s *shippedArtifacts) Open(_ context.Context, ref providerkit.ArtifactRef) (io.ReadCloser, error) {
	at, err := s.at(ref)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, held := s.objects[at]
	if !held {
		return nil, fmt.Errorf("no artifact at %s", at)
	}
	return io.NopCloser(bytes.NewReader(slices.Clone(blob))), nil
}

func (s *shippedArtifacts) RemovePrefix(_ context.Context, _ providerkit.Class, prefix string, _ providerkit.Reporter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for at := range s.objects {
		if strings.HasPrefix(at, conformanceArtifactBucket+"/"+prefix) {
			delete(s.objects, at)
		}
	}
	return nil
}

func TestReleaserRunsTheKitsPortTier(t *testing.T) {
	store := newShippedArtifacts()
	engine := &mockedEngine{outputs: provisionedOutputs(), mocks: store}
	conformance.RunReleaser(t, conformingReleaser(engine), store, Serves())
}

func TestProvisioningAnInfraStackRunsTheAWSProgramAndDecodesEveryLink(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: provisionedOutputs()}
	result, err := conformingReleaser(engine).Provision(context.Background(), providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: "conformance", Class: providerkit.ClassProduction, Name: naming.InfraStack("conformance")},
		Kind: providerkit.StackInfra,
		Resources: []providerkit.Resource{
			{Name: "c-postgres", Type: providerkit.LinkPostgres, Postgres: &providerkit.PostgresSpec{}},
			{Name: "c-bucket", Type: providerkit.LinkBucket, Bucket: &providerkit.BucketSpec{}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(engine.ran) != 1 {
		t.Fatalf("the engine ran %d stacks, want the one the plan named", len(engine.ran))
	}
	for _, link := range result.Links {
		if err := providerkit.VerifyProperties(link); err != nil {
			t.Errorf("Provision() returned a link the kit refuses to record: %v", err)
		}
	}
	if got := result.Links[0].Properties[providerkit.PropertyPassword]; got != "a-master-password" {
		t.Errorf("the postgres link carries password %q, want the one the managed secret holds", got)
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
	value, held := inputs[resource.PropertyKey(key)]
	if !held || !value.IsString() {
		return ""
	}
	return value.StringValue()
}

func TestProvisioningABucketPlacesTheUploadCompleterItDeclares(t *testing.T) {
	t.Parallel()

	uploader := &fakeUploader{}
	recorder := &lambdaCodeRecorder{}
	engine := &mockedEngine{outputs: provisionedOutputs(), mocks: recorder}
	if _, err := releaserPlacingInto(engine, uploader).Provision(context.Background(), providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: "conformance", Class: providerkit.ClassProduction, Name: naming.InfraStack("conformance")},
		Kind: providerkit.StackInfra,
		Resources: []providerkit.Resource{
			{Name: "c-bucket", Type: providerkit.LinkBucket, Bucket: &providerkit.BucketSpec{}},
		},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	at := payloads.At("ocel-artifacts", uploadCompleterKeyPrefix, payloads.UploadCompleter())
	if !slices.Contains(uploader.puts, at.Key) {
		t.Errorf("uploaded %v, want the upload completer placed at %s", uploader.puts, at.Key)
	}
	want := payloads.Placement{Bucket: at.Bucket, Key: at.Key}
	if placed := recorder.code["bucket-c-bucket-upload-completer"]; placed != want {
		t.Errorf("the upload completer lambda declares code at %+v, want %+v", placed, want)
	}
}
