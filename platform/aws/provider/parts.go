package aws

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/cloudfrontpart"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/taginvalidator"
)

type partWork struct {
	name    string
	summary string
	digest  string
	shape   []pricing.Shaped
	make    func(ctx context.Context, prior []provider.PartResource, updatesTrust bool) ([]provider.PartResource, error)
}

type tierPart struct {
	tier environment.Tier
	name string
}

type partRead struct {
	record  stackrecords.BootstrapPart
	present bool
}

func (p *Provider) readPart(ctx context.Context, tier environment.Tier, name string) (stackrecords.BootstrapPart, bool, error) {
	read, err := p.parts.resolve(tierPart{tier: tier, name: name}, func() (partRead, error) {
		record, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, name)
		return partRead{record: record, present: present}, err
	})
	return read.record, read.present, err
}

func (p *Provider) ensurePart(ctx context.Context, tier environment.Tier, work partWork, addedBy, addedWith string, say func(string)) ([]provider.PartResource, error) {
	recorded, present, err := p.readPart(ctx, tier, work.name)
	if err != nil {
		return nil, err
	}
	if present && recorded.CurrentFor(string(p.writtenBy), work.digest) {
		return recorded.Resources, nil
	}
	store := p.KeyValues()
	prior := recorded.Resources
	err = stackrecords.EnsureBootstrapPart(ctx, store, tier, stackrecords.PartWork{
		Name:      work.name,
		WrittenBy: string(p.writtenBy),
		Digest:    work.digest,
		AddedBy:   addedBy,
		AddedWith: addedWith,
		Make: func(ctx context.Context) ([]provider.PartResource, error) {
			if say != nil {
				say(fmt.Sprintf("Adding the %s tier's %s bootstrap part: %s", tier, work.name, work.summary))
			}
			return work.make(ctx, prior, false)
		},
	})
	if errors.Is(err, stackrecords.ErrPartBehind) && say != nil {
		say(fmt.Sprintf("%v: run `%s` to bring the %s tier's %s part forward", err, provider.BootstrapCommand(tier), tier, work.name))
	}
	if err != nil && !errors.Is(err, stackrecords.ErrPartBehind) {
		return nil, fmt.Errorf("add the %s tier's %s bootstrap part: %w", tier, work.name, err)
	}
	if recorded, present, err = stackrecords.ReadBootstrapPart(ctx, store, tier, work.name); err != nil {
		return nil, err
	}
	p.parts.remember(tierPart{tier: tier, name: work.name}, partRead{record: recorded, present: present})
	return recorded.Resources, nil
}

func (p *Provider) ensurePartNamed(ctx context.Context, tier environment.Tier, name, addedBy, addedWith string, say func(string)) ([]provider.PartResource, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return nil, err
	}
	if !deployed.Present {
		return nil, refusal.Refuse(refusal.CodeNotReady, "the %s tier is not bootstrapped, so there is nowhere to record its %s part: run `%s`", tier, name, provider.BootstrapCommand(tier))
	}
	work, err := p.partWorkFor(ctx, tier, name)
	if err != nil {
		return nil, err
	}
	return p.ensurePart(ctx, tier, work, addedBy, addedWith, say)
}

func (p *Provider) partWorkFor(ctx context.Context, tier environment.Tier, name string) (partWork, error) {
	switch name {
	case imageoptimizer.Part:
		return p.imageOptimizerWork(ctx, tier)
	case taginvalidator.Part:
		return p.tagInvalidatorWork(ctx, tier)
	case cloudfrontpart.Part:
		return p.cloudFrontWork(tier), nil
	case notfoundapi.Part:
		return p.notFoundAPIWork(tier), nil
	}
	return partWork{}, fmt.Errorf("this provider adds no bootstrap part named %q", name)
}

func (p *Provider) planDeployParts(ctx context.Context, spec provider.StackSpec) ([]provider.ChangeGroup, error) {
	tier := spec.Ref.Tier
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil || !deployed.Present {
		return nil, err
	}
	var names []string
	if spec.App.Compute != provider.ComputeContainer {
		uses, err := usesOf(spec.App.OutputRoot, spec.App.App, spec.App.Framework)
		if err != nil {
			return nil, err
		}
		names = partsNeededBy([]buildoutput.Uses{uses})
	}
	if spec.Edge != nil && spec.Edge.Kind() == cloudfront.Kind {
		names = append(names, cloudfrontpart.Part)
	}
	var groups []provider.ChangeGroup
	for _, name := range names {
		work, err := p.partWorkFor(ctx, tier, name)
		if err != nil {
			return nil, err
		}
		group, err := p.planPart(ctx, tier, work, "the first deploy in the "+string(tier)+" tier that needs it adds the "+name+" part: "+work.summary)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	return groups, nil
}

func partsUsedBy(uses []buildoutput.Uses, front edge.Kind) []string {
	used := partsNeededBy(uses)
	if front == cloudfront.Kind {
		used = append(used, cloudfrontpart.Part)
		slices.Sort(used)
	}
	return used
}

func (p *Provider) partShape(name string, tier environment.Tier) []pricing.Shaped {
	switch name {
	case imageoptimizer.Part:
		return imageoptimizer.Shape(string(p.namespace), tier)
	case taginvalidator.Part:
		return taginvalidator.Shape(string(p.namespace), tier)
	case cloudfrontpart.Part:
		return cloudfrontpart.Shape(p.namespace, tier)
	case notfoundapi.Part:
		return notfoundapi.Shape(string(p.namespace), tier)
	}
	return nil
}

var addedParts = []string{imageoptimizer.Part, taginvalidator.Part, cloudfrontpart.Part, notfoundapi.Part}

func (p *Provider) addedPartWorks(ctx context.Context, tier environment.Tier) ([]stackrecords.PartWork, error) {
	var works []stackrecords.PartWork
	for _, name := range addedParts {
		recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, name)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		work, err := p.partWorkFor(ctx, tier, name)
		if err != nil {
			return nil, err
		}
		prior := recorded.Resources
		works = append(works, stackrecords.PartWork{
			Name:      work.name,
			WrittenBy: string(p.writtenBy),
			Digest:    work.digest,
			AddedBy:   recorded.AddedBy,
			Make: func(ctx context.Context) ([]provider.PartResource, error) {
				return work.make(ctx, prior, true)
			},
		})
	}
	return works, nil
}

func (p *Provider) describeAddedParts(ctx context.Context, tier environment.Tier) ([]provider.BootstrapPart, error) {
	users, err := addedPartUsers(ctx, p.KeyValues(), tier)
	if err != nil {
		return nil, err
	}
	var parts []provider.BootstrapPart
	for _, name := range addedParts {
		work, err := p.partWorkFor(ctx, tier, name)
		if err != nil {
			return nil, err
		}
		part, present, err := p.describePart(ctx, tier, work, users[name])
		if err != nil {
			return nil, err
		}
		if present {
			parts = append(parts, part)
		}
	}
	return parts, nil
}

func addedPartUsers(ctx context.Context, store keyvalue.Store, tier environment.Tier) (map[string][]string, error) {
	users, err := stackrecords.ListPartUsers(ctx, store, tier)
	if err != nil || tier != environment.TierPreview {
		return users, err
	}
	wildcard, err := stackrecords.ReadWildcard(ctx, store)
	if err != nil {
		return nil, err
	}
	if part, serves := wildcardParts[wildcard.Edge]; serves && wildcard.IsRecorded() {
		users[part] = append(users[part], wildcard.Hostname())
	}
	return users, nil
}

var wildcardParts = map[edge.Kind]string{cloudfront.Kind: cloudfrontpart.Part, apigateway.Kind: notfoundapi.Part}

func (p *Provider) describePart(ctx context.Context, tier environment.Tier, work partWork, users []string) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, work.name)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	if users == nil {
		users = []string{}
	}
	return provider.BootstrapPart{
		Name:      work.name,
		Summary:   work.summary,
		Current:   recorded.CurrentFor(string(p.writtenBy), work.digest),
		Digest:    recorded.Digest,
		Resources: recorded.Resources,
		UsedBy:    users,
		Shape:     &provider.PartShape{Vendor: string(Vendor), Region: p.aws.Region, Resources: work.shape},
	}, true, nil
}

func (p *Provider) planPart(ctx context.Context, tier environment.Tier, work partWork, reason string) (*provider.ChangeGroup, error) {
	recorded, present, err := p.readPart(ctx, tier, work.name)
	if err != nil {
		return nil, err
	}
	if present && recorded.CurrentFor(string(p.writtenBy), work.digest) {
		return nil, nil
	}
	group := &provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   "bootstrap part " + work.name,
		Action: provider.ActionCreate,
		Reason: reason,
	}
	if present {
		group.Action, group.Reason = provider.ActionUpdate, "this build moves the "+string(tier)+" tier's "+work.name+" forward"
	}
	for _, shaped := range work.shape {
		group.Changes = append(group.Changes, provider.Change{Kind: shaped.Type, Name: shaped.Name, Action: group.Action})
	}
	return group, nil
}

func partsNeededBy(uses []buildoutput.Uses) []string {
	needed := map[string]bool{}
	for _, each := range uses {
		if each.Revalidation {
			needed[taginvalidator.Part] = true
		}
		if each.ImageOptimization {
			needed[imageoptimizer.Part] = true
		}
	}
	return slices.Sorted(maps.Keys(needed))
}

func usesOf(root, app, framework string) (buildoutput.Uses, error) {
	if root == "" {
		return buildoutput.PresumeUses(framework), nil
	}
	hosting, present, err := buildoutput.ReadHosting(root, app)
	if err != nil || !present {
		return buildoutput.PresumeUses(framework), err
	}
	_, revalidates := hosting.Needs[edge.NeedRevalidation]
	_, err = os.Stat(filepath.Join(buildoutput.AppRoot(root, app), naming.ImageConfigFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return buildoutput.Uses{}, fmt.Errorf("read the image config of %s: %w", app, err)
	}
	return buildoutput.Uses{Revalidation: revalidates, ImageOptimization: err == nil}, nil
}
