package bootstrap

import (
	"context"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func NameStacks(ns Namespace, described provider.BootstrapDescription) provider.BootstrapDescription {
	coreStack, err := ns.StackNameFor(string(described.Class))
	if err != nil {
		return described
	}
	return bootstrapplan.WithDefaultStackNames(described, Catalogue(), func(name string) string {
		f, ok := featureNamed(name)
		if !ok {
			return coreStack
		}
		return f.stackName(ns, string(described.Class))
	})
}

const planFanOut = 4

func PlanChanges(ctx context.Context, stacks cfn.API, read Reading, req Request, groups []provider.ChangeGroup) ([]provider.ChangeGroup, error) {
	target, err := bootstrapFor(read.ns, read.class)
	if err != nil {
		return nil, err
	}
	deployed, refs, class := read.Deployed, read.refs, read.class
	alongside := FeatureSet{}
	for _, name := range req.Features {
		alongside[name] = true
	}

	planned := make([]provider.ChangeGroup, len(groups))
	var work sync.WaitGroup
	inFlight := make(chan struct{}, planFanOut)
	plan := func(look func()) {
		work.Add(1)
		inFlight <- struct{}{}
		go func() {
			defer work.Done()
			defer func() { <-inFlight }()
			look()
		}()
	}
	for i, group := range groups {
		stack, ok := renderGroup(target, group.Feature, featureInputs{
			ns:             read.ns,
			class:          class,
			artifactBucket: deployed.ArtifactBucket,
			refs:           refs,
			alongside:      alongside,
			varsKey:        req.VarsKey,
		})
		switch {
		case !ok:
			planned[i] = group
		case group.Action == provider.ActionCreate:
			group.Changes = templateChanges(stack.body, group.Action)
			planned[i] = group
		case group.Action == provider.ActionDelete:
			plan(func() { planned[i] = planDelete(ctx, stacks, group, stack.body) })
		case group.Action == provider.ActionUpdate:
			plan(func() { planned[i] = planUpdate(ctx, stacks, read.ns, group, stack, req.Writer) })
		default:
			planned[i] = group
		}
	}
	work.Wait()
	return append(planned, planRuntimeLayers(ctx, stacks, read, req)), nil
}

func PlanRemove(ctx context.Context, stacks cfn.API, read Reading) ([]provider.ChangeGroup, error) {
	if !read.Deployed.Present {
		return nil, nil
	}
	target, err := bootstrapFor(read.ns, read.class)
	if err != nil {
		return nil, err
	}
	coreStack, err := read.ns.StackNameFor(read.class)
	if err != nil {
		return nil, err
	}
	standing := read.Deployed.Features.Names()
	order, err := FeatureDeleteOrder(standing)
	if err != nil {
		return nil, err
	}
	alongside := FeatureSet{}
	for _, name := range standing {
		alongside[name] = true
	}

	groups := make([]provider.ChangeGroup, 0, len(order)+2)
	for _, feature := range append(order, "") {
		if feature == "" {
			groups = append(groups, removeRuntimeLayers(ctx, stacks, read))
		}
		name := coreStack
		if feature != "" {
			name = read.ns.FeatureStackName(feature, read.class)
		}
		group := provider.ChangeGroup{
			Kind:    provider.StackGroupKind,
			Name:    name,
			Feature: feature,
			Action:  provider.ActionDelete,
		}
		stack, ok := renderGroup(target, feature, featureInputs{
			ns:             read.ns,
			class:          read.class,
			artifactBucket: read.Deployed.ArtifactBucket,
			refs:           read.refs,
			alongside:      alongside,
			varsKey:        broughtVarsKey(read.Deployed.Outputs),
		})
		if ok {
			group = noteStranded(planDelete(ctx, stacks, group, stack.body))
		}
		groups = append(groups, group)
	}
	return groups, nil
}

var stranded = map[string]provider.Change{
	"StateBucket": {
		Reason: "the Pulumi state of every stack this bootstrap deployed; nothing can describe or remove those resources afterwards",
		Slow:   true,
	},
	"ArtifactBucket": {Reason: "the function code staged for this bootstrap", Slow: true},
	"AssetBucket": {
		Reason: "every build's static assets, prerender fallbacks and edge fetch cache",
		Slow:   true,
	},
	"VarsTable": {Reason: "every variable value this class holds, and their history"},
	"VarsKey":   {Reason: "the key those values are encrypted under"},
}

func noteStranded(group provider.ChangeGroup) provider.ChangeGroup {
	for i, change := range group.Changes {
		note, ok := stranded[change.Name]
		if !ok {
			continue
		}
		group.Changes[i].Reason, group.Changes[i].Slow = note.Reason, note.Slow
	}
	return group
}

func renderGroup(target spec, feature string, in featureInputs) (featureStack, bool) {
	if feature == "" {
		return featureStack{body: target.core(coreVarsKey(in.alongside, in.varsKey))}, true
	}
	f, ok := featureNamed(feature)
	if !ok {
		return featureStack{}, false
	}
	return f.planned(in), true
}

func planUpdate(ctx context.Context, stacks cfn.API, ns Namespace, group provider.ChangeGroup, stack featureStack, writer provider.WrittenBy) provider.ChangeGroup {
	tags := stampTags(ns, Stamp{Schema: RequiredSchema, Digest: cfn.TemplateDigest(stack.body), WrittenBy: writer.String()})
	id, changes, err := cfn.Plan(ctx, stacks, ns.ChangeSetNameFor, group.Name, stack.body, stack.params,
		[]cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}, tags)
	if err != nil {
		group.Reason = provider.WithoutDetail(group.Reason)
		return group
	}
	if id == "" {
		group.Reason = "version stamp is behind; no resource changes"
		return group
	}
	cfn.DiscardChangeSet(ctx, stacks, id)
	if group.Changes = resourceChanges(changes); len(group.Changes) == 0 {
		group.Reason = provider.WithoutDetail(group.Reason)
	}
	return group
}

func planDelete(ctx context.Context, stacks cfn.API, group provider.ChangeGroup, body string) provider.ChangeGroup {
	standing, err := stackResources(ctx, stacks, group.Name)
	if err != nil {
		group.Changes = templateChanges(body, provider.ActionDelete)
		group.Reason = provider.WithoutDetail(group.Reason)
		return group
	}
	group.Changes = make([]provider.Change, 0, len(standing))
	for _, resource := range standing {
		group.Changes = append(group.Changes, provider.Change{
			Kind:   resource.kind,
			Name:   resource.id,
			Action: provider.ActionDelete,
		})
	}
	return group
}

func stackResources(ctx context.Context, stacks cfn.API, stackName string) ([]templateResource, error) {
	var out []templateResource
	var token *string
	for {
		page, err := stacks.ListStackResources(ctx, &cloudformation.ListStackResourcesInput{
			StackName: aws.String(stackName),
			NextToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, summary := range page.StackResourceSummaries {
			out = append(out, templateResource{
				id:   aws.ToString(summary.LogicalResourceId),
				kind: aws.ToString(summary.ResourceType),
			})
		}
		if token = page.NextToken; token == nil {
			return out, nil
		}
	}
}

func resourceChanges(changes []cfntypes.ResourceChange) []provider.Change {
	planned := make([]provider.Change, 0, len(changes))
	for _, change := range changes {
		planned = append(planned, provider.Change{
			Kind:   aws.ToString(change.ResourceType),
			Name:   aws.ToString(change.LogicalResourceId),
			Action: resourceAction(change),
			Reason: replacementReason(change),
		})
	}
	return planned
}

func resourceAction(change cfntypes.ResourceChange) provider.ChangeAction {
	switch change.Action {
	case cfntypes.ChangeActionAdd, cfntypes.ChangeActionImport:
		return provider.ActionCreate
	case cfntypes.ChangeActionRemove:
		return provider.ActionDelete
	default:
		if replaces(change.Replacement) {
			return provider.ActionReplace
		}
		return provider.ActionUpdate
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

func templateChanges(body string, action provider.ChangeAction) []provider.Change {
	resources := templateResources(body)
	changes := make([]provider.Change, 0, len(resources))
	for _, resource := range resources {
		changes = append(changes, provider.Change{Kind: resource.kind, Name: resource.id, Action: action})
	}
	return changes
}

type templateResource struct {
	id   string
	kind string
}

func templateResources(body string) []templateResource {
	resources := templateSection(body, "Resources")
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

func templateSection(body, name string) *yaml.Node {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	return mappingValue(doc.Content[0], name)
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
