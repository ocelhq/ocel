package bootstrap

import (
	"context"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func NameStacks(described providerkit.Bootstrap) providerkit.Bootstrap {
	target, err := bootstrapFor(string(described.Class))
	if err != nil {
		return described
	}
	held := make(map[string]bool, len(described.Stacks))
	for _, stack := range described.Stacks {
		held[stack.Feature] = true
	}
	named := described
	named.Stacks = slices.Clone(described.Stacks)
	if !held[""] {
		named.Stacks = append(named.Stacks, providerkit.BootstrapStack{Name: target.stackName})
	}
	for _, f := range featureRegistry {
		if held[f.name] {
			continue
		}
		named.Stacks = append(named.Stacks, providerkit.BootstrapStack{
			Name:    f.stackName(string(described.Class)),
			Feature: f.name,
		})
	}
	return named
}

func PlanChanges(ctx context.Context, cfn CFNAPI, class string, req Request, groups []providerkit.ChangeGroup) ([]providerkit.ChangeGroup, error) {
	target, err := bootstrapFor(class)
	if err != nil {
		return nil, err
	}
	deployed, refs, err := readBootstrap(ctx, cfn, class)
	if err != nil {
		return nil, err
	}
	alongside := FeatureSet{}
	for _, name := range req.Features {
		alongside[name] = true
	}

	planned := make([]providerkit.ChangeGroup, 0, len(groups))
	for _, group := range groups {
		stack, ok := renderGroup(target, group.Feature, class, deployed.ArtifactBucket, refs, alongside)
		switch {
		case !ok:
			planned = append(planned, group)
		case group.Action == providerkit.ActionCreate || group.Action == providerkit.ActionDelete:
			group.Changes = templateChanges(stack.body, group.Action)
			planned = append(planned, group)
		case group.Action == providerkit.ActionUpdate:
			planned = append(planned, planUpdate(ctx, cfn, group, stack, req.Writer))
		default:
			planned = append(planned, group)
		}
	}
	return planned, nil
}

func renderGroup(target spec, feature, class, artifactBucket string, refs stackRefs, alongside FeatureSet) (featureStack, bool) {
	if feature == "" {
		return featureStack{body: target.template()}, true
	}
	f, ok := featureNamed(feature)
	if !ok {
		return featureStack{}, false
	}
	return f.render(class, artifactBucket, refs, alongside), true
}

func planUpdate(ctx context.Context, cfn CFNAPI, group providerkit.ChangeGroup, stack featureStack, writer providerkit.Writer) providerkit.ChangeGroup {
	tags := stampTags(Stamp{Schema: RequiredSchema, Digest: TemplateDigest(stack.body), WrittenBy: writer.String()})
	id, changes, err := planCFNStack(ctx, cfn, group.Name, stack.body, stack.params,
		[]cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}, tags)
	if err != nil {
		group.Reason = providerkit.WithoutDetail(group.Reason)
		return group
	}
	if id == "" {
		group.Action, group.Reason = providerkit.ActionKeep, "already current"
		return group
	}
	discardChangeSet(ctx, cfn, id)
	if group.Changes = resourceChanges(changes); len(group.Changes) == 0 {
		group.Reason = providerkit.WithoutDetail(group.Reason)
	}
	return group
}

func resourceChanges(changes []cfntypes.ResourceChange) []providerkit.Change {
	planned := make([]providerkit.Change, 0, len(changes))
	for _, change := range changes {
		planned = append(planned, providerkit.Change{
			Kind:   aws.ToString(change.ResourceType),
			Name:   aws.ToString(change.LogicalResourceId),
			Action: resourceAction(change),
			Reason: replacementReason(change),
		})
	}
	return planned
}

func resourceAction(change cfntypes.ResourceChange) providerkit.ChangeAction {
	switch change.Action {
	case cfntypes.ChangeActionAdd, cfntypes.ChangeActionImport:
		return providerkit.ActionCreate
	case cfntypes.ChangeActionRemove:
		return providerkit.ActionDelete
	default:
		if replaces(change.Replacement) {
			return providerkit.ActionReplace
		}
		return providerkit.ActionUpdate
	}
}

func replaces(replacement cfntypes.Replacement) bool {
	return replacement == cfntypes.ReplacementTrue || replacement == cfntypes.ReplacementConditional
}

func replacementReason(change cfntypes.ResourceChange) string {
	if !replaces(change.Replacement) || change.Action == cfntypes.ChangeActionAdd || change.Action == cfntypes.ChangeActionRemove {
		return ""
	}
	for _, detail := range change.Details {
		if detail.Target == nil || detail.Target.RequiresRecreation == cfntypes.RequiresRecreationNever {
			continue
		}
		if name := aws.ToString(detail.Target.Name); name != "" {
			return "changing " + name + " is not a change AWS makes in place"
		}
		if attribute := string(detail.Target.Attribute); attribute != "" {
			return "changing its " + strings.ToLower(attribute) + " is not a change AWS makes in place"
		}
	}
	if change.Replacement == cfntypes.ReplacementConditional {
		return "AWS may not be able to make this change in place"
	}
	return "this change is not one AWS makes in place"
}

func templateChanges(body string, action providerkit.ChangeAction) []providerkit.Change {
	resources := templateResources(body)
	changes := make([]providerkit.Change, 0, len(resources))
	for _, resource := range resources {
		changes = append(changes, providerkit.Change{Kind: resource.kind, Name: resource.id, Action: action})
	}
	return changes
}

type templateResource struct {
	id   string
	kind string
}

func templateResources(body string) []templateResource {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	resources := mappingValue(doc.Content[0], "Resources")
	if resources == nil {
		return nil
	}
	var out []templateResource
	for i := 0; i+1 < len(resources.Content); i += 2 {
		resource := templateResource{id: resources.Content[i].Value}
		if kind := mappingValue(resources.Content[i+1], "Type"); kind != nil {
			resource.kind = kind.Value
		}
		out = append(out, resource)
	}
	return out
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
