package control

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

const baseSummary = "the permissions, buckets, tables and secrets every deploy in this tier runs on"

func (b Bootstrap) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	read, err := b.read(ctx, described.Tier)
	if err != nil {
		return nil, err
	}
	if !read.Deployed.Present {
		return nil, nil
	}
	var options []account.InventoryOption
	if b.VariablesKey != "" {
		options = append(options, account.WithVariablesKey(b.VariablesKey))
	}
	inventories, err := account.PartInventories(b.Namespace, described.Tier, read.Deployed.Parts.Names(), options...)
	if err != nil {
		return nil, err
	}
	summaries := map[string]string{provider.BasePart: baseSummary}
	for _, part := range account.Parts() {
		summaries[part.Name] = part.Summary
	}

	var parts []provider.BootstrapPart
	stacks := map[string]bool{}
	for _, stack := range read.Deployed.Stacks {
		stacks[stack.Name] = true
		if !stack.Present {
			continue
		}
		name := stack.Part
		if name == "" {
			name = provider.BasePart
		}
		parts = append(parts, provider.BootstrapPart{
			Name:      name,
			Summary:   summaries[name],
			Current:   stack.Current(),
			Digest:    stack.Digest,
			Resources: b.stackResources(ctx, stack.Name),
			Inventory: &provider.PartInventory{Vendor: string(groupVendor), Region: b.Region, Resources: inventories[name]},
		})
	}
	for _, stack := range described.Stacks {
		if !stacks[stack.Name] {
			parts = append(parts, provider.BootstrapPart{Name: stack.Name, Current: stack.DigestCurrent && stack.ReadError == ""})
		}
	}
	return parts, nil
}

func (b Bootstrap) stackResources(ctx context.Context, stackName string) []provider.PhysicalResource {
	var out []provider.PhysicalResource
	var token *string
	for {
		page, err := b.CFN.ListStackResources(ctx, &cloudformation.ListStackResourcesInput{
			StackName: aws.String(stackName),
			NextToken: token,
		})
		if err != nil {
			return nil
		}
		for _, summary := range page.StackResourceSummaries {
			id := aws.ToString(summary.PhysicalResourceId)
			if id == "" {
				id = aws.ToString(summary.LogicalResourceId)
			}
			out = append(out, provider.PhysicalResource{Kind: aws.ToString(summary.ResourceType), ID: id})
		}
		if token = page.NextToken; token == nil {
			return out
		}
	}
}
