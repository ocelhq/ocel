package variablestore_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func TestABindingReadTornByAPublishSixTimesOverIsStillResolved(t *testing.T) {
	t.Parallel()
	raced := &racedStore{Store: fake.NewKeyValues()}
	store := variablestore.Store{KeyValues: raced, Cipher: fake.NewCipher()}
	if _, err := store.SetBinding(context.Background(), racedScope, "", variablestore.OwnerOcel, "db", racedPair); err != nil {
		t.Fatal(err)
	}
	raced.mu.Lock()
	raced.tornLists = racingWriters
	raced.mu.Unlock()

	resolved, err := store.ResolveBindings(context.Background(), racedScope, "", []string{"db"})
	if err != nil || len(resolved) != 1 || string(resolved[0].Value) != `{"url":"postgres://db"}` {
		t.Fatalf("ResolveBindings() read torn %d times = %+v, %v, want the binding once a publish settles", racingWriters, resolved, err)
	}
}
