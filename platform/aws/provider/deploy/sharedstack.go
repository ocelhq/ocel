package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	consumersKey  = "consumers"
	leaseKey      = "lease"
	leaseAttempts = 5

	sharedClaimReleaseTimeout = 30 * time.Second
	sharedTakedownLimit       = time.Hour
)

type sharedStack struct {
	slug        string
	part        string
	subject     string
	consumer    string
	ref         func(environment.Tier) provider.StackRef
	lock        func(*Stacks) *sync.Mutex
	forget      func(context.Context, keyvalue.Store, environment.Tier) error
	refuseInUse func(environment.Tier, []string) error
}

func (s sharedStack) consumersPrefix(tier environment.Tier) keyvalue.Key {
	return stackrecords.StacksPartition(tier, s.slug).Key(consumersKey)
}

func (s sharedStack) consumerAt(ref provider.StackRef) keyvalue.Key {
	prefix := s.consumersPrefix(ref.Tier)
	return prefix.Partition.Key(append(prefix.Path, ref.Project, ref.Name.String())...)
}

func (s sharedStack) leaseAt(tier environment.Tier) keyvalue.Key {
	return stackrecords.StacksPartition(tier, s.slug).Key(leaseKey)
}

type lease struct {
	Destroying      bool      `json:"destroying,omitempty"`
	DestroyingSince time.Time `json:"destroying_since,omitzero"`
	DestroyFailed   bool      `json:"destroy_failed,omitempty"`
}

func (l lease) isGoingDown(now time.Time) bool {
	return l.Destroying && now.Sub(l.DestroyingSince) < sharedTakedownLimit
}

func (l lease) isPartlyDown(now time.Time) bool {
	return l.DestroyFailed || (l.Destroying && !l.isGoingDown(now))
}

func goingDownFrom(now time.Time) lease {
	return lease{Destroying: true, DestroyingSince: now.UTC()}
}

func (s sharedStack) leaseOf(entry keyvalue.Entry) (lease, error) {
	var state lease
	if len(entry.Value) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(entry.Value, &state); err != nil {
		return lease{}, fmt.Errorf("read the %s lease %s: %w", s.subject, entry.Key, err)
	}
	return state, nil
}

func (s sharedStack) readLease(ctx context.Context, store keyvalue.Store, tier environment.Tier) (lease, error) {
	leased, err := keyvalue.ReadOrEmpty(ctx, store, s.leaseAt(tier))
	if err != nil {
		return lease{}, err
	}
	return s.leaseOf(leased)
}

func (s sharedStack) writeLease(ctx context.Context, store keyvalue.Store, entry keyvalue.Entry, state lease) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode the %s lease: %w", s.subject, err)
	}
	entry.Value = encoded
	_, err = store.Write(ctx, entry)
	return err
}

func (s sharedStack) listUsers(ctx context.Context, store keyvalue.Store, tier environment.Tier) ([]string, error) {
	consumers := s.consumersPrefix(tier)
	entries, err := store.List(ctx, consumers.Partition, consumers.Path...)
	if err != nil {
		return nil, fmt.Errorf("read who uses the shared %s for the %s tier: %w", s.subject, tier, err)
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

func raiseShared(ctx context.Context, share sharedStack, owner *release, tier environment.Tier, addedBy string, raise func() ([]provider.PartResource, error)) error {
	store, writer := owner.cfg.KeyValues, string(owner.cfg.WrittenBy)
	claim, claimed, err := stackrecords.ClaimBootstrapPart(ctx, store, tier, share.part, stackrecords.BootstrapPart{
		WrittenBy: writer,
		AddedBy:   addedBy,
		AddedAt:   time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	resources, err := raise()
	if err != nil && claimed {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedClaimReleaseTimeout)
		defer cancel()
		return errors.Join(err, stackrecords.ReleaseBootstrapPart(settle, store, tier, share.part, claim))
	}
	if err != nil {
		return err
	}
	return stackrecords.MarkBootstrapPartReady(ctx, store, tier, share.part, stackrecords.BootstrapPart{
		WrittenBy: writer,
		AddedBy:   addedBy,
		AddedAt:   time.Now().UTC(),
		Resources: resources,
	})
}

func (r *Stacks) claimShared(ctx context.Context, share sharedStack, ref provider.StackRef) error {
	owner, err := r.at(ctx, share.ref(ref.Tier), "")
	if err != nil {
		return err
	}
	store := owner.cfg.KeyValues
	for range leaseAttempts {
		leased, err := keyvalue.ReadOrEmpty(ctx, store, share.leaseAt(ref.Tier))
		if err != nil {
			return err
		}
		state, err := share.leaseOf(leased)
		if err != nil {
			return err
		}
		if state.isGoingDown(time.Now()) {
			return errors.Join(
				refusal.Refuse(refusal.CodeBusy,
					"the shared %s for the %s tier is being taken down by another deploy whose last %s just left; re-run this deploy once it has gone and it will provision a fresh one", share.subject, ref.Tier, share.consumer),
				keyvalue.Forget(ctx, store, share.consumerAt(ref)))
		}
		entry, err := keyvalue.ReadOrEmpty(ctx, store, share.consumerAt(ref))
		if err != nil {
			return err
		}
		entry.Value = []byte("{}")
		if _, err := store.Write(ctx, entry); err != nil && !errors.Is(err, keyvalue.ErrStale) {
			return fmt.Errorf("record %s as a consumer of the shared %s: %w", ref.Name, share.subject, err)
		}
		err = share.writeLease(ctx, store, leased, lease{})
		if err == nil {
			return nil
		}
		if !errors.Is(err, keyvalue.ErrStale) {
			return fmt.Errorf("claim the shared %s for %s: %w", share.subject, ref.Name, err)
		}
	}
	return refusal.Refuse(refusal.CodeBusy,
		"the shared %s for the %s tier changed hands %d times while %s was claiming it; re-run this deploy", share.subject, ref.Tier, leaseAttempts, ref.Name)
}

func (r *Stacks) releaseShared(ctx context.Context, share sharedStack, store keyvalue.Store, ref provider.StackRef, progress progress.Log) error {
	if store == nil {
		return nil
	}
	lock := share.lock(r)
	lock.Lock()
	defer lock.Unlock()
	consumer, err := keyvalue.ReadOrEmpty(ctx, store, share.consumerAt(ref))
	if err != nil {
		return err
	}
	if len(consumer.Value) == 0 {
		return nil
	}
	leased, err := keyvalue.ReadOrEmpty(ctx, store, share.leaseAt(ref.Tier))
	if err != nil {
		return err
	}
	if err := keyvalue.Forget(ctx, store, share.consumerAt(ref)); err != nil {
		return err
	}
	owner, err := r.at(ctx, share.ref(ref.Tier), "")
	if err != nil {
		return err
	}
	consumers := share.consumersPrefix(ref.Tier)
	remaining, err := store.List(ctx, consumers.Partition, consumers.Path...)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	if err := share.writeLease(ctx, store, leased, goingDownFrom(time.Now())); err != nil {
		if errors.Is(err, keyvalue.ErrStale) {
			return nil
		}
		return fmt.Errorf("mark the shared %s for the %s tier as going down: %w", share.subject, ref.Tier, err)
	}
	if progress != nil {
		progress.Say("Taking down the shared " + share.subject + " for the " + string(ref.Tier) + " tier: the last " + share.consumer + " in it is gone")
	}
	return retireShared(ctx, share, owner, store, ref.Tier, progress)
}

func (r *Stacks) removeShared(ctx context.Context, share sharedStack, tier environment.Tier, progress progress.Log) error {
	lock := share.lock(r)
	lock.Lock()
	defer lock.Unlock()
	owner, err := r.at(ctx, share.ref(tier), "")
	if err != nil {
		return err
	}
	store := owner.cfg.KeyValues
	users, err := share.listUsers(ctx, store, tier)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return share.refuseInUse(tier, users)
	}
	leased, err := keyvalue.ReadOrEmpty(ctx, store, share.leaseAt(tier))
	if err != nil {
		return err
	}
	state, err := share.leaseOf(leased)
	if err != nil {
		return err
	}
	if state.isGoingDown(time.Now()) {
		return refusal.Refuse(refusal.CodeBusy, "the shared %s of the %s tier is being taken down by the deploy its last %s left with; run the removal again once it has gone", share.subject, tier, share.consumer)
	}
	if err := share.writeLease(ctx, store, leased, goingDownFrom(time.Now())); err != nil {
		if errors.Is(err, keyvalue.ErrStale) {
			return refusal.Refuse(refusal.CodeBusy, "a deploy took the shared %s of the %s tier while this run was removing it; run the removal again", share.subject, tier)
		}
		return fmt.Errorf("mark the shared %s for the %s tier as going down: %w", share.subject, tier, err)
	}
	return retireShared(ctx, share, owner, store, tier, progress)
}

func retireShared(ctx context.Context, share sharedStack, owner *release, store keyvalue.Store, tier environment.Tier, progress progress.Log) error {
	stack := share.ref(tier)
	if err := owner.automation.Destroy(ctx, stack, provider.Lease{}, progress); err != nil {
		failed := fmt.Errorf("take down the shared %s for the %s tier: %w\n"+
			"It stays recorded as the %s part, used by no project: run `%s --remove %s` to take it down again, or deploy a %s and it raises it again",
			share.subject, tier, err, share.part, provider.BootstrapCommand(tier), share.part, share.consumer)
		return errors.Join(failed, share.markDestroyFailed(ctx, store, tier))
	}
	if err := stackrecords.Forget(ctx, store, tier, share.slug, stack.Name); err != nil {
		return err
	}
	if share.forget != nil {
		if err := share.forget(ctx, store, tier); err != nil {
			return err
		}
	}
	if err := stackrecords.ForgetBootstrapPart(ctx, store, tier, share.part); err != nil {
		return err
	}
	return keyvalue.Forget(ctx, store, share.leaseAt(tier))
}

func (s sharedStack) markDestroyFailed(ctx context.Context, store keyvalue.Store, tier environment.Tier) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedClaimReleaseTimeout)
	defer cancel()
	leased, err := keyvalue.ReadOrEmpty(settle, store, s.leaseAt(tier))
	if err != nil {
		return err
	}
	if err := s.writeLease(settle, store, leased, lease{DestroyFailed: true}); err != nil {
		return fmt.Errorf("record that the shared %s for the %s tier failed to go down: %w", s.subject, tier, err)
	}
	return nil
}
