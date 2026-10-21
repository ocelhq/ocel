package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const (
	EdgeBucketPart  = "edge-bucket"
	CacheBucketPart = "cache-bucket"
	KeysPart        = "keys"
	RealtimePart    = "realtime"
	TopicsPart      = "topics"
)

type ResourcePart struct {
	Name    string
	Summary string
	APIs    []string
	Retires bool
	items   func(names names.Names, tier environment.Tier) []Resource
}

var EdgeBucket = ResourcePart{
	Name:    EdgeBucketPart,
	Summary: "the bucket this tier's edges and its load balancer serve from: static files, prerendered pages, route tables, edge bundles and image settings",
	Retires: true,
	items: func(names names.Names, tier environment.Tier) []Resource {
		return []Resource{{
			Kind: KindBucket, Name: names.TierBucketStem(tier, naming.StorageEdge), Class: naming.StorageEdge,
			Note: "what this tier's edges and its load balancer may serve: static files, prerendered pages, route tables, edge bundles and image settings",
		}}
	},
}

var CacheBucket = ResourcePart{
	Name:    CacheBucketPart,
	Summary: "the bucket an app's fetch and use-cache entries and its tag clock are kept in, which only the app's own code reads back",
	Retires: true,
	items: func(names names.Names, tier environment.Tier) []Resource {
		return []Resource{{
			Kind: KindBucket, Name: names.TierBucketStem(tier, naming.StorageCache), Class: naming.StorageCache,
			Note: fmt.Sprintf("what only an app's own code reads back: its fetch and use-cache entries, each deleted %d days after its last write, and its tag clock, which is kept", naming.CacheObjectLifetimeDays),
		}}
	},
}

var Keys = ResourcePart{
	Name:    KeysPart,
	Summary: "the key ring both tiers' keys hang on and this tier's key, which every value the tier seals that belongs to no project is sealed under",
	APIs:    []string{"cloudkms.googleapis.com"},
	items: func(names names.Names, tier environment.Tier) []Resource {
		return []Resource{
			{Kind: KindKeyRing, Name: names.KeyRing(), Note: "the ring both tiers' keys hang on, and each project's own keys"},
			{Kind: KindKey, Name: string(tier), Note: "the key every value this tier stores that belongs to no project is sealed under"},
		}
	},
}

var Realtime = ResourcePart{
	Name:    RealtimePart,
	Summary: "the identity this tier's realtime gateways run as",
	APIs:    []string{"pubsub.googleapis.com"},
	Retires: true,
	items: func(names names.Names, tier environment.Tier) []Resource {
		return []Resource{{
			Kind: KindServiceAccount, Name: names.RealtimeAccount(tier),
			Note: "the identity this tier's realtime gateways run as, which reads its gateways' keys and holds the Pub/Sub subscriptions its instances hear their topic through",
		}}
	},
}

var Topics = ResourcePart{
	Name:    TopicsPart,
	Summary: "the identity Pub/Sub signs each push to a worker of this tier as, and the queue a delayed message of this tier waits in",
	APIs:    []string{"pubsub.googleapis.com", "cloudtasks.googleapis.com"},
	Retires: true,
	items: func(names names.Names, tier environment.Tier) []Resource {
		return []Resource{
			{
				Kind: KindServiceAccount, Name: names.PushAccount(tier),
				Note: "the identity Pub/Sub signs each push to a worker of this tier as, which Pub/Sub's agent may sign as",
			},
			{
				Kind: KindQueue, Name: names.DelayQueue(tier),
				Note: "the queue a delayed message of this tier waits in, which a deploy grants each app that publishes the right to enqueue on",
			},
		}
	},
}

func DeployedParts() []ResourcePart {
	return []ResourcePart{Keys, EdgeBucket, CacheBucket, Realtime, Topics}
}

func (p ResourcePart) Items(names names.Names, tier environment.Tier) []Resource {
	return p.items(names, tier)
}

func (p ResourcePart) Shape(names names.Names, tier environment.Tier, region string) []pricing.Shaped {
	var shaped []pricing.Shaped
	for _, item := range p.items(names, tier) {
		if each, priced := item.Shape(region); priced {
			shaped = append(shaped, each)
		}
	}
	return shaped
}

func (p ResourcePart) Removable() RemovablePart {
	return RemovablePart{
		Part:     provider.Part{Name: p.Name, Summary: p.Summary},
		Deployed: p.For,
	}
}

func (p ResourcePart) provisionedItems(b Engine, tier environment.Tier) []Resource {
	return slices.DeleteFunc(p.items(b.Clients.Names, tier), func(each Resource) bool { return !provisioned(each, b.Clients.Emulated()) })
}

func (p ResourcePart) For(b Engine, tier environment.Tier) Part {
	items := p.provisionedItems(b, tier)
	part := Part{
		Name:     p.Name,
		Summary:  p.Summary,
		APIs:     p.APIs,
		Retires:  p.Retires,
		Shape:    p.Shape(b.Clients.Names, tier, b.Clients.Region),
		Removals: plannedRemovals(items),
		Make:     func(ctx context.Context) error { return b.makeItems(ctx, tier, items, false) },
		Replace:  func(ctx context.Context) error { return b.makeItems(ctx, tier, items, true) },
		Remove:   func(ctx context.Context) error { return b.removeItems(ctx, tier, items) },
		Users:    func(ctx context.Context) ([]string, error) { return p.listUsers(ctx, b, tier) },
		RefuseInUse: func(ctx context.Context, removal PartRemoval) error {
			return p.refuseInUse(ctx, b, tier, removal)
		},
	}
	for _, item := range items {
		part.Resources = append(part.Resources, provider.PhysicalResource{Kind: string(item.Kind), ID: item.Name})
	}
	return part
}

func plannedRemovals(items []Resource) []provider.Change {
	held := Survey{Held: map[string]bool{}}
	for _, item := range items {
		held.Held[item.ID()] = true
	}
	changes := make([]provider.Change, 0, len(items))
	for _, item := range slices.Backward(items) {
		changes = append(changes, Removing(held, item).change(item.Name))
	}
	return changes
}

func (p ResourcePart) listUsers(ctx context.Context, b Engine, tier environment.Tier) ([]string, error) {
	users := []string{}
	if p.Retires {
		listed, err := stackrecords.ListPartUsers(ctx, b.Records, tier)
		if err != nil {
			return nil, err
		}
		return append(users, listed[p.Name]...), nil
	}
	projects, err := stackrecords.ListProjects(ctx, b.Records, tier)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		users = append(users, project.Slug)
	}
	return users, nil
}

func (p ResourcePart) refuseInUse(ctx context.Context, b Engine, tier environment.Tier, removal PartRemoval) error {
	if err := p.refuseReadByFronts(b, tier, removal); err != nil {
		return err
	}
	users, err := p.listUsers(ctx, b, tier)
	if err != nil || len(users) == 0 {
		return err
	}
	if p.Retires {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s still use the %s part of tier %s, and taking it down breaks what they deploy.\n"+
				"Deploy them without what needs it, or destroy their environments, then run `%s` again",
			strings.Join(users, ", "), p.Name, tier, removal.Retry)
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s are still recorded in tier %s, and the %s part holds the keys their values are sealed under: taking it down loses every one of them.\n"+
			"Destroy those projects in the tier, then run `%s` again",
		strings.Join(users, ", "), tier, p.Name, removal.Retry)
}

func (p ResourcePart) refuseReadByFronts(b Engine, tier environment.Tier, removal PartRemoval) error {
	fronts := b.fronted(removal.Fronts)
	if len(fronts) == 0 || !slices.Contains(frontedParts, p.Name) {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"the %s part of tier %s reads the %s part, and taking it down leaves that front reading what is gone.\n"+
			"Run `%s --remove %s --remove %s` to take both down, or leave the %s part in place",
		fronts[0].Name, tier, p.Name, provider.BootstrapCommand(tier), fronts[0].Name, p.Name, p.Name)
}

func (b Engine) surveyItems(ctx context.Context, tier environment.Tier, items []Resource) (Survey, error) {
	read := b.newSurvey(tier)
	var err error
	if read.Buckets, err = b.Clients.RecordedTierBuckets(ctx, tier); err != nil {
		return Survey{}, err
	}
	if err := b.surveyPresence(ctx, read, items); err != nil {
		return Survey{}, err
	}
	return read, nil
}

func (b Engine) makeItems(ctx context.Context, tier environment.Tier, items []Resource, replacing bool) error {
	read, err := b.surveyItems(ctx, tier, items)
	if err != nil {
		return err
	}
	if !replacing {
		if err := RefuseReplacements(read, items, "run `"+provider.BootstrapCommand(tier)+" --yes`"); err != nil {
			return err
		}
	}
	for _, item := range items {
		if err := b.Provision(ctx, read, item, nil); err != nil {
			return err
		}
	}
	return nil
}

func (b Engine) removeItems(ctx context.Context, tier environment.Tier, items []Resource) error {
	read, err := b.surveyItems(ctx, tier, items)
	if err != nil {
		return err
	}
	for _, item := range slices.Backward(items) {
		if _, err := b.RemoveResource(ctx, read, Removing(read, item)); err != nil {
			return err
		}
	}
	return nil
}
