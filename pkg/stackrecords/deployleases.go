package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type DeployLease struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

func DeployLeasesPartition(tier environment.Tier, slug string) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootDeployLeases, Path: []string{slug}}
}

func DeployLeaseKey(tier environment.Tier, slug, env string) keyvalue.Key {
	return DeployLeasesPartition(tier, slug).Key(env)
}

func TakeDeployLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string, now time.Time, ttl time.Duration) error {
	name := DeployLeaseKey(tier, slug, env)
	return keyvalue.Change(ctx, store, name, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		held, err := decodeDeployLease(name, recorded)
		if err != nil {
			return nil, false, err
		}
		if held.Token != "" && held.Token != token && now.Before(time.Unix(held.ExpiresAt, 0)) {
			return nil, false, refusal.Refuse(refusal.CodeBusy,
				"another deploy to %s is running: deploy again once it ends, or once its lease runs out at %s if it was interrupted",
				env, time.Unix(held.ExpiresAt, 0).UTC().Format(time.DateTime+" MST"))
		}
		value, err := json.Marshal(DeployLease{Token: token, ExpiresAt: now.Add(ttl).Unix()})
		if err != nil {
			return nil, false, fmt.Errorf("record %s: %w", name, err)
		}
		return value, true, nil
	})
}

func RenewDeployLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string, now time.Time, ttl time.Duration) error {
	name := DeployLeaseKey(tier, slug, env)
	return keyvalue.Change(ctx, store, name, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		held, err := decodeDeployLease(name, recorded)
		if err != nil {
			return nil, false, err
		}
		if held.Token != token {
			return nil, false, refusal.Refuse(refusal.CodeBusy,
				"another deploy to %s is running, and took over the lease this deploy held, so this deploy stops before it promotes: deploy again once that deploy ends",
				env)
		}
		value, err := json.Marshal(DeployLease{Token: token, ExpiresAt: now.Add(ttl).Unix()})
		if err != nil {
			return nil, false, fmt.Errorf("record %s: %w", name, err)
		}
		return value, true, nil
	})
}

func ForgetDeployLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string) error {
	name := DeployLeaseKey(tier, slug, env)
	return keyvalue.ForgetMatching(ctx, store, name, func(recorded keyvalue.Entry) (bool, error) {
		held, err := decodeDeployLease(name, recorded)
		return held.Token == token, err
	})
}

func decodeDeployLease(name keyvalue.Key, recorded keyvalue.Entry) (DeployLease, error) {
	var held DeployLease
	if len(recorded.Value) == 0 {
		return held, nil
	}
	if err := json.Unmarshal(recorded.Value, &held); err != nil {
		return DeployLease{}, fmt.Errorf("read %s: %w", name, err)
	}
	return held, nil
}
