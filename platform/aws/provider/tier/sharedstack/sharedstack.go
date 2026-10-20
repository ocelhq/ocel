package sharedstack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	consumersKey  = "consumers"
	leaseKey      = "lease"
	leaseAttempts = 5

	claimReleaseTimeout = 30 * time.Second
	TakedownLimit       = time.Hour
)

type Owner struct {
	Automation *pulumi.Automation[component.Program]
	KeyValues  records.Store
	WrittenBy  provider.WrittenBy

	Namespace      string
	Region         string
	EC2            component.NetworkAPI
	Quotas         component.QuotasAPI
	NetworkCIDR    string
	VPCOrigins     awsports.VPCOriginsAPI
	AppBoundaryARN string
	AppRolePath    string
}

type Stack struct {
	Slug        string
	Part        string
	Subject     string
	Consumer    string
	Ref         func(environment.Tier) provider.StackRef
	Forget      func(context.Context, records.Store, environment.Tier) error
	RefuseInUse func(environment.Tier, []string) error
}

func (s Stack) ConsumersPrefix(tier environment.Tier) records.Key {
	return stackrecords.StacksPartition(tier, s.Slug).Key(consumersKey)
}

func (s Stack) ConsumerAt(ref provider.StackRef) records.Key {
	prefix := s.ConsumersPrefix(ref.Tier)
	return prefix.Partition.Key(append(prefix.Path, ref.Project, ref.Name.String())...)
}

func (s Stack) LeaseAt(tier environment.Tier) records.Key {
	return stackrecords.StacksPartition(tier, s.Slug).Key(leaseKey)
}

type Lease struct {
	Destroying      bool      `json:"destroying,omitempty"`
	DestroyingSince time.Time `json:"destroying_since,omitzero"`
	DestroyFailed   bool      `json:"destroy_failed,omitempty"`
}

func (l Lease) IsGoingDown(now time.Time) bool {
	return l.Destroying && now.Sub(l.DestroyingSince) < TakedownLimit
}

func (l Lease) IsPartlyDown(now time.Time) bool {
	return l.DestroyFailed || (l.Destroying && !l.IsGoingDown(now))
}

func goingDownFrom(now time.Time) Lease {
	return Lease{Destroying: true, DestroyingSince: now.UTC()}
}

func (s Stack) leaseOf(entry records.Entry) (Lease, error) {
	var state Lease
	if len(entry.Value) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(entry.Value, &state); err != nil {
		return Lease{}, fmt.Errorf("read the %s lease %s: %w", s.Subject, entry.Key, err)
	}
	return state, nil
}

func (s Stack) ReadLease(ctx context.Context, store records.Store, tier environment.Tier) (Lease, error) {
	leased, err := records.ReadOrEmpty(ctx, store, s.LeaseAt(tier))
	if err != nil {
		return Lease{}, err
	}
	return s.leaseOf(leased)
}

func (s Stack) WriteLease(ctx context.Context, store records.Store, entry records.Entry, state Lease) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode the %s lease: %w", s.Subject, err)
	}
	entry.Value = encoded
	_, err = store.Write(ctx, entry)
	return err
}

func (s Stack) ListUsers(ctx context.Context, store records.Store, tier environment.Tier) ([]string, error) {
	consumers := s.ConsumersPrefix(tier)
	entries, err := store.List(ctx, consumers.Partition, consumers.Path...)
	if err != nil {
		return nil, fmt.Errorf("read who uses the shared %s for the %s tier: %w", s.Subject, tier, err)
	}
	users := []string{}
	for _, entry := range entries {
		if rest, ok := entry.Key.Under(consumers.Path...); ok {
			users = append(users, rest[0])
		}
	}
	slices.Sort(users)
	return slices.Compact(users), nil
}

func (s Stack) Raise(ctx context.Context, owner Owner, tier environment.Tier, addedBy string, raise func() ([]provider.PhysicalResource, error)) error {
	store, writer := owner.KeyValues, string(owner.WrittenBy)
	claim, claimed, err := stackrecords.ClaimBootstrapPart(ctx, store, tier, s.Part, stackrecords.BootstrapPart{
		WrittenBy: writer,
		AddedBy:   addedBy,
		AddedAt:   time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	resources, err := raise()
	if err != nil && claimed {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimReleaseTimeout)
		defer cancel()
		return errors.Join(err, stackrecords.ReleaseBootstrapPart(settle, store, tier, s.Part, claim))
	}
	if err != nil {
		return err
	}
	return stackrecords.MarkBootstrapPartReady(ctx, store, tier, s.Part, stackrecords.BootstrapPart{
		WrittenBy: writer,
		AddedBy:   addedBy,
		AddedAt:   time.Now().UTC(),
		Resources: resources,
	})
}

func (s Stack) Claim(ctx context.Context, owner Owner, ref provider.StackRef) error {
	store := owner.KeyValues
	for range leaseAttempts {
		leased, err := records.ReadOrEmpty(ctx, store, s.LeaseAt(ref.Tier))
		if err != nil {
			return err
		}
		state, err := s.leaseOf(leased)
		if err != nil {
			return err
		}
		if state.IsGoingDown(time.Now()) {
			return errors.Join(
				refusal.Refuse(refusal.CodeBusy,
					"the shared %s for the %s tier is being taken down by another deploy whose last %s just left; re-run this deploy once it has gone and it will provision a fresh one", s.Subject, ref.Tier, s.Consumer),
				records.Forget(ctx, store, s.ConsumerAt(ref)))
		}
		entry, err := records.ReadOrEmpty(ctx, store, s.ConsumerAt(ref))
		if err != nil {
			return err
		}
		entry.Value = []byte("{}")
		if _, err := store.Write(ctx, entry); err != nil && !errors.Is(err, records.ErrStale) {
			return fmt.Errorf("record %s as a consumer of the shared %s: %w", ref.Name, s.Subject, err)
		}
		err = s.WriteLease(ctx, store, leased, Lease{})
		if err == nil {
			return nil
		}
		if !errors.Is(err, records.ErrStale) {
			return fmt.Errorf("claim the shared %s for %s: %w", s.Subject, ref.Name, err)
		}
	}
	return refusal.Refuse(refusal.CodeBusy,
		"the shared %s for the %s tier changed hands %d times while %s was claiming it; re-run this deploy", s.Subject, ref.Tier, leaseAttempts, ref.Name)
}

func (s Stack) Release(ctx context.Context, open func(context.Context, provider.StackRef) (Owner, error), store records.Store, ref provider.StackRef, progress progress.Log) error {
	consumer, err := records.ReadOrEmpty(ctx, store, s.ConsumerAt(ref))
	if err != nil {
		return err
	}
	if len(consumer.Value) == 0 {
		return nil
	}
	leased, err := records.ReadOrEmpty(ctx, store, s.LeaseAt(ref.Tier))
	if err != nil {
		return err
	}
	if err := records.Forget(ctx, store, s.ConsumerAt(ref)); err != nil {
		return err
	}
	owner, err := open(ctx, s.Ref(ref.Tier))
	if err != nil {
		return err
	}
	consumers := s.ConsumersPrefix(ref.Tier)
	remaining, err := store.List(ctx, consumers.Partition, consumers.Path...)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	if err := s.WriteLease(ctx, store, leased, goingDownFrom(time.Now())); err != nil {
		if errors.Is(err, records.ErrStale) {
			return nil
		}
		return fmt.Errorf("mark the shared %s for the %s tier as going down: %w", s.Subject, ref.Tier, err)
	}
	if progress != nil {
		progress.Say("Taking down the shared " + s.Subject + " for the " + string(ref.Tier) + " tier: the last " + s.Consumer + " in it is gone")
	}
	return s.retire(ctx, owner, store, ref.Tier, progress)
}

func (s Stack) ForgetConsumer(ctx context.Context, store records.Store, ref provider.StackRef) error {
	return records.Forget(ctx, store, s.ConsumerAt(ref))
}

func (s Stack) Remove(ctx context.Context, owner Owner, tier environment.Tier, progress progress.Log) error {
	store := owner.KeyValues
	users, err := s.ListUsers(ctx, store, tier)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return s.RefuseInUse(tier, users)
	}
	leased, err := records.ReadOrEmpty(ctx, store, s.LeaseAt(tier))
	if err != nil {
		return err
	}
	state, err := s.leaseOf(leased)
	if err != nil {
		return err
	}
	if state.IsGoingDown(time.Now()) {
		return refusal.Refuse(refusal.CodeBusy, "the shared %s of the %s tier is being taken down by the deploy its last %s left with; run the removal again once it has gone", s.Subject, tier, s.Consumer)
	}
	if err := s.WriteLease(ctx, store, leased, goingDownFrom(time.Now())); err != nil {
		if errors.Is(err, records.ErrStale) {
			return refusal.Refuse(refusal.CodeBusy, "a deploy took the shared %s of the %s tier while this run was removing it; run the removal again", s.Subject, tier)
		}
		return fmt.Errorf("mark the shared %s for the %s tier as going down: %w", s.Subject, tier, err)
	}
	return s.retire(ctx, owner, store, tier, progress)
}

func (s Stack) retire(ctx context.Context, owner Owner, store records.Store, tier environment.Tier, progress progress.Log) error {
	stack := s.Ref(tier)
	if err := owner.Automation.Destroy(ctx, stack, provider.Lease{}, progress); err != nil {
		failed := fmt.Errorf("take down the shared %s for the %s tier: %w\n"+
			"It stays recorded as the %s part, used by no project: run `%s --remove %s` to take it down again, or deploy a %s and it raises it again",
			s.Subject, tier, err, s.Part, provider.BootstrapCommand(tier), s.Part, s.Consumer)
		return errors.Join(failed, s.markDestroyFailed(ctx, store, tier))
	}
	if err := stackrecords.Forget(ctx, store, tier, s.Slug, stack.Name); err != nil {
		return err
	}
	if s.Forget != nil {
		if err := s.Forget(ctx, store, tier); err != nil {
			return err
		}
	}
	if err := stackrecords.ForgetBootstrapPart(ctx, store, tier, s.Part); err != nil {
		return err
	}
	return records.Forget(ctx, store, s.LeaseAt(tier))
}

func (s Stack) markDestroyFailed(ctx context.Context, store records.Store, tier environment.Tier) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimReleaseTimeout)
	defer cancel()
	leased, err := records.ReadOrEmpty(settle, store, s.LeaseAt(tier))
	if err != nil {
		return err
	}
	if err := s.WriteLease(settle, store, leased, Lease{DestroyFailed: true}); err != nil {
		return fmt.Errorf("record that the shared %s for the %s tier failed to go down: %w", s.Subject, tier, err)
	}
	return nil
}
