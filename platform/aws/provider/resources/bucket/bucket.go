package bucket

import (
	"context"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/credential"
)

const transformKind = "bucket"

func New(artifacts component.Artifacts) component.Component {
	return component.Component{
		Types: []provider.BindingType{provider.BindingBucket},
		Declare: func(ctx *pulumi.Context, at component.Stack, declared []provider.Resource) error {
			return declare(ctx, at, declared, artifacts)
		},
		Collect: func(_ context.Context, at component.Stack, declared provider.Resource, outputs auto.OutputMap) (*variablestorev1.PublishedBinding, error) {
			fields, err := component.RequireOutputFields(outputs, declared.Name)
			if err != nil {
				return nil, err
			}
			return collectBinding(declared.Name, NewSessions(at.Project, at.Env, at.StateTableARN), fields)
		},
		WidestGrants: func(at component.Stack, _ provider.Resource) []*variablestorev1.Grant {
			return Grants(strings.Repeat("b", maxS3BucketNameLen), NewSessions(at.Project, at.Env, at.StateTableARN))
		},
		Credential: credential.Bucket,
		Transform: func(at component.Stack, declared provider.Resource) (component.Transformable, error) {
			names, err := NamesFor(at, declared.Name)
			return component.Transformable{Kind: transformKind, Names: names.Surfaces()}, err
		},
		Inventory: inventory,
	}
}

func declare(ctx *pulumi.Context, at component.Stack, declared []provider.Resource, artifacts component.Artifacts) error {
	if len(declared) == 0 {
		return nil
	}
	completer, err := placeUploadCompleter(ctx.Context(), artifacts, at.Tier())
	if err != nil {
		return err
	}
	sessions := NewSessions(at.Project, at.Env, at.StateTableARN)
	for _, resource := range declared {
		args := translate(resource.Bucket)
		args.Tags = at.Transforms.TagsFor(transformKind, resource.Name)
		args.PatchedCORS = at.Transforms.Patches(transformKind, resource.Name, "cors")
		if err := register(ctx, at, resource.Name, args, sessions, completer); err != nil {
			return fmt.Errorf("declare %s: %w", resource.Name, err)
		}
	}
	return nil
}
