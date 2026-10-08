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
	Token     string      `json:"token"`
	Holder    LeaseHolder `json:"holder"`
	ExpiresAt int64       `json:"expiresAt"`
}

type LeaseHolder string

const (
	LeaseDeploy   LeaseHolder = "deploy"
	LeaseRollback LeaseHolder = "rollback"
	LeaseRemoval  LeaseHolder = "removal"
)

func (h LeaseHolder) describe(env string, beside LeaseHolder) string {
	article := "a"
	if h == beside {
		article = "another"
	}
	switch h {
	case LeaseRollback:
		return article + " rollback of " + env
	case LeaseRemoval:
		return article + " removal of " + env
	default:
		return article + " deploy to " + env
	}
}

func (h LeaseHolder) describeRetry() string {
	switch h {
	case LeaseRollback:
		return "roll back"
	case LeaseRemoval:
		return "remove it"
	default:
		return "deploy"
	}
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

func TakeEnvironmentLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string, holder LeaseHolder, terms LeaseTerms) (held bool, err error) {
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
			return false, refuseHeldLease(env, current, holder)
		case !watching && terms.Now().Before(time.Unix(current.ExpiresAt, 0)):
			return false, refuseHeldLease(env, current, holder)
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
		err = writeEnvironmentLease(ctx, store, recorded, token, holder, terms)
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

func refuseHeldLease(env string, held EnvironmentLease, taker LeaseHolder) error {
	return refusal.Refuse(refusal.CodeBusy,
		"%s is running: %s again once it ends, or once its lease runs out at %s if it was interrupted",
		held.Holder.describe(env, taker), taker.describeRetry(), time.Unix(held.ExpiresAt, 0).UTC().Format(time.DateTime+" MST"))
}

func RenewEnvironmentLease(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, env, token string, holder LeaseHolder, terms LeaseTerms) error {
	name := EnvironmentLeaseKey(tier, slug, env)
	return keyvalue.Change(ctx, store, name, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		current, err := decodeEnvironmentLease(name, recorded)
		if err != nil {
			return nil, false, err
		}
		if current.Token == "" {
			return nil, false, refusal.Refuse(refusal.CodeBusy,
				"this %s holds no lease on %s: it ran out and was freed, or was never taken, so something else may have changed %s since: %s again",
				holder, env, env, holder.describeRetry())
		}
		if current.Token != token {
			return nil, false, refusal.Refuse(refusal.CodeBusy,
				"%s is running, and took over the lease this %s held, so this %s stopped: %s again once it ends",
				current.Holder.describe(env, holder), holder, holder, holder.describeRetry())
		}
		value, err := encodeEnvironmentLease(name, token, holder, terms)
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

func writeEnvironmentLease(ctx context.Context, store keyvalue.Store, recorded keyvalue.Entry, token string, holder LeaseHolder, terms LeaseTerms) error {
	value, err := encodeEnvironmentLease(recorded.Key, token, holder, terms)
	if err != nil {
		return err
	}
	recorded.Value = value
	_, err = store.Write(ctx, recorded)
	return err
}

func encodeEnvironmentLease(name keyvalue.Key, token string, holder LeaseHolder, terms LeaseTerms) ([]byte, error) {
	value, err := json.Marshal(EnvironmentLease{Token: token, Holder: holder, ExpiresAt: terms.Now().Add(terms.TTL).Unix()})
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
