package stackrecords

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

type LeaseTerms struct {
	TTL   time.Duration
	Watch time.Duration
	Now   func() time.Time
	Wait  func(ctx context.Context, d time.Duration) error
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

const leaseWriteAttempts = 5

func TakeEnvironmentLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string, terms LeaseTerms) (held bool, err error) {
	name := EnvironmentLeaseKey(tier, slug, env)
	var (
		watching     bool
		watched      keyvalue.Revision
		watchedSince time.Time
		stale        int
	)
	for {
		recorded, err := keyvalue.ReadOrEmpty(ctx, store, name)
		if err != nil {
			return false, fmt.Errorf("read %s: %w", name, err)
		}
		current, err := decodeEnvironmentLease(name, recorded)
		if err != nil {
			return false, err
		}
		switch {
		case current.Token == "" || current.Token == token:
		case watching && recorded.Revision != watched:
			return false, refuseHeldLease(env, current)
		case !watching && terms.Now().Before(time.Unix(current.ExpiresAt, 0)):
			return false, refuseHeldLease(env, current)
		case !watching:
			watching, watched, watchedSince = true, recorded.Revision, terms.Now()
			if err := terms.Wait(ctx, min(terms.Watch, terms.TTL)); err != nil {
				return false, err
			}
			continue
		case terms.Now().Sub(watchedSince) < terms.TTL:
			if err := terms.Wait(ctx, min(terms.Watch, terms.TTL-terms.Now().Sub(watchedSince))); err != nil {
				return false, err
			}
			continue
		}
		err = writeEnvironmentLease(ctx, store, recorded, token, terms)
		if errors.Is(err, keyvalue.ErrStale) && stale < leaseWriteAttempts {
			stale++
			continue
		}
		if err != nil {
			return false, fmt.Errorf("record %s: %w", name, err)
		}
		return current.Token == token, nil
	}
}

func refuseHeldLease(env string, held EnvironmentLease) error {
	return refusal.Refuse(refusal.CodeBusy,
		"another deploy to %s is running: deploy again once it ends, or once its lease runs out at %s if it was interrupted",
		env, time.Unix(held.ExpiresAt, 0).UTC().Format(time.DateTime+" MST"))
}

func RenewEnvironmentLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string, terms LeaseTerms) error {
	name := EnvironmentLeaseKey(tier, slug, env)
	return keyvalue.Change(ctx, store, name, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		current, err := decodeEnvironmentLease(name, recorded)
		if err != nil {
			return nil, false, err
		}
		if current.Token == "" {
			return nil, false, refusal.Refuse(refusal.CodeBusy,
				"the lease this deploy held on %s ran out and was freed, so another deploy may have changed %s since: deploy again",
				env, env)
		}
		if current.Token != token {
			return nil, false, refusal.Refuse(refusal.CodeBusy,
				"another deploy to %s is running, and took over the lease this deploy held, so this deploy stopped: deploy again once that deploy ends",
				env)
		}
		value, err := encodeEnvironmentLease(name, token, terms)
		return value, err == nil, err
	})
}

func ForgetEnvironmentLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string) error {
	name := EnvironmentLeaseKey(tier, slug, env)
	return keyvalue.ForgetMatching(ctx, store, name, func(recorded keyvalue.Entry) (bool, error) {
		held, err := decodeEnvironmentLease(name, recorded)
		return held.Token == token, err
	})
}

func writeEnvironmentLease(ctx context.Context, store keyvalue.Store, recorded keyvalue.Entry, token string, terms LeaseTerms) error {
	value, err := encodeEnvironmentLease(recorded.Key, token, terms)
	if err != nil {
		return err
	}
	recorded.Value = value
	_, err = store.Write(ctx, recorded)
	return err
}

func encodeEnvironmentLease(name keyvalue.Key, token string, terms LeaseTerms) ([]byte, error) {
	value, err := json.Marshal(EnvironmentLease{Token: token, ExpiresAt: terms.Now().Add(terms.TTL).Unix()})
	if err != nil {
		return nil, fmt.Errorf("record %s: %w", name, err)
	}
	return value, nil
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
