package albpart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	PartName = "alb"
	Summary  = "a global external Application Load Balancer every project behind the alb edge is served through: one address, one certificate map, one URL map, " +
		"and the gate as a Service Extensions plugin kept as a generic artifact in a repository of its own. " +
		"It costs " + alb.BaselineCost + " idle, so the deploy or destroy that removes the last project behind it takes it down"

	consumersKey  = "consumers"
	leaseKey      = "lease"
	leaseAttempts = 5

	RetireLimit   = 30 * time.Minute
	settleTimeout = 30 * time.Second
)

type Lease struct {
	Retiring      bool      `json:"retiring,omitempty"`
	RetiringSince time.Time `json:"retiring_since,omitzero"`
}

func Consumers(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(alb.Kind), consumersKey)
}

func ConsumerAt(tier environment.Tier, slug string) records.Key {
	consumers := Consumers(tier)
	return consumers.Partition.Key(append(slices.Clone(consumers.Path), slug)...)
}

func LeaseAt(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(alb.Kind), leaseKey)
}

var Part = bootstrap.RemovablePart{Part: provider.Part{Name: PartName, Summary: Summary}, Deployed: NewPart}

func NewPart(b bootstrap.Engine, tier environment.Tier) bootstrap.Part {
	var resources []provider.PhysicalResource
	for _, shaped := range alb.ShapeLoadBalancer(string(b.Clients.Namespace()), tier) {
		resources = append(resources, provider.PhysicalResource{Kind: shaped.Type, ID: shaped.Name})
	}
	resources = append(resources, provider.PhysicalResource{Kind: string(bootstrap.KindRepository), ID: b.Clients.Repository(tier) + PluginRepository})
	var shape []pricing.Shaped
	if shaped, err := alb.Shape(string(b.Clients.Namespace()), pricing.EdgeSite{Tier: tier, Region: b.Clients.Region}); err == nil {
		shape = shaped.Shared
	}
	return bootstrap.Part{
		Name:      PartName,
		Summary:   Summary,
		APIs:      bootstrap.ALBAPIs,
		Retires:   true,
		Resources: resources,
		Shape:     shape,
		Make:      func(ctx context.Context) error { return makeLoadBalancer(ctx, b, tier) },
		Claim:     func(ctx context.Context, project string) error { return Claim(ctx, b, tier, project) },
		Users:     func(ctx context.Context) ([]string, error) { return ListUsers(ctx, b, tier) },
		RefuseInUse: func(ctx context.Context, removal bootstrap.PartRemoval) error {
			return refuseInUse(ctx, b, tier, removal)
		},
		Remove: func(ctx context.Context) error { return removeLoadBalancer(ctx, b, tier) },
	}
}

func openEdge(b bootstrap.Engine) (edge.Edge, error) {
	if b.Edges == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %s part is the %s edge's load balancer, and this bootstrap was opened with no edge registry to open it through", PartName, alb.Kind)
	}
	return b.Edges.Open(alb.Kind, nil)
}

func makeLoadBalancer(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	front, err := openEdge(b)
	if err != nil {
		return err
	}
	_, err = front.Bootstrap(ctx, tier)
	return err
}

func listBound(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]string, error) {
	front, err := openEdge(b)
	if err != nil {
		return nil, err
	}
	bound := front.Hooks().ListBoundHostnames
	if bound == nil {
		return nil, nil
	}
	return bound(ctx, tier)
}

func refuseInUse(ctx context.Context, b bootstrap.Engine, tier environment.Tier, removal bootstrap.PartRemoval) error {
	bound, err := listBound(ctx, b, tier)
	if err != nil || len(bound) == 0 {
		return err
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s is still served by the %s load balancer of tier %s, and the load balancer owns the certificate map those hostnames are entries in, "+
			"which Google will not delete while it contains any.\nRelease them with `ocel domain remove` in the projects that bound them, then run `%s` again",
		strings.Join(bound, ", "), alb.Kind, tier, removal.Retry)
}

func ListUsers(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]string, error) {
	consumers := Consumers(tier)
	entries, err := b.Records.List(ctx, consumers.Partition, consumers.Path...)
	if err != nil {
		return nil, fmt.Errorf("read which projects the %s load balancer of tier %s serves: %w", alb.Kind, tier, err)
	}
	users := []string{}
	for _, entry := range entries {
		if rest, ok := entry.Key.Under(consumers.Path...); ok && len(rest) == 1 {
			users = append(users, rest[0])
		}
	}
	slices.Sort(users)
	return users, nil
}

func ReadLease(ctx context.Context, store records.Store, tier environment.Tier) (records.Entry, Lease, error) {
	entry, err := records.ReadOrEmpty(ctx, store, LeaseAt(tier))
	if err != nil {
		return records.Entry{}, Lease{}, err
	}
	var lease Lease
	if len(entry.Value) == 0 {
		return entry, lease, nil
	}
	if err := json.Unmarshal(entry.Value, &lease); err != nil {
		return records.Entry{}, Lease{}, fmt.Errorf("read the %s load balancer lease %s: %w", alb.Kind, entry.Key, err)
	}
	return entry, lease, nil
}

func WriteLease(ctx context.Context, store records.Store, entry records.Entry, lease Lease) (records.Revision, error) {
	encoded, err := json.Marshal(lease)
	if err != nil {
		return "", fmt.Errorf("encode the %s load balancer lease: %w", alb.Kind, err)
	}
	entry.Value = encoded
	return store.Write(ctx, entry)
}

func (l Lease) isRetiring(now time.Time) bool {
	return l.Retiring && now.Sub(l.RetiringSince) < RetireLimit
}

func Claim(ctx context.Context, b bootstrap.Engine, tier environment.Tier, user string) error {
	wrote := false
	for range leaseAttempts {
		leased, lease, err := ReadLease(ctx, b.Records, tier)
		if err != nil {
			return err
		}
		if lease.isRetiring(time.Now()) {
			refused := refusal.Refuse(refusal.CodeBusy,
				"the %s load balancer of tier %s is being taken down by the run that removed the last project behind it; deploy again once it has gone and this deploy adds a fresh one",
				alb.Kind, tier)
			if !wrote {
				return refused
			}
			return errors.Join(refused, records.Forget(ctx, b.Records, ConsumerAt(tier, user)))
		}
		takeover := lease.Retiring
		consumer, err := records.ReadOrEmpty(ctx, b.Records, ConsumerAt(tier, user))
		if err != nil {
			return err
		}
		if len(consumer.Value) > 0 && !takeover {
			return nil
		}
		if len(consumer.Value) == 0 {
			consumer.Value = []byte("{}")
			_, err := b.Records.Write(ctx, consumer)
			switch {
			case err == nil:
				wrote = true
			case !errors.Is(err, records.ErrStale):
				return fmt.Errorf("record %s as a user of the %s load balancer of tier %s: %w", user, alb.Kind, tier, err)
			}
		}
		_, err = WriteLease(ctx, b.Records, leased, Lease{})
		if err == nil {
			return nil
		}
		if !errors.Is(err, records.ErrStale) {
			return fmt.Errorf("claim the %s load balancer of tier %s for %s: %w", alb.Kind, tier, user, err)
		}
	}
	return refusal.Refuse(refusal.CodeBusy,
		"the %s load balancer of tier %s changed hands %d times while %s was claiming it; deploy again", alb.Kind, tier, leaseAttempts, user)
}

func Release(ctx context.Context, b bootstrap.Engine, tier environment.Tier, user string, leaving bool, progress progress.Log) error {
	consumer, err := records.ReadOrEmpty(ctx, b.Records, ConsumerAt(tier, user))
	if err != nil || len(consumer.Value) == 0 {
		return err
	}
	leased, lease, err := ReadLease(ctx, b.Records, tier)
	if err != nil {
		return err
	}
	bound, err := listBound(ctx, b, tier)
	if err != nil {
		return err
	}
	if len(bound) > 0 && !leaving {
		users, err := ListUsers(ctx, b, tier)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(users, func(each string) bool { return each != user }) {
			sayKept(progress, tier, bound)
			return nil
		}
	}
	if err := records.Forget(ctx, b.Records, ConsumerAt(tier, user)); err != nil {
		return err
	}
	remaining, err := ListUsers(ctx, b, tier)
	if err != nil || len(remaining) > 0 {
		return err
	}
	if len(bound) > 0 {
		sayKept(progress, tier, bound)
		return nil
	}
	if lease.isRetiring(time.Now()) {
		return nil
	}
	revision, err := WriteLease(ctx, b.Records, leased, Lease{Retiring: true, RetiringSince: time.Now().UTC()})
	if errors.Is(err, records.ErrStale) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mark the %s load balancer of tier %s as going down: %w", alb.Kind, tier, err)
	}
	gcloud.EnsureProgress(progress).Say("Taking down the " + string(alb.Kind) + " load balancer of tier " + string(tier) + ": " + user + " was the last user it served")
	if err := retireLoadBalancer(ctx, b, tier, revision, nil); err != nil {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
		defer cancel()
		return errors.Join(err, restoreUser(settle, b, tier, user))
	}
	return nil
}

func restoreUser(ctx context.Context, b bootstrap.Engine, tier environment.Tier, user string) error {
	err := records.Change(ctx, b.Records, ConsumerAt(tier, user), func(recorded records.Entry) ([]byte, bool, error) {
		return []byte("{}"), len(recorded.Value) == 0, nil
	})
	if err != nil {
		return fmt.Errorf("record %s again as a user of the %s load balancer of tier %s it could not take down: %w", user, alb.Kind, tier, err)
	}
	return nil
}

func sayKept(progress progress.Log, tier environment.Tier, bound []string) {
	gcloud.EnsureProgress(progress).Say("Kept the " + string(alb.Kind) + " load balancer of tier " + string(tier) + ": it still serves " + strings.Join(bound, ", ") +
		", and it is taken down once nothing is bound to it")
}

func retireLoadBalancer(ctx context.Context, b bootstrap.Engine, tier environment.Tier, retiring records.Revision, users []string) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	recorded, _, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, PartName)
	if err == nil {
		err = stackrecords.ForgetBootstrapPart(ctx, b.Records, tier, PartName)
	}
	if err != nil {
		return errors.Join(err, liftLease(settle, b, tier, retiring))
	}
	front, err := openEdge(b)
	if err == nil {
		err = front.Teardown(ctx, tier)
	}
	if err != nil {
		return errors.Join(err, keepRecorded(settle, b, tier, recorded), liftLease(settle, b, tier, retiring))
	}
	for _, user := range users {
		if err != nil {
			break
		}
		err = records.Forget(ctx, b.Records, ConsumerAt(tier, user))
	}
	return errors.Join(err, liftLease(settle, b, tier, retiring))
}

func keepRecorded(ctx context.Context, b bootstrap.Engine, tier environment.Tier, recorded stackrecords.BootstrapPart) error {
	ready := stackrecords.BootstrapPart{
		State:     stackrecords.PartReady,
		AddedBy:   recorded.AddedBy,
		AddedWith: recorded.AddedWith,
		AddedAt:   recorded.AddedAt,
		Resources: NewPart(b, tier).Resources,
	}
	value, err := json.Marshal(ready)
	if err == nil {
		err = records.Change(ctx, b.Records, stackrecords.BootstrapPartKey(tier, PartName), func(prior records.Entry) ([]byte, bool, error) {
			return value, len(prior.Value) == 0, nil
		})
	}
	if err != nil {
		return fmt.Errorf("record the %s load balancer of tier %s again after its teardown failed: %w", alb.Kind, tier, err)
	}
	return nil
}

func liftLease(ctx context.Context, b bootstrap.Engine, tier environment.Tier, retiring records.Revision) error {
	err := records.ForgetMatching(ctx, b.Records, LeaseAt(tier), func(recorded records.Entry) (bool, error) {
		return recorded.Revision == retiring, nil
	})
	if err != nil {
		return fmt.Errorf("lift the lease taking the %s load balancer of tier %s down: %w", alb.Kind, tier, err)
	}
	return nil
}

func removeLoadBalancer(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	leased, lease, err := ReadLease(ctx, b.Records, tier)
	if err != nil {
		return err
	}
	if lease.isRetiring(time.Now()) {
		return refusal.Refuse(refusal.CodeBusy,
			"the %s load balancer of tier %s is being taken down by the run that removed the last project behind it; remove this bootstrap again once it has gone",
			alb.Kind, tier)
	}
	revision, err := WriteLease(ctx, b.Records, leased, Lease{Retiring: true, RetiringSince: time.Now().UTC()})
	if errors.Is(err, records.ErrStale) {
		return refusal.Refuse(refusal.CodeBusy,
			"a deploy claimed the %s load balancer of tier %s while this bootstrap was being removed; remove it again", alb.Kind, tier)
	}
	if err != nil {
		return fmt.Errorf("mark the %s load balancer of tier %s as going down: %w", alb.Kind, tier, err)
	}
	users, err := ListUsers(ctx, b, tier)
	if err != nil {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
		defer cancel()
		return errors.Join(err, liftLease(settle, b, tier, revision))
	}
	return retireLoadBalancer(ctx, b, tier, revision, users)
}

func Front(openBootstrap func(context.Context) (bootstrap.Engine, error)) *alb.FrontPart {
	return &alb.FrontPart{
		Claim: func(ctx context.Context, tier environment.Tier, user string) error {
			b, err := openBootstrap(ctx)
			if err != nil {
				return err
			}
			if err := Claim(ctx, b, tier, user); err != nil {
				return err
			}
			if err := b.EnsurePart(ctx, tier, user, bootstrap.EdgeBucket.For(b, tier)); err != nil {
				return err
			}
			return b.EnsurePart(ctx, tier, user, NewPart(b, tier))
		},
		Release: func(ctx context.Context, tier environment.Tier, user string) error {
			b, err := openBootstrap(ctx)
			if err != nil {
				return err
			}
			return Release(ctx, b, tier, user, true, nil)
		},
	}
}
