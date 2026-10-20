package component

import (
	"context"
	"slices"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	ProviderName = "aws"

	providerTokenPrefix     = ProviderName + ":"
	disableDefaultProviders = "pulumi:disable-default-providers"
)

type Program struct {
	Run     func(*sdk.Context) error
	Tags    map[string]string
	Secrets []string
}

func ProgramIn(region string) func(*sdk.Context, Program) error {
	return func(pctx *sdk.Context, program Program) error {
		if err := DeclareProvider(pctx, region, program.Tags); err != nil {
			return err
		}
		return program.Run(pctx)
	}
}

func DeclareProvider(pctx *sdk.Context, region string, tags map[string]string) error {
	args := &aws.ProviderArgs{Region: sdk.String(region)}
	if len(tags) > 0 {
		args.DefaultTags = &aws.ProviderDefaultTagsArgs{Tags: sdk.ToStringMap(tags)}
	}
	declared, err := aws.NewProvider(pctx, ProviderName, args)
	if err != nil {
		return err
	}
	if err := pctx.RegisterStackTransformation(ThroughProvider(declared)); err != nil {
		return err
	}
	return pctx.RegisterInvokeTransform(InvokedThroughProvider(declared))
}

func ThroughProvider(declared sdk.ProviderResource) sdk.ResourceTransformation {
	return func(args *sdk.ResourceTransformationArgs) *sdk.ResourceTransformationResult {
		if !strings.HasPrefix(args.Type, providerTokenPrefix) {
			return nil
		}
		named, err := sdk.NewResourceOptions(args.Opts...)
		if err != nil || named.Provider != nil {
			return nil
		}
		return &sdk.ResourceTransformationResult{Props: args.Props, Opts: append(slices.Clone(args.Opts), sdk.Provider(declared))}
	}
}

func InvokedThroughProvider(declared sdk.ProviderResource) sdk.InvokeTransform {
	return func(_ context.Context, args *sdk.InvokeTransformArgs) *sdk.InvokeTransformResult {
		if !strings.HasPrefix(args.Token, providerTokenPrefix) || args.Opts.Provider != nil {
			return nil
		}
		opts := args.Opts
		opts.Provider = declared
		return &sdk.InvokeTransformResult{Args: args.Args, Opts: opts}
	}
}

func DefaultProvidersDisabled(context.Context, Program) (auto.ConfigMap, error) {
	return auto.ConfigMap{disableDefaultProviders: auto.ConfigValue{Value: `["` + ProviderName + `"]`}}, nil
}
