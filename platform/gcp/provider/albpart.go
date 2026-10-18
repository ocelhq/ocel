package gcp

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
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	albPart    = "alb"
	albSummary = "a global external Application Load Balancer every project behind the alb edge is served through: one address, one certificate map, one URL map, " +
		"and the gate as a Service Extensions plugin kept as a generic artifact in a repository of its own. " +
		"It costs " + alb.BaselineCost + " idle, so the deploy or destroy that removes the last project behind it takes it down"

	albConsumersKey  = "consumers"
	albLeaseKey      = "lease"
	albLeaseAttempts = 5

	albRetireLimit   = 30 * time.Minute
	albSettleTimeout = 30 * time.Second
)

var albPartPermissions = []string{
	"artifactregistry.repositories.create",
	"artifactregistry.repositories.get",
	"artifactregistry.repositories.delete",
}

type albLease struct {
	Retiring      bool      `json:"retiring,omitempty"`
	RetiringSince time.Time `json:"retiring_since,omitzero"`
}

func albConsumers(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(alb.Kind), albConsumersKey)
}

func albConsumerAt(tier environment.Tier, slug string) records.Key {
	consumers := albConsumers(tier)
	return consumers.Partition.Key(append(slices.Clone(consumers.Path), slug)...)
}

func albLeaseAt(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(alb.Kind), albLeaseKey)
}

func (b bootstrap) albPart(tier environment.Tier) part {
	var resources []provider.PartResource
	for _, shaped := range alb.ShapeLoadBalancer(tier, "") {
		resources = append(resources, provider.PartResource{Kind: shaped.Type, ID: shaped.Name})
	}
	resources = append(resources, provider.PartResource{Kind: string(KindRepository), ID: b.clients.Repository(tier) + pluginRepository})
	var shape []pricing.Shaped
	if shaped, err := alb.Shape(pricing.EdgeSite{Tier: tier, Region: b.clients.Region}); err == nil {
		shape = shaped.Shared
	}
	return part{
		name:        albPart,
		summary:     albSummary,
		apis:        albAPIs,
		retires:     true,
		resources:   resources,
		shape:       shape,
		make:        func(ctx context.Context) error { return b.makeALB(ctx, tier) },
		users:       func(ctx context.Context) ([]string, error) { return b.listALBUsers(ctx, tier) },
		refuseInUse: func(ctx context.Context, removal partRemoval) error { return b.refuseALBInUse(ctx, tier, removal) },
		remove:      func(ctx context.Context) error { return b.removeALB(ctx, tier) },
	}
}

func (b bootstrap) openALB() (edge.Edge, error) {
	if b.fronts == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %s part is the %s edge's load balancer, and this bootstrap was opened with no edge registry to open it through", albPart, alb.Kind)
	}
	return b.fronts.Open(alb.Kind, nil)
}

func (b bootstrap) makeALB(ctx context.Context, tier environment.Tier) error {
	front, err := b.openALB()
	if err != nil {
		return err
	}
	_, err = front.Bootstrap(ctx, tier)
	return err
}

func (b bootstrap) listALBBound(ctx context.Context, tier environment.Tier) ([]string, error) {
	front, err := b.openALB()
	if err != nil {
		return nil, err
	}
	bound := front.Hooks().ListBoundHostnames
	if bound == nil {
		return nil, nil
	}
	return bound(ctx, tier)
}

func (b bootstrap) refuseALBInUse(ctx context.Context, tier environment.Tier, removal partRemoval) error {
	bound, err := b.listALBBound(ctx, tier)
	if err != nil || len(bound) == 0 {
		return err
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s is still served by the %s load balancer of tier %s, and the load balancer owns the certificate map those hostnames are entries in, "+
			"which Google will not delete while it contains any.\nRelease them with `ocel domain remove` in the projects that bound them, then run `%s` again",
		strings.Join(bound, ", "), alb.Kind, tier, removal.retry)
}

func (b bootstrap) listALBUsers(ctx context.Context, tier environment.Tier) ([]string, error) {
	consumers := albConsumers(tier)
	entries, err := b.records.List(ctx, consumers.Partition, consumers.Path...)
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

func readALBLease(ctx context.Context, store records.Store, tier environment.Tier) (records.Entry, albLease, error) {
	entry, err := records.ReadOrEmpty(ctx, store, albLeaseAt(tier))
	if err != nil {
		return records.Entry{}, albLease{}, err
	}
	var lease albLease
	if len(entry.Value) == 0 {
		return entry, lease, nil
	}
	if err := json.Unmarshal(entry.Value, &lease); err != nil {
		return records.Entry{}, albLease{}, fmt.Errorf("read the %s load balancer lease %s: %w", alb.Kind, entry.Key, err)
	}
	return entry, lease, nil
}

func writeALBLease(ctx context.Context, store records.Store, entry records.Entry, lease albLease) (records.Revision, error) {
	encoded, err := json.Marshal(lease)
	if err != nil {
		return "", fmt.Errorf("encode the %s load balancer lease: %w", alb.Kind, err)
	}
	entry.Value = encoded
	return store.Write(ctx, entry)
}

func (l albLease) isRetiring(now time.Time) bool {
	return l.Retiring && now.Sub(l.RetiringSince) < albRetireLimit
}

func (b bootstrap) claimALB(ctx context.Context, tier environment.Tier, user string) error {
	wrote := false
	for range albLeaseAttempts {
		leased, lease, err := readALBLease(ctx, b.records, tier)
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
			return errors.Join(refused, records.Forget(ctx, b.records, albConsumerAt(tier, user)))
		}
		takeover := lease.Retiring
		consumer, err := records.ReadOrEmpty(ctx, b.records, albConsumerAt(tier, user))
		if err != nil {
			return err
		}
		if len(consumer.Value) > 0 && !takeover {
			return nil
		}
		if len(consumer.Value) == 0 {
			consumer.Value = []byte("{}")
			_, err := b.records.Write(ctx, consumer)
			switch {
			case err == nil:
				wrote = true
			case !errors.Is(err, records.ErrStale):
				return fmt.Errorf("record %s as a user of the %s load balancer of tier %s: %w", user, alb.Kind, tier, err)
			}
		}
		_, err = writeALBLease(ctx, b.records, leased, albLease{})
		if err == nil {
			return nil
		}
		if !errors.Is(err, records.ErrStale) {
			return fmt.Errorf("claim the %s load balancer of tier %s for %s: %w", alb.Kind, tier, user, err)
		}
	}
	return refusal.Refuse(refusal.CodeBusy,
		"the %s load balancer of tier %s changed hands %d times while %s was claiming it; deploy again", alb.Kind, tier, albLeaseAttempts, user)
}

func (b bootstrap) releaseALB(ctx context.Context, tier environment.Tier, user string, leaving bool, progress progress.Log) error {
	consumer, err := records.ReadOrEmpty(ctx, b.records, albConsumerAt(tier, user))
	if err != nil || len(consumer.Value) == 0 {
		return err
	}
	leased, lease, err := readALBLease(ctx, b.records, tier)
	if err != nil {
		return err
	}
	bound, err := b.listALBBound(ctx, tier)
	if err != nil {
		return err
	}
	if len(bound) > 0 && !leaving {
		users, err := b.listALBUsers(ctx, tier)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(users, func(each string) bool { return each != user }) {
			sayALBKept(progress, tier, bound)
			return nil
		}
	}
	if err := records.Forget(ctx, b.records, albConsumerAt(tier, user)); err != nil {
		return err
	}
	remaining, err := b.listALBUsers(ctx, tier)
	if err != nil || len(remaining) > 0 {
		return err
	}
	if len(bound) > 0 {
		sayALBKept(progress, tier, bound)
		return nil
	}
	if lease.isRetiring(time.Now()) {
		return nil
	}
	revision, err := writeALBLease(ctx, b.records, leased, albLease{Retiring: true, RetiringSince: time.Now().UTC()})
	if errors.Is(err, records.ErrStale) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mark the %s load balancer of tier %s as going down: %w", alb.Kind, tier, err)
	}
	gcloud.EnsureProgress(progress).Say("Taking down the " + string(alb.Kind) + " load balancer of tier " + string(tier) + ": " + user + " was the last user it served")
	if err := b.retireALB(ctx, tier, revision, nil); err != nil {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), albSettleTimeout)
		defer cancel()
		return errors.Join(err, b.restoreALBUser(settle, tier, user))
	}
	return nil
}

func (b bootstrap) restoreALBUser(ctx context.Context, tier environment.Tier, user string) error {
	err := records.Change(ctx, b.records, albConsumerAt(tier, user), func(recorded records.Entry) ([]byte, bool, error) {
		return []byte("{}"), len(recorded.Value) == 0, nil
	})
	if err != nil {
		return fmt.Errorf("record %s again as a user of the %s load balancer of tier %s it could not take down: %w", user, alb.Kind, tier, err)
	}
	return nil
}

func sayALBKept(progress progress.Log, tier environment.Tier, bound []string) {
	gcloud.EnsureProgress(progress).Say("Kept the " + string(alb.Kind) + " load balancer of tier " + string(tier) + ": it still serves " + strings.Join(bound, ", ") +
		", and it is taken down once nothing is bound to it")
}

func (b bootstrap) retireALB(ctx context.Context, tier environment.Tier, retiring records.Revision, users []string) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), albSettleTimeout)
	defer cancel()
	recorded, _, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, albPart)
	if err == nil {
		err = stackrecords.ForgetBootstrapPart(ctx, b.records, tier, albPart)
	}
	if err != nil {
		return errors.Join(err, b.liftALBLease(settle, tier, retiring))
	}
	front, err := b.openALB()
	if err == nil {
		err = front.Teardown(ctx, tier)
	}
	if err != nil {
		return errors.Join(err, b.keepALBRecorded(settle, tier, recorded), b.liftALBLease(settle, tier, retiring))
	}
	for _, user := range users {
		if err != nil {
			break
		}
		err = records.Forget(ctx, b.records, albConsumerAt(tier, user))
	}
	return errors.Join(err, b.liftALBLease(settle, tier, retiring))
}

func (b bootstrap) keepALBRecorded(ctx context.Context, tier environment.Tier, recorded stackrecords.BootstrapPart) error {
	ready := stackrecords.BootstrapPart{
		State:     stackrecords.PartReady,
		AddedBy:   recorded.AddedBy,
		AddedWith: recorded.AddedWith,
		AddedAt:   recorded.AddedAt,
		Resources: b.albPart(tier).resources,
	}
	value, err := json.Marshal(ready)
	if err == nil {
		err = records.Change(ctx, b.records, stackrecords.BootstrapPartKey(tier, albPart), func(prior records.Entry) ([]byte, bool, error) {
			return value, len(prior.Value) == 0, nil
		})
	}
	if err != nil {
		return fmt.Errorf("record the %s load balancer of tier %s again after its teardown failed: %w", alb.Kind, tier, err)
	}
	return nil
}

func (b bootstrap) liftALBLease(ctx context.Context, tier environment.Tier, retiring records.Revision) error {
	err := records.ForgetMatching(ctx, b.records, albLeaseAt(tier), func(recorded records.Entry) (bool, error) {
		return recorded.Revision == retiring, nil
	})
	if err != nil {
		return fmt.Errorf("lift the lease taking the %s load balancer of tier %s down: %w", alb.Kind, tier, err)
	}
	return nil
}

func (b bootstrap) removeALB(ctx context.Context, tier environment.Tier) error {
	leased, lease, err := readALBLease(ctx, b.records, tier)
	if err != nil {
		return err
	}
	if lease.isRetiring(time.Now()) {
		return refusal.Refuse(refusal.CodeBusy,
			"the %s load balancer of tier %s is being taken down by the run that removed the last project behind it; remove this bootstrap again once it has gone",
			alb.Kind, tier)
	}
	revision, err := writeALBLease(ctx, b.records, leased, albLease{Retiring: true, RetiringSince: time.Now().UTC()})
	if errors.Is(err, records.ErrStale) {
		return refusal.Refuse(refusal.CodeBusy,
			"a deploy claimed the %s load balancer of tier %s while this bootstrap was being removed; remove it again", alb.Kind, tier)
	}
	if err != nil {
		return fmt.Errorf("mark the %s load balancer of tier %s as going down: %w", alb.Kind, tier, err)
	}
	users, err := b.listALBUsers(ctx, tier)
	if err != nil {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), albSettleTimeout)
		defer cancel()
		return errors.Join(err, b.liftALBLease(settle, tier, revision))
	}
	return b.retireALB(ctx, tier, revision, users)
}

func (p *Provider) claimALB(ctx context.Context, tier environment.Tier, user string) error {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	if err := b.claimALB(ctx, tier, user); err != nil {
		return err
	}
	return b.ensurePart(ctx, tier, user, b.albPart(tier))
}

func (p *Provider) releaseALB(ctx context.Context, tier environment.Tier, user string) error {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.releaseALB(ctx, tier, user, true, nil)
}
