package variablestore_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const racingWriters = 6

type racedStore struct {
	records.Store

	mu         sync.Mutex
	racedIndex int
	tornLists  int
}

func (r *racedStore) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	r.mu.Lock()
	raced := slices.Contains(entry.Key.Path, "bindingowners") && r.racedIndex > 0
	if raced {
		r.racedIndex--
	}
	r.mu.Unlock()
	if raced {
		return "", records.ErrStale
	}
	return r.Store.Write(ctx, entry)
}

func (r *racedStore) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	listed, err := r.Store.List(ctx, in, under...)
	r.mu.Lock()
	torn := r.tornLists > 0
	if torn {
		r.tornLists--
	}
	r.mu.Unlock()
	if !torn {
		return listed, err
	}
	return slices.DeleteFunc(listed, func(entry records.Entry) bool { return slices.Contains(entry.Key.Path, "values") }), err
}

var racedScope = variablestore.Scope{Project: "shop", Tier: environment.TierProduction}

var racedPair = variablestore.BindingWrite{Record: []byte(`{"name":"db"}`), Value: []byte(`{"url":"postgres://db"}`)}

func TestABindingWhoseOwnerIndexSixWritersRaceIsStillPublished(t *testing.T) {
	t.Parallel()
	store := variablestore.Store{KeyValues: &racedStore{Store: fake.NewKeyValues(), racedIndex: racingWriters}, Cipher: fake.NewCipher()}

	if _, err := store.SetBinding(context.Background(), racedScope, "", variablestore.OwnerOcel, "db", racedPair); err != nil {
		t.Fatalf("SetBinding() whose owner index %d writers raced = %v, want the binding published: parallel deploys of one environment race to it", racingWriters, err)
	}
}
