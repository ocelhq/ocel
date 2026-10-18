package postgres

import (
	"context"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

const transformKind = "postgres"

func New(clusters ClustersAPI) component.Component {
	return component.Component{
		Types:   []provider.BindingType{provider.BindingPostgres},
		InVPC:   true,
		Declare: declare,
		Collect: func(_ context.Context, at component.Stack, declared provider.Resource, outputs auto.OutputMap) (*variablestorev1.PublishedBinding, error) {
			fields, err := component.RequireOutputFields(outputs, declared.Name)
			if err != nil {
				return nil, err
			}
			return collectBinding(at.Region, declared.Name, fields)
		},
		WidestGrants: func(component.Stack, provider.Resource) []*variablestorev1.Grant {
			return grants(widestDBUserARN)
		},
		Teardown: &component.Teardown{
			Plan: markProtectedClusters,
			Before: func(ctx context.Context, ref provider.StackRef, outputs func(context.Context) (auto.OutputMap, error), progress progress.Log) error {
				return disableDeletionProtection(ctx, clusters, ref, outputs, progress)
			},
		},
		Transform: func(project, env, name string) component.Transformable {
			return component.Transformable{Kind: transformKind, Names: resourceNames(project, env, name)}
		},
		Shape: shape,
	}
}

func declare(ctx *pulumi.Context, at component.Stack, declared []provider.Resource) error {
	for _, resource := range declared {
		args := translate(resource.Postgres, at.Tier())
		args.Tags = at.Transforms.TagsFor(transformKind, resource.Name)
		args.Protected = at.Tier() == environment.TierProduction
		if err := register(ctx, at.Project, at.Env, resource.Name, args, at.Network); err != nil {
			return fmt.Errorf("declare %s: %w", resource.Name, err)
		}
	}
	return nil
}
