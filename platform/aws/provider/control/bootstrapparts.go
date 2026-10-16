package control

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

const (
	baseSummary    = "the permissions, buckets, tables and secrets every deploy in this tier runs on"
	bastionSummary = "the task a port forward runs inside the tier's network"
	bastionPart    = "bastion"
)

func (b Bootstrap) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	read, passed := described.VendorState.(bootstrap.Reading)
	if !passed || read.Tier() != described.Tier {
		var err error
		if read, err = b.read(ctx, described.Tier); err != nil {
			return nil, err
		}
	}
	if !read.Deployed.Present {
		return nil, nil
	}
	var options []bootstrap.ShapeOption
	if b.VariablesKey != "" {
		options = append(options, bootstrap.WithVariablesKey(b.VariablesKey))
	}
	shapes, err := bootstrap.ShapeParts(b.Namespace, described.Tier, read.Deployed.Features.Names(), options...)
	if err != nil {
		return nil, err
	}
	summaries := map[string]string{provider.BasePart: baseSummary}
	for _, feature := range bootstrap.Catalogue() {
		summaries[feature.Name] = feature.Summary
	}

	var parts []provider.BootstrapPart
	stacks := map[string]bool{}
	for _, stack := range read.Deployed.Stacks {
		stacks[stack.Name] = true
		if !stack.Present {
			continue
		}
		name := stack.Feature
		if name == "" {
			name = provider.BasePart
		}
		resources, err := b.stackResources(ctx, stack.Name)
		if err != nil {
			return nil, err
		}
		parts = append(parts, provider.BootstrapPart{
			Name:      name,
			Summary:   summaries[name],
			Current:   stack.Current(),
			Resources: resources,
			Shape:     &provider.PartShape{Vendor: string(groupVendor), Region: b.Region, Resources: shapes[name]},
		})
	}
	resources, raised, err := bastion.Describe(ctx, b.Bastion, described.Tier)
	if err != nil {
		return nil, err
	}
	if raised {
		parts = append(parts, provider.BootstrapPart{
			Name:      bastionPart,
			Summary:   bastionSummary,
			Current:   true,
			Resources: resources,
			Shape: &provider.PartShape{Vendor: string(groupVendor), Region: b.Region, Resources: []pricing.Shaped{
				{Name: bastion.NameFor(described.Tier), Type: "aws_ecs_cluster", Properties: map[string]any{}},
			}},
		})
	}
	for _, stack := range described.Stacks {
		if !stacks[stack.Name] {
			parts = append(parts, provider.BootstrapPart{Name: stack.Name, Current: stack.DigestCurrent && stack.ReadError == ""})
		}
	}
	return parts, nil
}

func (b Bootstrap) stackResources(ctx context.Context, stackName string) ([]provider.PartResource, error) {
	var out []provider.PartResource
	var token *string
	for {
		page, err := b.CFN.ListStackResources(ctx, &cloudformation.ListStackResourcesInput{
			StackName: aws.String(stackName),
			NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list the resources of %s: %w", stackName, err)
		}
		for _, summary := range page.StackResourceSummaries {
			id := aws.ToString(summary.PhysicalResourceId)
			if id == "" {
				id = aws.ToString(summary.LogicalResourceId)
			}
			out = append(out, provider.PartResource{Kind: aws.ToString(summary.ResourceType), ID: id})
		}
		if token = page.NextToken; token == nil {
			return out, nil
		}
	}
}
