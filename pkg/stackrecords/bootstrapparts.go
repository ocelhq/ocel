package stackrecords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
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
	AddedAt   time.Time               `json:"added_at"`
	Resources []provider.PartResource `json:"resources,omitempty"`
}

func ClaimBootstrapPart(ctx context.Context, store keyvalue.Store, tier environment.Tier, name string, claim BootstrapPart) (BootstrapPart, bool, error) {
	claim.State = PartCreating
	value, err := json.Marshal(claim)
	if err != nil {
		return BootstrapPart{}, false, fmt.Errorf("record the %s bootstrap part %s: %w", tier, name, err)
	}
	for range claimAttempts {
		_, err = store.Write(ctx, keyvalue.Entry{Key: BootstrapPartKey(tier, name), Value: value})
		if err == nil {
			return claim, true, nil
		}
		if !errors.Is(err, keyvalue.ErrStale) {
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

func ReleaseBootstrapPart(ctx context.Context, store keyvalue.Store, tier environment.Tier, name string, claim BootstrapPart) error {
	err := keyvalue.ForgetMatching(ctx, store, BootstrapPartKey(tier, name), func(recorded keyvalue.Entry) (bool, error) {
		part, err := decodeBootstrapPart(recorded)
		return err == nil && part.State == PartCreating && part.WrittenBy == claim.WrittenBy && part.AddedAt.Equal(claim.AddedAt), err
	})
	if err != nil {
		return fmt.Errorf("release the claim on the %s bootstrap part %s: %w", tier, name, err)
	}
	return nil
}

func MarkBootstrapPartReady(ctx context.Context, store keyvalue.Store, tier environment.Tier, name string, ready BootstrapPart) error {
	return keyvalue.Change(ctx, store, BootstrapPartKey(tier, name), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		part := ready
		if len(recorded.Value) > 0 {
			prior, err := decodeBootstrapPart(recorded)
			if err != nil {
				return nil, false, err
			}
			if provider.WrittenBy(prior.WrittenBy).Newer(provider.WrittenBy(ready.WrittenBy)) {
				return nil, false, nil
			}
			part.AddedBy, part.AddedAt = prior.AddedBy, prior.AddedAt
		}
		part.State = PartReady
		value, err := json.Marshal(part)
		if err != nil {
			return nil, false, fmt.Errorf("record the %s bootstrap part %s: %w", tier, name, err)
		}
		return value, true, nil
	})
}

func ReadBootstrapPart(ctx context.Context, store keyvalue.Store, tier environment.Tier, name string) (BootstrapPart, bool, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, BootstrapPartKey(tier, name))
	if err != nil {
		return BootstrapPart{}, false, fmt.Errorf("read the %s bootstrap part %s: %w", tier, name, err)
	}
	if len(recorded.Value) == 0 {
		return BootstrapPart{}, false, nil
	}
	part, err := decodeBootstrapPart(recorded)
	return part, err == nil, err
}

func ListBootstrapParts(ctx context.Context, store keyvalue.Store, tier environment.Tier) (map[string]BootstrapPart, error) {
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

func ForgetBootstrapPart(ctx context.Context, store keyvalue.Store, tier environment.Tier, name string) error {
	if err := keyvalue.Forget(ctx, store, BootstrapPartKey(tier, name)); err != nil {
		return fmt.Errorf("forget the %s bootstrap part %s: %w", tier, name, err)
	}
	return nil
}

func decodeBootstrapPart(entry keyvalue.Entry) (BootstrapPart, error) {
	var part BootstrapPart
	if err := json.Unmarshal(entry.Value, &part); err != nil {
		return BootstrapPart{}, fmt.Errorf("read the bootstrap part %s: %w", entry.Key, err)
	}
	return part, nil
}

type PartWork struct {
	Name      string
	WrittenBy string
	Digest    string
	AddedBy   string
	Make      func(ctx context.Context) ([]provider.PartResource, error)
}

var (
	partPoll        = time.Second
	partPollCeiling = 10 * time.Second
	partLease       = 15 * time.Minute
)

func EnsureBootstrapPart(ctx context.Context, store keyvalue.Store, tier environment.Tier, work PartWork) error {
	key := BootstrapPartKey(tier, work.Name)
	var seen keyvalue.Revision
	var since time.Time
	for attempt := 0; ; attempt++ {
		recorded, err := keyvalue.ReadOrEmpty(ctx, store, key)
		if err != nil {
			return fmt.Errorf("read the %s bootstrap part %s: %w", tier, work.Name, err)
		}
		claim := BootstrapPart{State: PartCreating, WrittenBy: work.WrittenBy, Digest: work.Digest, AddedBy: work.AddedBy, AddedAt: time.Now().UTC()}
		var prior []byte
		if len(recorded.Value) > 0 {
			part, err := decodeBootstrapPart(recorded)
			if err != nil {
				return err
			}
			switch {
			case part.State == PartReady && (part.Digest == work.Digest || provider.WrittenBy(part.WrittenBy).Newer(provider.WrittenBy(work.WrittenBy))):
				return nil
			case part.State != PartReady:
				if recorded.Revision != seen {
					seen, since = recorded.Revision, time.Now()
				}
				if time.Since(since) < partLease {
					if err := pausePart(ctx, attempt); err != nil {
						return fmt.Errorf("wait for the %s bootstrap part %s another run is adding: %w", tier, work.Name, err)
					}
					continue
				}
			default:
				prior = recorded.Value
			}
			claim.AddedBy, claim.AddedAt = part.AddedBy, part.AddedAt
		}
		if recorded.Value, err = json.Marshal(claim); err != nil {
			return fmt.Errorf("record the %s bootstrap part %s: %w", tier, work.Name, err)
		}
		revision, err := store.Write(ctx, recorded)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return fmt.Errorf("claim the %s bootstrap part %s: %w", tier, work.Name, err)
		}
		resources, err := work.Make(ctx)
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), partSettleTimeout)
		defer cancel()
		if err != nil {
			return errors.Join(err, releaseClaim(settle, store, keyvalue.Entry{Key: key, Value: prior, Revision: revision}))
		}
		claim.Resources = resources
		return MarkBootstrapPartReady(settle, store, tier, work.Name, claim)
	}
}

const partSettleTimeout = 30 * time.Second

func releaseClaim(ctx context.Context, store keyvalue.Store, claimed keyvalue.Entry) error {
	if len(claimed.Value) == 0 {
		err := store.Remove(ctx, claimed.Key, claimed.Revision)
		if err == nil || errors.Is(err, keyvalue.ErrNotFound) || errors.Is(err, keyvalue.ErrStale) {
			return nil
		}
		return fmt.Errorf("release the claim on %s: %w", claimed.Key, err)
	}
	if _, err := store.Write(ctx, claimed); err != nil && !errors.Is(err, keyvalue.ErrStale) {
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
