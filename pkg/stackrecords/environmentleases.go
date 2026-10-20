package stackrecords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type EnvironmentLease struct {
	Token     string      `json:"token"`
	Holder    LeaseHolder `json:"holder"`
	ExpiresAt int64       `json:"expiresAt"`
}

type LeaseHolder string

const EnvironmentLeaseTTL = 5 * time.Minute

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

func EnvironmentLeasesPartition(tier environment.Tier, slug string) records.Partition {
	return records.Partition{Tier: tier, Root: RootEnvironmentLeases, Path: []string{slug}}
}

func EnvironmentLeaseKey(tier environment.Tier, slug, env string) records.Key {
	return EnvironmentLeasesPartition(tier, slug).Key(env)
}

const (
	leaseWriteAttempts  = 5
	leaseCleanupTimeout = 30 * time.Second
)

func ProjectLeaseKey(tier environment.Tier, slug string) records.Key {
	return records.Partition{Tier: tier, Root: RootProjectLeases}.Key(slug)
}

func describeProject(tier environment.Tier, slug string) string {
	return fmt.Sprintf("%s in %s", slug, tier)
}

func TakeEnvironmentLease(ctx context.Context, store records.Store, tier environment.Tier, slug, env, token string, holder LeaseHolder, terms LeaseTerms) (held bool, err error) {
	if err := forgetStoppedProjectLease(ctx, store, tier, slug, token, holder, terms); err != nil {
		return false, err
	}
	name := EnvironmentLeaseKey(tier, slug, env)
	held, err = takeLease(ctx, store, name, env, token, holder, terms)
	if err != nil {
		return false, err
	}
	if err := refuseHeldProject(ctx, store, tier, slug, token, holder); err != nil {
		if !held {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseCleanupTimeout)
			defer cancel()
			err = errors.Join(err, forgetLease(cleanup, store, name, token))
		}
		return false, err
	}
	return held, nil
}

func readLease(ctx context.Context, store records.Store, name records.Key) (records.Entry, EnvironmentLease, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return records.Entry{}, EnvironmentLease{}, fmt.Errorf("read %s: %w", name, err)
	}
	current, err := decodeEnvironmentLease(name, recorded)
	return recorded, current, err
}

func forgetStoppedProjectLease(ctx context.Context, store records.Store, tier environment.Tier, slug, token string, holder LeaseHolder, terms LeaseTerms) error {
	name := ProjectLeaseKey(tier, slug)
	recorded, current, err := readLease(ctx, store, name)
	if err != nil {
		return err
	}
	if current.Token == "" || current.Token == token {
		return nil
	}
	if terms.Now().Before(time.Unix(current.ExpiresAt, 0)) {
		return refuseHeldLease(describeProject(tier, slug), current, holder)
	}
	watched, watchedSince := recorded.Revision, terms.Now()
	for elapsed := time.Duration(0); elapsed < terms.TTL; elapsed = terms.Now().Sub(watchedSince) {
		if err := terms.Wait(ctx, min(terms.Watch, terms.TTL-elapsed)); err != nil {
			return err
		}
		if recorded, current, err = readLease(ctx, store, name); err != nil {
			return err
		}
		if current.Token == "" {
			return nil
		}
		if recorded.Revision != watched {
			return refuseHeldLease(describeProject(tier, slug), current, holder)
		}
	}
	return records.ForgetMatching(ctx, store, name, func(recorded records.Entry) (bool, error) {
		return recorded.Revision == watched, nil
	})
}

func refuseHeldProject(ctx context.Context, store records.Store, tier environment.Tier, slug, token string, holder LeaseHolder) error {
	_, current, err := readLease(ctx, store, ProjectLeaseKey(tier, slug))
	if err != nil {
		return err
	}
	if current.Token == "" || current.Token == token {
		return nil
	}
	return refuseHeldLease(describeProject(tier, slug), current, holder)
}

func TakeProjectLease(ctx context.Context, store records.Store, tier environment.Tier, slug, token string, holder LeaseHolder, terms LeaseTerms) (held bool, err error) {
	return takeLease(ctx, store, ProjectLeaseKey(tier, slug), describeProject(tier, slug), token, holder, terms)
}

func RenewProjectLease(ctx context.Context, store records.Store, tier environment.Tier, slug, token string, holder LeaseHolder, terms LeaseTerms) error {
	return renewLease(ctx, store, ProjectLeaseKey(tier, slug), describeProject(tier, slug), token, holder, terms)
}

func ForgetProjectLease(ctx context.Context, store records.Store, tier environment.Tier, slug, token string) error {
	return forgetLease(ctx, store, ProjectLeaseKey(tier, slug), token)
}

func takeLease(ctx context.Context, store records.Store, name records.Key, env, token string, holder LeaseHolder, terms LeaseTerms) (held bool, err error) {
	var (
		watching     bool
		watched      records.Revision
		watchedSince time.Time
		stale        int
	)
	for {
		recorded, err := records.ReadOrEmpty(ctx, store, name)
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
		if errors.Is(err, records.ErrStale) && stale < leaseWriteAttempts {
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

func RenewEnvironmentLease(ctx context.Context, store records.Store, tier environment.Tier, slug, env, token string, holder LeaseHolder, terms LeaseTerms) error {
	return renewLease(ctx, store, EnvironmentLeaseKey(tier, slug, env), env, token, holder, terms)
}

func renewLease(ctx context.Context, store records.Store, name records.Key, env, token string, holder LeaseHolder, terms LeaseTerms) error {
	return records.Change(ctx, store, name, func(recorded records.Entry) ([]byte, bool, error) {
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

func ListEnvironmentLeases(ctx context.Context, store records.Store, tier environment.Tier, slug string) (map[string]EnvironmentLease, error) {
	recorded, err := store.List(ctx, EnvironmentLeasesPartition(tier, slug))
	if err != nil {
		return nil, fmt.Errorf("read %s's environment leases: %w", slug, err)
	}
	leases := make(map[string]EnvironmentLease, len(recorded))
	for _, entry := range recorded {
		lease, err := decodeEnvironmentLease(entry.Key, entry)
		if err != nil {
			return nil, err
		}
		leases[entry.Key.Path[0]] = lease
	}
	return leases, nil
}

func ForgetEnvironmentLease(ctx context.Context, store records.Store, tier environment.Tier, slug, env, token string) error {
	return forgetLease(ctx, store, EnvironmentLeaseKey(tier, slug, env), token)
}

func forgetLease(ctx context.Context, store records.Store, name records.Key, token string) error {
	return records.ForgetMatching(ctx, store, name, func(recorded records.Entry) (bool, error) {
		held, err := decodeEnvironmentLease(name, recorded)
		return held.Token == token, err
	})
}

func writeEnvironmentLease(ctx context.Context, store records.Store, recorded records.Entry, token string, holder LeaseHolder, terms LeaseTerms) error {
	value, err := encodeEnvironmentLease(recorded.Key, token, holder, terms)
	if err != nil {
		return err
	}
	recorded.Value = value
	_, err = store.Write(ctx, recorded)
	return err
}

func encodeEnvironmentLease(name records.Key, token string, holder LeaseHolder, terms LeaseTerms) ([]byte, error) {
	value, err := json.Marshal(EnvironmentLease{Token: token, Holder: holder, ExpiresAt: terms.Now().Add(terms.TTL).Unix()})
	if err != nil {
		return nil, fmt.Errorf("record %s: %w", name, err)
	}
	return value, nil
}

func decodeEnvironmentLease(name records.Key, recorded records.Entry) (EnvironmentLease, error) {
	var held EnvironmentLease
	if len(recorded.Value) == 0 {
		return held, nil
	}
	if err := json.Unmarshal(recorded.Value, &held); err != nil {
		return EnvironmentLease{}, fmt.Errorf("read %s: %w", name, err)
	}
	return held, nil
}
