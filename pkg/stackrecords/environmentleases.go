package stackrecords

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type EnvironmentLease struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

func NewEnvironmentLeaseToken() (string, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("mint an environment lease token: %w", err)
	}
	return hex.EncodeToString(token), nil
}

func EnvironmentLeasesPartition(tier environment.Tier, slug string) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootEnvironmentLeases, Path: []string{slug}}
}

func EnvironmentLeaseKey(tier environment.Tier, slug, env string) keyvalue.Key {
	return EnvironmentLeasesPartition(tier, slug).Key(env)
}

func TakeEnvironmentLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string, now time.Time, ttl time.Duration) error {
	name := EnvironmentLeaseKey(tier, slug, env)
	return keyvalue.Change(ctx, store, name, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		held, err := decodeEnvironmentLease(name, recorded)
		if err != nil {
			return nil, false, err
		}
		if held.Token != "" && held.Token != token && now.Before(time.Unix(held.ExpiresAt, 0)) {
			return nil, false, refusal.Refuse(refusal.CodeBusy,
				"another deploy to %s is running: deploy again once it ends, or once its lease runs out at %s if it was interrupted",
				env, time.Unix(held.ExpiresAt, 0).UTC().Format(time.DateTime+" MST"))
		}
		value, err := json.Marshal(EnvironmentLease{Token: token, ExpiresAt: now.Add(ttl).Unix()})
		if err != nil {
			return nil, false, fmt.Errorf("record %s: %w", name, err)
		}
		return value, true, nil
	})
}

func RenewEnvironmentLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string, now time.Time, ttl time.Duration) error {
	name := EnvironmentLeaseKey(tier, slug, env)
	return keyvalue.Change(ctx, store, name, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		held, err := decodeEnvironmentLease(name, recorded)
		if err != nil {
			return nil, false, err
		}
		if held.Token != token {
			return nil, false, refusal.Refuse(refusal.CodeBusy,
				"another deploy to %s is running, and took over the lease this deploy held, so this deploy stops before it promotes: deploy again once that deploy ends",
				env)
		}
		value, err := json.Marshal(EnvironmentLease{Token: token, ExpiresAt: now.Add(ttl).Unix()})
		if err != nil {
			return nil, false, fmt.Errorf("record %s: %w", name, err)
		}
		return value, true, nil
	})
}

func ForgetEnvironmentLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string) error {
	name := EnvironmentLeaseKey(tier, slug, env)
	return keyvalue.ForgetMatching(ctx, store, name, func(recorded keyvalue.Entry) (bool, error) {
		held, err := decodeEnvironmentLease(name, recorded)
		return held.Token == token, err
	})
}

func decodeEnvironmentLease(name keyvalue.Key, recorded keyvalue.Entry) (EnvironmentLease, error) {
	var held EnvironmentLease
	if len(recorded.Value) == 0 {
		return held, nil
	}
	if err := json.Unmarshal(recorded.Value, &held); err != nil {
		return EnvironmentLease{}, fmt.Errorf("read %s: %w", name, err)
	}
	return held, nil
}
