package deploy

import (
	"context"
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
)

type mockedEngine struct {
	outputs auto.OutputMap
	ran     []string
}

var _ kitpulumi.Engine = (*mockedEngine)(nil)

func (e *mockedEngine) Up(_ context.Context, setup kitpulumi.Setup, _ providerkit.Reporter) (auto.OutputMap, error) {
	if err := sdk.RunErr(setup.Program, sdk.WithMocks("shop", setup.Stack, standInCloud{})); err != nil {
		return nil, err
	}
	e.ran = append(e.ran, setup.Stack)
	return e.outputs, nil
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
	}
	return newReleaser(fixed(cfg), &Realized{}, engine)
}

func TestReleaserRunsTheKitsPortTier(t *testing.T) {
	conformance.RunReleaser(t, conformingReleaser(&mockedEngine{outputs: provisionedOutputs()}), Serves())
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
