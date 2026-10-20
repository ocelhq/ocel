package component_test

import (
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
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

func TestAnAWSResourceWithNoProviderIsHandedTheDeclaredOne(t *testing.T) {
	t.Parallel()

	declared := &aws.Provider{}
	through := component.ThroughProvider(declared)

	result := through(t.Context(), &sdk.ResourceTransformArgs{Type: "aws:sqs/queue:Queue", Name: "q"})
	if result == nil || result.Opts.Provider != declared {
		t.Errorf("transform of an aws resource = %+v, want it handed the declared provider", result)
	}
	if result := through(t.Context(), &sdk.ResourceTransformArgs{Type: component.RandomTagToken, Name: "t"}); result != nil {
		t.Errorf("transform of an ocel resource = %+v, want it left to its own provider", result)
	}
	chosen := &aws.Provider{}
	if result := through(t.Context(), &sdk.ResourceTransformArgs{Type: "aws:sqs/queue:Queue", Name: "q", Opts: sdk.ResourceOptions{Provider: chosen}}); result != nil {
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
