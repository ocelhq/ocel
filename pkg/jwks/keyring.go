package jwks

import (
	"context"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
)

const refetchFloor = 5 * time.Second

type Keyring struct {
	url string

	mu        sync.Mutex
	set       jwk.Set
	fetchedAt time.Time
}

func NewKeyring(url string) *Keyring { return &Keyring{url: url} }

func (k *Keyring) Read(ctx context.Context, kid string) (jwk.Set, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.set != nil {
		if _, found := k.set.LookupKeyID(kid); found {
			return k.set, nil
		}
		if time.Since(k.fetchedAt) < refetchFloor {
			return k.set, nil
		}
	}

	fetched, err := jwk.Fetch(ctx, k.url)
	if err != nil {
		return nil, err
	}
	k.set = fetched
	k.fetchedAt = time.Now()
	return fetched, nil
}
