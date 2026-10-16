package aws

import (
	"context"
	"encoding/json"
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
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/taginvalidator"
)

const partsUsedSegment = "parts"

type partWork struct {
	name    string
	summary string
	digest  string
	shape   []pricing.Shaped
	make    func(ctx context.Context, prior []provider.PartResource) ([]provider.PartResource, error)
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

func (p *Provider) ensurePart(ctx context.Context, tier environment.Tier, work partWork, addedBy string, say func(string)) ([]provider.PartResource, error) {
	recorded, present, err := p.readPart(ctx, tier, work.name)
	if err != nil {
		return nil, err
	}
	if present && recorded.CurrentFor(string(p.writtenBy), work.digest) {
		return recorded.Resources, nil
	}
	store := p.KeyValues()
	prior := recorded.Resources
	if err := stackrecords.EnsureBootstrapPart(ctx, store, tier, stackrecords.PartWork{
		Name:      work.name,
		WrittenBy: string(p.writtenBy),
		Digest:    work.digest,
		AddedBy:   addedBy,
		Make: func(ctx context.Context) ([]provider.PartResource, error) {
			if say != nil {
				say(fmt.Sprintf("Adding the %s tier's %s bootstrap part: %s", tier, work.name, work.summary))
			}
			return work.make(ctx, prior)
		},
	}); err != nil {
		return nil, fmt.Errorf("add the %s tier's %s bootstrap part: %w", tier, work.name, err)
	}
	if recorded, present, err = stackrecords.ReadBootstrapPart(ctx, store, tier, work.name); err != nil {
		return nil, err
	}
	p.parts.remember(tierPart{tier: tier, name: work.name}, partRead{record: recorded, present: present})
	return recorded.Resources, nil
}

func (p *Provider) ensurePartNamed(ctx context.Context, tier environment.Tier, name, addedBy string, say func(string)) ([]provider.PartResource, error) {
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
	return p.ensurePart(ctx, tier, work, addedBy, say)
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
		if names, err = p.partsBuiltFor([]builtApp{{name: spec.App.App, framework: spec.App.Framework}}); err != nil {
			return nil, err
		}
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

func (p *Provider) partsUsedBy(apps []provider.AppEntry, front edge.Kind) ([]string, error) {
	used, err := p.partsBuiltFor(servedApps(apps))
	if err != nil {
		return nil, err
	}
	if front == cloudfront.Kind {
		used = append(used, cloudfrontpart.Part)
		slices.Sort(used)
	}
	return used, nil
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
				return work.make(ctx, prior)
			},
		})
	}
	return works, nil
}

func (p *Provider) describeAddedParts(ctx context.Context, tier environment.Tier) ([]provider.BootstrapPart, error) {
	users, err := readPartUsers(ctx, p.KeyValues(), tier)
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

type builtApp struct {
	name      string
	framework string
}

func servedApps(apps []provider.AppEntry) []builtApp {
	var served []builtApp
	for _, app := range apps {
		if app.Compute() != provider.ComputeContainer {
			served = append(served, builtApp{name: app.App, framework: app.Manifest.GetFramework().GetName()})
		}
	}
	return served
}

func (p *Provider) partsBuiltFor(apps []builtApp) ([]string, error) {
	root := ""
	if p.projectDir != "" {
		var err error
		if root, err = buildoutput.Root(p.projectDir); err != nil {
			return nil, err
		}
	}
	return partsNeededBy(root, apps)
}

func partsNeededBy(root string, apps []builtApp) ([]string, error) {
	needed := map[string]bool{}
	for _, app := range apps {
		uses, err := usesOf(root, app)
		if err != nil {
			return nil, err
		}
		if uses.Revalidation {
			needed[taginvalidator.Part] = true
		}
		if uses.ImageOptimization {
			needed[imageoptimizer.Part] = true
		}
	}
	return slices.Sorted(maps.Keys(needed)), nil
}

func usesOf(root string, app builtApp) (buildoutput.Uses, error) {
	if root == "" {
		return buildoutput.PresumeUses(app.framework), nil
	}
	hosting, present, err := buildoutput.ReadHosting(root, app.name)
	if err != nil || !present {
		return buildoutput.PresumeUses(app.framework), err
	}
	_, revalidates := hosting.Needs[edge.NeedRevalidation]
	_, err = os.Stat(filepath.Join(buildoutput.AppRoot(root, app.name), naming.ImageConfigFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return buildoutput.Uses{}, fmt.Errorf("read the image config of %s: %w", app.name, err)
	}
	return buildoutput.Uses{Revalidation: revalidates, ImageOptimization: err == nil}, nil
}

type partsUsed struct {
	Parts []string `json:"parts,omitempty"`
}

func partsUsedKey(tier environment.Tier, project string) keyvalue.Key {
	return stackrecords.ProjectsPartition(tier).Key(project, partsUsedSegment)
}

func recordPartsUsed(ctx context.Context, store keyvalue.Store, tier environment.Tier, project string, parts []string) error {
	return keyvalue.Change(ctx, store, partsUsedKey(tier, project), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		var prior partsUsed
		if len(recorded.Value) > 0 {
			if err := json.Unmarshal(recorded.Value, &prior); err != nil {
				return nil, false, fmt.Errorf("read the parts %s uses: %w", project, err)
			}
		}
		if slices.Equal(prior.Parts, parts) && len(recorded.Value) > 0 {
			return nil, false, nil
		}
		value, err := json.Marshal(partsUsed{Parts: parts})
		return value, true, err
	})
}

func readPartUsers(ctx context.Context, store keyvalue.Store, tier environment.Tier) (map[string][]string, error) {
	entries, err := store.List(ctx, stackrecords.ProjectsPartition(tier))
	if err != nil {
		return nil, fmt.Errorf("read the projects of the %s tier: %w", tier, err)
	}
	live := map[string]bool{}
	used := map[string][]string{}
	for _, entry := range entries {
		switch path := entry.Key.Path; {
		case len(path) == 1:
			live[path[0]] = true
		case len(path) == 2 && path[1] == partsUsedSegment && len(entry.Value) > 0:
			var recorded partsUsed
			if err := json.Unmarshal(entry.Value, &recorded); err != nil {
				return nil, fmt.Errorf("read the parts %s uses: %w", path[0], err)
			}
			for _, part := range recorded.Parts {
				used[part] = append(used[part], path[0])
			}
		}
	}
	users := map[string][]string{}
	for part, projects := range used {
		for _, project := range projects {
			if live[project] {
				users[part] = append(users[part], project)
			}
		}
		slices.Sort(users[part])
	}
	return users, nil
}
