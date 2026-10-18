package tier

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

type Records struct {
	KeyValues records.Store
	WrittenBy provider.WrittenBy
	Region    string
	Reads     *Reads
}

type Reads struct {
	mu     sync.Mutex
	values map[readKey]read
}

type readKey struct {
	tier environment.Tier
	name string
}

type read struct {
	record  stackrecords.BootstrapPart
	present bool
}

func (r *Reads) Forget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = nil
}

func (r *Reads) remember(key readKey, value read) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[readKey]read{}
	}
	r.values[key] = value
}

type Work struct {
	Digest string
	Make   func(ctx context.Context, prior []provider.PartResource, updatesTrust bool) ([]provider.PartResource, error)
}

type RecordedSpec struct {
	Name     string
	Summary  string
	Shape    func(tier environment.Tier) []pricing.Shaped
	Work     func(ctx context.Context, tier environment.Tier) (Work, error)
	Remove   func(ctx context.Context, tier environment.Tier, recorded []provider.PartResource) error
	IsUsedBy func(uses []buildoutput.Uses, front edge.Kind) bool
	Wildcard edge.Kind
}

func Recorded(r Records, spec RecordedSpec) Part {
	part := Part{
		Name:    spec.Name,
		Summary: spec.Summary,
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			return r.describe(ctx, tier, spec)
		},
		Removal: r.Removal(spec.Name, spec.Remove),
		Teardown: &Removal{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				return r.planDeletion(ctx, tier, spec.Name, "the bootstrap it belongs to is being removed")
			},
			Remove: func(ctx context.Context, tier environment.Tier, progress progress.Log) error {
				recorded, present, err := stackrecords.ReadBootstrapPart(ctx, r.KeyValues, tier, spec.Name)
				if err != nil || !present {
					return err
				}
				if progress != nil {
					progress.Say(fmt.Sprintf("Removing the %s part the %s tier's deploys added", spec.Name, tier))
				}
				return spec.Remove(ctx, tier, recorded.Resources)
			},
		},
		Repair: r.Repair(spec.Name, func(ctx context.Context, tier environment.Tier) (Work, bool, error) {
			work, err := spec.Work(ctx, tier)
			return work, err == nil, err
		}),
		Added: &Added{
			Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				work, err := spec.Work(ctx, tier)
				if err != nil {
					return nil, err
				}
				return r.plan(ctx, tier, spec, work)
			},
			Ensure: func(ctx context.Context, tier environment.Tier, addedBy, addedWith string, say func(string)) ([]provider.PartResource, error) {
				work, err := spec.Work(ctx, tier)
				if err != nil {
					return nil, err
				}
				return r.ensure(ctx, tier, spec, work, addedBy, addedWith, say)
			},
		},
	}
	if spec.IsUsedBy != nil {
		part.Use = &Use{IsUsedBy: spec.IsUsedBy, Shape: spec.Shape}
	}
	return part
}

func (r Records) Read(ctx context.Context, tier environment.Tier, name string) (stackrecords.BootstrapPart, bool, error) {
	key := readKey{tier: tier, name: name}
	r.Reads.mu.Lock()
	defer r.Reads.mu.Unlock()
	if cached, filled := r.Reads.values[key]; filled {
		return cached.record, cached.present, nil
	}
	record, present, err := stackrecords.ReadBootstrapPart(ctx, r.KeyValues, tier, name)
	if err != nil {
		return stackrecords.BootstrapPart{}, false, err
	}
	if r.Reads.values == nil {
		r.Reads.values = map[readKey]read{}
	}
	r.Reads.values[key] = read{record: record, present: present}
	return record, present, nil
}

func (r Records) Shape(resources []pricing.Shaped) *provider.PartShape {
	return &provider.PartShape{Vendor: awsports.Vendor, Region: r.Region, Resources: resources}
}

func (r Records) Removal(name string, remove func(ctx context.Context, tier environment.Tier, recorded []provider.PartResource) error) *Removal {
	return &Removal{
		Plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
			return r.planDeletion(ctx, tier, name, "this run removes it; the next deploy that uses it adds it again")
		},
		Remove: func(ctx context.Context, tier environment.Tier, _ progress.Log) error {
			recorded, present, err := stackrecords.ReadBootstrapPart(ctx, r.KeyValues, tier, name)
			if err != nil || !present {
				return err
			}
			return remove(ctx, tier, recorded.Resources)
		},
	}
}

func (r Records) Repair(name string, work func(ctx context.Context, tier environment.Tier) (Work, bool, error)) func(ctx context.Context, tier environment.Tier) (bool, error) {
	return func(ctx context.Context, tier environment.Tier) (bool, error) {
		recorded, present, err := stackrecords.ReadBootstrapPart(ctx, r.KeyValues, tier, name)
		if err != nil || !present {
			return false, err
		}
		made, makes, err := work(ctx, tier)
		if err != nil || !makes {
			return false, err
		}
		prior := recorded.Resources
		repaired, err := stackrecords.RepairBootstrapParts(ctx, r.KeyValues, tier, []stackrecords.PartWork{{
			Name:      name,
			WrittenBy: string(r.WrittenBy),
			Digest:    made.Digest,
			AddedBy:   recorded.AddedBy,
			Make: func(ctx context.Context) ([]provider.PartResource, error) {
				return made.Make(ctx, prior, true)
			},
		}})
		return len(repaired) > 0, err
	}
}

func (r Records) ensure(ctx context.Context, tier environment.Tier, spec RecordedSpec, work Work, addedBy, addedWith string, say func(string)) ([]provider.PartResource, error) {
	recorded, present, err := r.Read(ctx, tier, spec.Name)
	if err != nil {
		return nil, err
	}
	writer := string(r.WrittenBy)
	if present && recorded.CurrentFor(writer, work.Digest) {
		return recorded.Resources, nil
	}
	prior := recorded.Resources
	err = stackrecords.EnsureBootstrapPart(ctx, r.KeyValues, tier, stackrecords.PartWork{
		Name:      spec.Name,
		WrittenBy: writer,
		Digest:    work.Digest,
		AddedBy:   addedBy,
		AddedWith: addedWith,
		Make: func(ctx context.Context) ([]provider.PartResource, error) {
			if say != nil {
				say(fmt.Sprintf("Adding the %s tier's %s bootstrap part: %s", tier, spec.Name, spec.Summary))
			}
			return work.Make(ctx, prior, false)
		},
	})
	if errors.Is(err, stackrecords.ErrPartBehind) && say != nil {
		say(fmt.Sprintf("%v: run `%s` to bring the %s tier's %s part forward", err, provider.BootstrapCommand(tier), tier, spec.Name))
	}
	if err != nil && !errors.Is(err, stackrecords.ErrPartBehind) {
		return nil, fmt.Errorf("add the %s tier's %s bootstrap part: %w", tier, spec.Name, err)
	}
	if recorded, present, err = stackrecords.ReadBootstrapPart(ctx, r.KeyValues, tier, spec.Name); err != nil {
		return nil, err
	}
	r.Reads.remember(readKey{tier: tier, name: spec.Name}, read{record: recorded, present: present})
	return recorded.Resources, nil
}

func (r Records) plan(ctx context.Context, tier environment.Tier, spec RecordedSpec, work Work) (*provider.ChangeGroup, error) {
	recorded, present, err := r.Read(ctx, tier, spec.Name)
	if err != nil {
		return nil, err
	}
	if present && recorded.CurrentFor(string(r.WrittenBy), work.Digest) {
		return nil, nil
	}
	group := &provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   "bootstrap part " + spec.Name,
		Action: provider.ActionCreate,
		Reason: "the first deploy in the " + string(tier) + " tier that needs it adds the " + spec.Name + " part: " + spec.Summary,
	}
	if present {
		group.Action, group.Reason = provider.ActionUpdate, "this build moves the "+string(tier)+" tier's "+spec.Name+" forward"
	}
	for _, shaped := range spec.Shape(tier) {
		group.Changes = append(group.Changes, provider.Change{Kind: shaped.Type, Name: shaped.Name, Action: group.Action})
	}
	return group, nil
}

func (r Records) describe(ctx context.Context, tier environment.Tier, spec RecordedSpec) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, r.KeyValues, tier, spec.Name)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	work, err := spec.Work(ctx, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	users, err := r.usersOf(ctx, tier, spec.Name, spec.Wildcard)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      spec.Name,
		Summary:   spec.Summary,
		Current:   recorded.CurrentFor(string(r.WrittenBy), work.Digest),
		Digest:    recorded.Digest,
		Resources: recorded.Resources,
		UsedBy:    users,
		Shape:     r.Shape(spec.Shape(tier)),
	}, true, nil
}

func (r Records) usersOf(ctx context.Context, tier environment.Tier, name string, wildcard edge.Kind) ([]string, error) {
	listed, err := stackrecords.ListPartUsers(ctx, r.KeyValues, tier)
	if err != nil {
		return nil, err
	}
	users := listed[name]
	if tier == environment.TierPreview && wildcard != edge.None {
		recorded, err := stackrecords.ReadWildcard(ctx, r.KeyValues)
		if err != nil {
			return nil, err
		}
		if recorded.Edge == wildcard && recorded.IsRecorded() {
			users = append(users, recorded.Hostname())
		}
	}
	if users == nil {
		users = []string{}
	}
	return users, nil
}

func (r Records) planDeletion(ctx context.Context, tier environment.Tier, name, why string) (*provider.ChangeGroup, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, r.KeyValues, tier, name)
	if err != nil || !present {
		return nil, err
	}
	return addedDeletion(name, why, recorded), nil
}

func addedDeletion(name, why string, recorded stackrecords.BootstrapPart) *provider.ChangeGroup {
	by := recorded.AddedBy
	switch {
	case recorded.AddedWith != "" && by != "":
		by = "`" + recorded.AddedWith + "` for " + by
	case recorded.AddedWith != "":
		by = "`" + recorded.AddedWith + "`"
	}
	return Deletion(name, "added by "+by+" on first use; "+why, recorded.Resources)
}
