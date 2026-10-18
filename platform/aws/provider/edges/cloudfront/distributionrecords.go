package cloudfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
)

type distributionRecords struct {
	keyValues keyvalue.Store
	tier      environment.Tier
}

type recordedFront struct {
	ID         string `json:"id"`
	DomainName string `json:"domainName"`
}

func (r distributionRecords) key(name string) keyvalue.Key {
	return keyvalue.Partition{Tier: r.tier, Root: keyvalue.RootRouters, Path: []string{string(Kind)}}.Key("distribution", name)
}

func (r distributionRecords) find(ctx context.Context, name string) (front, bool, error) {
	entry, err := keyvalue.ReadOrEmpty(ctx, r.keyValues, r.key(name))
	if err != nil {
		return front{}, false, fmt.Errorf("read the distribution recorded for %s: %w", name, err)
	}
	if len(entry.Value) == 0 {
		return front{}, false, nil
	}
	var recorded recordedFront
	if err := json.Unmarshal(entry.Value, &recorded); err != nil || recorded.ID == "" {
		return front{}, false, fmt.Errorf("read the distribution recorded for %s: %s is no distribution record", name, entry.Value)
	}
	return front{id: recorded.ID, domainName: recorded.DomainName}, true, nil
}

func (r distributionRecords) record(ctx context.Context, name string, served front) error {
	encoded, err := json.Marshal(recordedFront{ID: served.id, DomainName: served.domainName})
	if err != nil {
		return err
	}
	for range invalidationAttempts {
		entry, err := keyvalue.ReadOrEmpty(ctx, r.keyValues, r.key(name))
		if err != nil {
			return fmt.Errorf("record distribution %s for %s: %w", served.id, name, err)
		}
		if string(entry.Value) == string(encoded) {
			return nil
		}
		entry.Value = encoded
		if _, err := r.keyValues.Write(ctx, entry); err != nil {
			if errors.Is(err, keyvalue.ErrStale) {
				continue
			}
			return fmt.Errorf("record distribution %s for %s: %w", served.id, name, err)
		}
		return nil
	}
	return fmt.Errorf("record distribution %s for %s: the record moved under %d attempts", served.id, name, invalidationAttempts)
}

func (r distributionRecords) forget(ctx context.Context, name string) error {
	if err := keyvalue.Forget(ctx, r.keyValues, r.key(name)); err != nil {
		return fmt.Errorf("forget the distribution recorded for %s: %w", name, err)
	}
	return nil
}

func raiseRecordedDistribution(ctx context.Context, c Clients, records distributionRecords, spec distributionSpec, aliases []string, certificate string) (raised front, adopted bool, err error) {
	raised, err = createDistribution(ctx, c, spec, aliases, certificate)
	if distributionTaken(err) {
		existing, found, findErr := findDistribution(ctx, c, spec.name)
		if findErr != nil {
			return front{}, false, findErr
		}
		if !found {
			return front{}, false, err
		}
		raised, adopted, err = existing, true, nil
	}
	if err != nil {
		return front{}, false, err
	}
	if err := records.record(ctx, spec.name, raised); err != nil {
		return front{}, false, err
	}
	return raised, adopted, nil
}
