package stackrecords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
