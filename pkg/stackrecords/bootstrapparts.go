package stackrecords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

type PartState string

const (
	PartCreating PartState = "creating"
	PartReady    PartState = "ready"
)

type BootstrapPart struct {
	State     PartState               `json:"state"`
	WrittenBy string                  `json:"written_by,omitempty"`
	Digest    string                  `json:"digest,omitempty"`
	AddedBy   string                  `json:"added_by,omitempty"`
	AddedWith string                  `json:"added_with,omitempty"`
	AddedAt   time.Time               `json:"added_at"`
	ClaimedAt time.Time               `json:"claimed_at,omitzero"`
	Resources []provider.PartResource `json:"resources,omitempty"`
}

func ClaimBootstrapPart(ctx context.Context, store records.Store, tier environment.Tier, name string, claim BootstrapPart) (BootstrapPart, bool, error) {
	claim.State, claim.ClaimedAt = PartCreating, time.Now().UTC()
	value, err := json.Marshal(claim)
	if err != nil {
		return BootstrapPart{}, false, fmt.Errorf("record the %s bootstrap part %s: %w", tier, name, err)
	}
	for range claimAttempts {
		_, err = store.Write(ctx, records.Entry{Key: BootstrapPartKey(tier, name), Value: value})
		if err == nil {
			return claim, true, nil
		}
		if !errors.Is(err, records.ErrStale) {
			return BootstrapPart{}, false, fmt.Errorf("claim the %s bootstrap part %s: %w", tier, name, err)
		}
		recorded, present, err := ReadBootstrapPart(ctx, store, tier, name)
		if err != nil || present {
			return recorded, false, err
		}
	}
	return BootstrapPart{}, false, fmt.Errorf(
		"claim the %s bootstrap part %s: it was written and forgotten between every claim and the read that followed, %d times over",
		tier, name, claimAttempts)
}

const claimAttempts = 5

func ReleaseBootstrapPart(ctx context.Context, store records.Store, tier environment.Tier, name string, claim BootstrapPart) error {
	err := records.ForgetMatching(ctx, store, BootstrapPartKey(tier, name), func(recorded records.Entry) (bool, error) {
		part, err := decodeBootstrapPart(recorded)
		return err == nil && part.State == PartCreating && part.WrittenBy == claim.WrittenBy && part.AddedAt.Equal(claim.AddedAt), err
	})
	if err != nil {
		return fmt.Errorf("release the claim on the %s bootstrap part %s: %w", tier, name, err)
	}
	return nil
}

func MarkBootstrapPartReady(ctx context.Context, store records.Store, tier environment.Tier, name string, ready BootstrapPart) error {
	return records.Change(ctx, store, BootstrapPartKey(tier, name), func(recorded records.Entry) ([]byte, bool, error) {
		part := ready
		if len(recorded.Value) > 0 {
			prior, err := decodeBootstrapPart(recorded)
			if err != nil {
				return nil, false, err
			}
			if provider.WrittenBy(prior.WrittenBy).Newer(provider.WrittenBy(ready.WrittenBy)) {
				return nil, false, nil
			}
			part.AddedBy, part.AddedWith, part.AddedAt = prior.AddedBy, prior.AddedWith, prior.AddedAt
		}
		part.State = PartReady
		value, err := json.Marshal(part)
		if err != nil {
			return nil, false, fmt.Errorf("record the %s bootstrap part %s: %w", tier, name, err)
		}
		return value, true, nil
	})
}

func ReadBootstrapPart(ctx context.Context, store records.Store, tier environment.Tier, name string) (BootstrapPart, bool, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, BootstrapPartKey(tier, name))
	if err != nil {
		return BootstrapPart{}, false, fmt.Errorf("read the %s bootstrap part %s: %w", tier, name, err)
	}
	if len(recorded.Value) == 0 {
		return BootstrapPart{}, false, nil
	}
	part, err := decodeBootstrapPart(recorded)
	return part, err == nil, err
}

func ListBootstrapParts(ctx context.Context, store records.Store, tier environment.Tier) (map[string]BootstrapPart, error) {
	entries, err := store.List(ctx, bootstrapPartition(tier), partsSegment)
	if err != nil {
		return nil, fmt.Errorf("read the %s bootstrap parts: %w", tier, err)
	}
	parts := make(map[string]BootstrapPart, len(entries))
	for _, entry := range entries {
		rest, ok := entry.Key.Under(partsSegment)
		if !ok || len(rest) != 1 || len(entry.Value) == 0 {
			continue
		}
		part, err := decodeBootstrapPart(entry)
		if err != nil {
			return nil, err
		}
		parts[rest[0]] = part
	}
	return parts, nil
}

func ForgetBootstrapPart(ctx context.Context, store records.Store, tier environment.Tier, name string) error {
	if err := records.Forget(ctx, store, BootstrapPartKey(tier, name)); err != nil {
		return fmt.Errorf("forget the %s bootstrap part %s: %w", tier, name, err)
	}
	return nil
}

func decodeBootstrapPart(entry records.Entry) (BootstrapPart, error) {
	var part BootstrapPart
	if err := json.Unmarshal(entry.Value, &part); err != nil {
		return BootstrapPart{}, fmt.Errorf("read the bootstrap part %s: %w", entry.Key, err)
	}
	return part, nil
}

func (p BootstrapPart) CurrentFor(writtenBy, digest string) bool {
	return p.State == PartReady && (p.Digest == digest || provider.WrittenBy(p.WrittenBy).Newer(provider.WrittenBy(writtenBy)))
}

type PartWork struct {
	Name      string
	WrittenBy string
	Digest    string
	AddedBy   string
	AddedWith string
	Make      func(ctx context.Context) ([]provider.PartResource, error)
}

var ErrPartBehind = errors.New("the part is left behind this build")

var (
	partPoll        = time.Second
	partPollCeiling = 10 * time.Second
	partLease       = 15 * time.Minute
)

func EnsureBootstrapPart(ctx context.Context, store records.Store, tier environment.Tier, work PartWork) error {
	_, err := ensurePart(ctx, store, tier, work, false)
	return err
}

func RepairBootstrapParts(ctx context.Context, store records.Store, tier environment.Tier, works []PartWork) ([]string, error) {
	var repaired []string
	for _, work := range works {
		made, err := ensurePart(ctx, store, tier, work, true)
		if err != nil {
			return repaired, err
		}
		if made {
			repaired = append(repaired, work.Name)
		}
	}
	return repaired, nil
}

func ensurePart(ctx context.Context, store records.Store, tier environment.Tier, work PartWork, repairing bool) (bool, error) {
	key := BootstrapPartKey(tier, work.Name)
	waited := false
	for attempt := 0; ; attempt++ {
		recorded, err := records.ReadOrEmpty(ctx, store, key)
		if err != nil {
			return false, fmt.Errorf("read the %s bootstrap part %s: %w", tier, work.Name, err)
		}
		if repairing && len(recorded.Value) == 0 {
			return false, nil
		}
		now := time.Now().UTC()
		claim := BootstrapPart{State: PartCreating, WrittenBy: work.WrittenBy, Digest: work.Digest, AddedBy: work.AddedBy, AddedWith: work.AddedWith, AddedAt: now, ClaimedAt: now}
		var prior []byte
		if len(recorded.Value) > 0 {
			part, err := decodeBootstrapPart(recorded)
			if err != nil {
				return false, err
			}
			switch {
			case part.settledFor(work, repairing && !waited):
				return false, nil
			case part.claimed() && now.Sub(part.ClaimedAt) < partLease:
				if err := pausePart(ctx, attempt); err != nil {
					return false, fmt.Errorf("wait for the %s bootstrap part %s another run is adding: %w", tier, work.Name, err)
				}
				waited = true
				continue
			case part.State == PartReady:
				prior = recorded.Value
				claim = part
				claim.ClaimedAt = now
			}
			claim.AddedBy, claim.AddedWith, claim.AddedAt = part.AddedBy, part.AddedWith, part.AddedAt
		}
		if recorded.Value, err = json.Marshal(claim); err != nil {
			return false, fmt.Errorf("record the %s bootstrap part %s: %w", tier, work.Name, err)
		}
		revision, err := store.Write(ctx, recorded)
		if errors.Is(err, records.ErrStale) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("claim the %s bootstrap part %s: %w", tier, work.Name, err)
		}
		resources, err := work.Make(ctx)
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), partSettleTimeout)
		defer cancel()
		behind := errors.Is(err, ErrPartBehind)
		if err != nil && !behind {
			return false, errors.Join(err, releaseClaim(settle, store, records.Entry{Key: key, Value: prior, Revision: revision}))
		}
		digest := work.Digest
		if behind {
			digest = ""
		}
		return true, errors.Join(err, MarkBootstrapPartReady(settle, store, tier, work.Name, BootstrapPart{
			WrittenBy: work.WrittenBy,
			Digest:    digest,
			AddedBy:   claim.AddedBy,
			AddedWith: claim.AddedWith,
			AddedAt:   claim.AddedAt,
			Resources: resources,
		}))
	}
}

func (p BootstrapPart) claimed() bool {
	return p.State != PartReady || !p.ClaimedAt.IsZero()
}

func (p BootstrapPart) settledFor(work PartWork, remaking bool) bool {
	if remaking {
		return p.State == PartReady && provider.WrittenBy(p.WrittenBy).Newer(provider.WrittenBy(work.WrittenBy))
	}
	return p.CurrentFor(work.WrittenBy, work.Digest)
}

const partSettleTimeout = 30 * time.Second

func releaseClaim(ctx context.Context, store records.Store, claimed records.Entry) error {
	if len(claimed.Value) == 0 {
		err := store.Remove(ctx, claimed.Key, claimed.Revision)
		if err == nil || errors.Is(err, records.ErrNotFound) || errors.Is(err, records.ErrStale) {
			return nil
		}
		return fmt.Errorf("release the claim on %s: %w", claimed.Key, err)
	}
	if _, err := store.Write(ctx, claimed); err != nil && !errors.Is(err, records.ErrStale) {
		return fmt.Errorf("restore %s to what it recorded before this claim: %w", claimed.Key, err)
	}
	return nil
}

func pausePart(ctx context.Context, attempt int) error {
	delay := min(partPoll<<min(attempt, 8), partPollCeiling)
	timer := time.NewTimer(delay/2 + time.Duration(rand.Int64N(int64(delay))))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
