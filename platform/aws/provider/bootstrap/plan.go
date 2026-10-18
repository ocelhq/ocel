package bootstrap

import (
	"context"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func WithDefaultStackNames(ns Namespace, described provider.BootstrapDescription) provider.BootstrapDescription {
	coreStack, err := ns.StackNameFor(described.Tier)
	if err != nil {
		return described
	}
	return bootstrapplan.WithDefaultStackNames(described, Parts(), func(name string) string {
		f, ok := partNamed(name)
		if !ok {
			return coreStack
		}
		return f.stackName(ns, described.Tier)
	})
}

const planFanOut = 4

func PlanChanges(ctx context.Context, stacks cfn.API, layers LayerAPI, read Reading, req Request, groups []provider.ChangeGroup) ([]provider.ChangeGroup, error) {
	target, err := bootstrapFor(read.ns, read.tier)
	if err != nil {
		return nil, err
	}
	refs, tier := read.refs, read.tier
	alongside := PartSet{}
	for _, name := range req.Parts {
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
		stack, ok, err := renderGroup(target, group.Part, partInputs{
			ns:           read.ns,
			tier:         tier,
			refs:         refs,
			alongside:    alongside,
			variablesKey: req.VariablesKey,
		})
		if err != nil {
			work.Wait()
			return nil, err
		}
		switch {
		case !ok:
			planned[i] = group
		case group.Action == provider.ActionCreate:
			group.Changes = templateChanges(stack.template, group.Action)
			planned[i] = group
		case group.Action == provider.ActionDelete:
			plan(func() { planned[i] = planDelete(ctx, stacks, group, stack.template) })
		case group.Action == provider.ActionUpdate:
			plan(func() { planned[i] = planUpdate(ctx, stacks, read.ns, read.tier, group, stack, req.Writer) })
		default:
			planned[i] = group
		}
	}
	work.Wait()
	return append(planned, planRuntimeLayers(ctx, layers, read)), nil
}

func PlanRemove(ctx context.Context, stacks cfn.API, layers LayerTeardownAPI, records RecordStore, read Reading) ([]provider.ChangeGroup, error) {
	if !read.Deployed.Present {
		return planRemoveLeftStacks(ctx, stacks, layers, records, read)
	}
	target, err := bootstrapFor(read.ns, read.tier)
	if err != nil {
		return nil, err
	}
	coreStack, err := read.ns.StackNameFor(read.tier)
	if err != nil {
		return nil, err
	}
	installed := read.Deployed.Parts.Names()
	order := installed
	alongside := PartSet{}
	for _, name := range installed {
		alongside[name] = true
	}

	groups := make([]provider.ChangeGroup, 0, len(order)+2)
	for _, part := range append(order, "") {
		if part == "" {
			if group, found := removeRuntimeLayers(ctx, layers, records(read.Deployed.StateTable), read.ns, read.tier); found {
				groups = append(groups, group)
			}
		}
		name := coreStack
		if part != "" {
			name = read.ns.PartStackName(part, read.tier)
		}
		group := provider.ChangeGroup{
			Kind:   provider.StackGroupKind,
			Name:   name,
			Part:   part,
			Action: provider.ActionDelete,
		}
		stack, ok, err := renderGroup(target, part, partInputs{
			ns:           read.ns,
			tier:         read.tier,
			refs:         read.refs,
			alongside:    alongside,
			variablesKey: broughtVariablesKey(read.Deployed.Outputs),
		})
		if err != nil {
			return nil, err
		}
		if ok {
			group = noteStranded(planDelete(ctx, stacks, group, stack.template))
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func planRemoveLeftStacks(ctx context.Context, stacks cfn.API, layers LayerTeardownAPI, records RecordStore, read Reading) ([]provider.ChangeGroup, error) {
	coreStack, err := read.ns.StackNameFor(read.tier)
	if err != nil {
		return nil, err
	}
	installed, err := installedParts(ctx, stacks, read.ns, read.tier)
	if err != nil {
		return nil, err
	}
	order := installed.Names()
	left := make([]provider.ChangeGroup, 0, len(order)+2)
	for _, part := range order {
		left = append(left, provider.ChangeGroup{Name: read.ns.PartStackName(part, read.tier), Part: part})
	}
	left = append(left, provider.ChangeGroup{Name: coreStack})

	var groups []provider.ChangeGroup
	for _, group := range left {
		if group.Name == coreStack {
			if runtime, found := removeRuntimeLayers(ctx, layers, records(read.Deployed.StateTable), read.ns, read.tier); found {
				groups = append(groups, runtime)
			}
		}
		stack, err := cfn.DescribeStack(ctx, stacks, group.Name)
		if err != nil {
			return nil, err
		}
		if stack == nil {
			continue
		}
		groups = append(groups, provider.ChangeGroup{
			Kind:   provider.StackGroupKind,
			Name:   group.Name,
			Part:   group.Part,
			Action: provider.ActionDelete,
			Reason: "left in " + string(stack.StackStatus) + " with no usable core stack",
		})
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
	"VariablesTable": {Reason: "every variable value this tier stores, and their history"},
	"VariablesKey":   {Reason: "the key those values are encrypted under"},
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

func renderGroup(target spec, part string, in partInputs) (partStack, bool, error) {
	if part == "" {
		core, err := target.core(coreVariablesKey(in.alongside, in.variablesKey))
		return core, true, err
	}
	f, ok := partNamed(part)
	if !ok {
		return partStack{}, false, nil
	}
	stack, err := f.template(in)
	return stack, true, err
}

func planUpdate(ctx context.Context, stacks cfn.API, ns Namespace, tier environment.Tier, group provider.ChangeGroup, stack partStack, writer provider.WrittenBy) provider.ChangeGroup {
	tags := stampTags(ns, tier, Stamp{Digest: cfn.TemplateDigest(stack.body), WrittenBy: writer.String()})
	id, changes, err := cfn.Plan(ctx, stacks, ns.ChangeSetNameFor, group.Name, stack.body, stack.params,
		[]cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}, tags)
	if err != nil {
		group.Reason = provider.WithDetailUnavailable(group.Reason)
		return group
	}
	if id == "" {
		group.Reason = "version stamp is behind; no resource changes"
		return group
	}
	cfn.DiscardChangeSet(ctx, stacks, group.Name, id)
	if group.Changes = resourceChanges(changes); len(group.Changes) == 0 {
		group.Reason = provider.WithDetailUnavailable(group.Reason)
	}
	return group
}

func planDelete(ctx context.Context, stacks cfn.API, group provider.ChangeGroup, template cfn.Template) provider.ChangeGroup {
	provisioned, err := stackResources(ctx, stacks, group.Name)
	if err != nil {
		group.Changes = templateChanges(template, provider.ActionDelete)
		group.Reason = provider.WithDetailUnavailable(group.Reason)
		return group
	}
	group.Changes = make([]provider.Change, 0, len(provisioned))
	for _, resource := range provisioned {
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

func templateChanges(template cfn.Template, action provider.ChangeAction) []provider.Change {
	changes := make([]provider.Change, 0, len(template.Resources))
	for _, resource := range template.Resources {
		changes = append(changes, provider.Change{Kind: resource.Type, Name: resource.ID, Action: action})
	}
	return changes
}

type templateResource struct {
	id   string
	kind string
}
