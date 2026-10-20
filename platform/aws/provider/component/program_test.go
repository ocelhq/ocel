package component_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	s3 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

const providerToken = "pulumi:providers:aws"

func TestEveryProgramDeclaresAProviderNamedAWSWithTheRegionAndTheStacksTags(t *testing.T) {
	t.Parallel()

	ran := false
	rec := componenttest.Record(t, func(pctx *sdk.Context) error {
		return component.ProgramIn("eu-west-1")(pctx, component.Program{
			Run:  func(*sdk.Context) error { ran = true; return nil },
			Tags: map[string]string{"ocel:project": "shop"},
		})
	})

	if !ran {
		t.Fatal("the program behind the provider never ran")
	}
	inputs := rec.InputsOf(t, providerToken, component.ProviderName)
	if got := inputs["region"]; !got.IsString() || got.StringValue() != "eu-west-1" {
		t.Errorf("provider region = %v, want eu-west-1", got)
	}
	tags := inputs["defaultTags"]
	if !tags.IsObject() || tags.ObjectValue()["tags"].ObjectValue()["ocel:project"].StringValue() != "shop" {
		t.Errorf("provider defaultTags = %v, want the stack's tags", tags)
	}
}

func TestAnAWSResourceCarryingAFileAssetIsRegisteredThroughTheDeclaredProvider(t *testing.T) {
	t.Parallel()

	const objectToken = "aws:s3/bucketObjectv2:BucketObjectv2"
	const key = "fn/shop/prod/web/r1/fn-web-index/aa1a3cdb8653b858.zip"
	zip := filepath.Join(t.TempDir(), "code.zip")
	if err := os.WriteFile(zip, []byte("PK\x05\x06"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := componenttest.Record(t, func(pctx *sdk.Context) error {
		return component.ProgramIn("eu-west-1")(pctx, component.Program{
			Run: func(pctx *sdk.Context) error {
				_, err := s3.NewBucketObjectv2(pctx, key, &s3.BucketObjectv2Args{
					Bucket: sdk.String("ocel-prod-artifacts-k3x9q2"),
					Key:    sdk.String(key),
					Source: sdk.NewFileAsset(zip),
				})
				return err
			},
		})
	})

	provider := rec.ProviderOf(objectToken, key)
	if !strings.Contains(provider, "::"+providerToken+"::"+component.ProviderName+"::") {
		t.Errorf("the object's provider = %q, want the declared %s attached in the program, not by an engine transform that drops the asset's hash", provider, component.ProviderName)
	}
	if source := rec.InputsOf(t, objectToken, key)["source"]; !source.IsAsset() || source.AssetValue().Path != zip {
		t.Errorf("the object's source = %v, want the file asset at %s", source, zip)
	}
}

func TestAnAWSResourceWithNoProviderIsHandedTheDeclaredOne(t *testing.T) {
	t.Parallel()

	declared := &aws.Provider{}
	through := component.ThroughProvider(declared)

	result := through(&sdk.ResourceTransformationArgs{Type: "aws:sqs/queue:Queue", Name: "q"})
	if result == nil {
		t.Fatal("transform of an aws resource = nil, want it handed the declared provider")
	}
	if handed, err := sdk.NewResourceOptions(result.Opts...); err != nil || handed.Provider != declared {
		t.Errorf("transform of an aws resource names provider %v (%v), want the declared one", handed, err)
	}
	if result := through(&sdk.ResourceTransformationArgs{Type: component.RandomTagToken, Name: "t"}); result != nil {
		t.Errorf("transform of an ocel resource = %+v, want it left to its own provider", result)
	}
	chosen := &aws.Provider{}
	if result := through(&sdk.ResourceTransformationArgs{Type: "aws:sqs/queue:Queue", Name: "q", Opts: []sdk.ResourceOption{sdk.Provider(chosen)}}); result != nil {
		t.Errorf("transform of a resource that names its provider = %+v, want that provider kept", result)
	}
	invoked := component.InvokedThroughProvider(declared)(t.Context(), &sdk.InvokeTransformArgs{Token: "aws:ec2/getSubnets:getSubnets"})
	if invoked == nil || invoked.Opts.Provider != declared {
		t.Errorf("transform of an aws invoke = %+v, want it handed the declared provider", invoked)
	}
}

func TestAStackRunsWithTheDefaultAWSProviderDisabled(t *testing.T) {
	t.Parallel()

	config, err := component.DefaultProvidersDisabled(t.Context(), component.Program{})
	if err != nil {
		t.Fatal(err)
	}
	if got := config["pulumi:disable-default-providers"].Value; got != `["aws"]` {
		t.Errorf("disable-default-providers = %q, want [\"aws\"]: a resource the transform missed fails rather than reaching the default provider", got)
	}
}
