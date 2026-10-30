package endpoint

import (
	"context"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"golang.org/x/sync/singleflight"
)

const (
	refetchFloor = 5 * time.Second
	fetchTimeout = 10 * time.Second
)

type Keyring struct {
	url     string
	fetches singleflight.Group

	mu          sync.Mutex
	set         jwk.Set
	attemptedAt time.Time
	failure     error
}

func NewKeyring(url string) *Keyring { return &Keyring{url: url} }

func (k *Keyring) Read(ctx context.Context, kid string) (jwk.Set, error) {
	k.mu.Lock()
	set, attemptedAt, failure := k.set, k.attemptedAt, k.failure
	k.mu.Unlock()
	if set != nil {
		if _, found := set.LookupKeyID(kid); found {
			return set, nil
		}
	}
	if !attemptedAt.IsZero() && time.Since(attemptedAt) < refetchFloor {
		switch {
		case failure != nil:
			return nil, failure
		case set != nil:
			return set, nil
		}
	}
	fetched := k.fetches.DoChan(k.url, func() (any, error) { return k.fetch(ctx) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-fetched:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(jwk.Set), nil
	}
}

func (k *Keyring) fetch(ctx context.Context) (jwk.Set, error) {
	k.mu.Lock()
	if !k.attemptedAt.IsZero() && time.Since(k.attemptedAt) < refetchFloor && (k.set != nil || k.failure != nil) {
		set, failure := k.set, k.failure
		k.mu.Unlock()
		return set, failure
	}
	k.mu.Unlock()
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	fetched, err := jwk.Fetch(bounded, k.url)
	k.mu.Lock()
	defer k.mu.Unlock()
	k.attemptedAt = time.Now()
	if err != nil {
		k.failure = err
		return nil, err
	}
	k.set, k.failure = fetched, nil
	return fetched, nil
}
