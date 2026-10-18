package realtime

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"slices"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type Keys struct {
	SigningKeys SigningKeysAPI
	Root        string
}

func New(artifacts component.Artifacts, keys Keys) component.Component {
	return component.Component{
		Types: []provider.BindingType{provider.BindingRealtime},
		Declare: func(ctx *pulumi.Context, at component.Stack, declared []provider.Resource) error {
			return declare(ctx, at, declared, artifacts, keys)
		},
		Collect: func(ctx context.Context, _ component.Stack, declared provider.Resource, outputs auto.OutputMap) (*variablestorev1.PublishedBinding, error) {
			fields, err := component.RequireOutputFields(outputs, declared.Name)
			if err != nil {
				return nil, err
			}
			return collectRealtimeBinding(ctx, keys.SigningKeys, declared.Name, fields)
		},
		WidestGrants: func(_ component.Stack, declared provider.Resource) []*variablestorev1.Grant {
			return realtimeGrants(widestEventAPIARN, declared.Declared)
		},
		Credential: credential,
		Sweep: func(ctx context.Context, at component.Stack, kept []provider.Resource) error {
			return sweep(ctx, at, kept, keys)
		},
		Shape: shape,
	}
}

func placeRealtimeAuthorizer(ctx context.Context, artifacts component.Artifacts, at component.Stack) (payloads.Placement, error) {
	return component.PlacePayload(ctx, artifacts, at.Tier(), AuthorizerKeyPrefix, "realtime authorizer", payloads.RealtimeAuthorizer())
}

func declare(ctx *pulumi.Context, at component.Stack, realtimes []provider.Resource, artifacts component.Artifacts, keys Keys) error {
	if len(realtimes) == 0 {
		return nil
	}
	authorizer, err := placeRealtimeAuthorizer(ctx.Context(), artifacts, at)
	if err != nil {
		return err
	}
	args := realtimeArgs{Authorizer: authorizer, BoundaryARN: at.AppBoundaryARN, RolePath: at.AppRolePath}
	for _, resource := range realtimes {
		secret := SigningKeySecret(keys.Root, at.Tier(), at.Project, at.Env, resource.Name)
		var seed []byte
		var err error
		if at.Previewing {
			seed, err = previewSigningKey(ctx.Context(), keys.SigningKeys, secret)
		} else {
			seed, err = ensureSigningKey(ctx.Context(), keys.SigningKeys, at.Tier(), secret)
		}
		if err != nil {
			return fmt.Errorf("declare %s: %w", resource.Name, err)
		}
		args.Namespaces = append(args.Namespaces, realtimeNamespace{
			LogicalName:      resource.Name,
			Namespace:        resource.Declared,
			VerifyKey:        ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey),
			SigningKeySecret: secret,
		})
	}
	return registerRealtime(ctx, at.Project, at.Env, args)
}

func sweep(ctx context.Context, at component.Stack, kept []provider.Resource, keys Keys) error {
	listed, err := listSigningKeys(ctx, keys.SigningKeys, signingKeyPath(keys.Root, at.Tier(), at.Project, at.Env))
	if err != nil {
		return err
	}
	var undeclared []string
	for _, name := range listed {
		declared := slices.ContainsFunc(kept, func(resource provider.Resource) bool {
			return SigningKeySecret(keys.Root, at.Tier(), at.Project, at.Env, resource.Name) == name
		})
		if !declared {
			undeclared = append(undeclared, name)
		}
	}
	return deleteSigningKeys(ctx, keys.SigningKeys, undeclared)
}
