package kv

import (
	"context"
	"fmt"
	"slices"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/credential"
)

const transformKind = "kv"

type Tokens struct {
	Parameters ParametersAPI
	Root       string
}

func New(tokens Tokens) component.Component {
	return component.Component{
		Types: []provider.BindingType{provider.BindingKV},
		InVPC: true,
		Declare: func(ctx *pulumi.Context, at component.Stack, declared []provider.Resource) error {
			for _, resource := range declared {
				if err := declare(ctx, at, resource, tokens); err != nil {
					return fmt.Errorf("declare %s: %w", resource.Name, err)
				}
			}
			return nil
		},
		Collect: func(ctx context.Context, _ component.Stack, declared provider.Resource, outputs auto.OutputMap) (*variablestorev1.PublishedBinding, error) {
			fields, err := component.RequireOutputFields(outputs, declared.Name)
			if err != nil {
				return nil, err
			}
			binding, err := collectBinding(ctx, tokens.Parameters, declared.Name, fields)
			if err != nil {
				return nil, err
			}
			return &variablestorev1.PublishedBinding{Binding: binding}, nil
		},
		Credential: credential.KV,
		Sweep: func(ctx context.Context, at component.Stack, kept []provider.Resource) error {
			return sweep(ctx, at, kept, tokens)
		},
		Transform: func(at component.Stack, declared provider.Resource) (component.Transformable, error) {
			args, err := translateKV(declared.Name, declared.KV)
			if err != nil {
				return component.Transformable{}, err
			}
			names, err := NamesFor(at, declared.Name, args.Family)
			return component.Transformable{Kind: transformKind, Names: names.Surfaces(), Check: func(name string, patches map[string]map[string]any) error {
				return checkKVNodeCount(name, patches["replicationGroup"])
			}}, err
		},
		Inventory: inventory,
	}
}

func declare(ctx *pulumi.Context, at component.Stack, resource provider.Resource, tokens Tokens) error {
	args, err := translateKV(resource.Name, resource.KV)
	if err != nil {
		return err
	}
	args.AuthTokenParameter = TokenParameter(tokens.Root, at.Tier(), at.Project, at.Env, resource.Name)
	if at.Previewing {
		args.AuthToken, err = previewKVToken(ctx.Context(), tokens.Parameters, args.AuthTokenParameter)
	} else {
		args.AuthToken, err = ensureKVToken(ctx.Context(), tokens.Parameters, args.AuthTokenParameter)
	}
	if err != nil {
		return err
	}
	args.Tags = at.Transforms.TagsFor(transformKind, resource.Name)
	return registerKV(ctx, at, resource.Name, args)
}

func sweep(ctx context.Context, at component.Stack, kept []provider.Resource, tokens Tokens) error {
	listed, err := listKVTokens(ctx, tokens.Parameters, kvTokenPath(tokens.Root, at.Tier(), at.Project, at.Env))
	if err != nil {
		return err
	}
	var undeclared []string
	for _, name := range listed {
		declared := slices.ContainsFunc(kept, func(resource provider.Resource) bool {
			return TokenParameter(tokens.Root, at.Tier(), at.Project, at.Env, resource.Name) == name
		})
		if !declared {
			undeclared = append(undeclared, name)
		}
	}
	return deleteKVTokens(ctx, tokens.Parameters, undeclared)
}
