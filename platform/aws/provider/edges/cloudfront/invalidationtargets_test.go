package cloudfront

import (
	"context"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type contendedKeyValues struct {
	keyvalue.Store
	beaten int
}

func (c *contendedKeyValues) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if c.beaten > 0 {
		c.beaten--
		return "", keyvalue.ErrStale
	}
	return c.Store.Write(ctx, entry)
}

func TestAnInvalidationTargetAnotherDeployKeepsBeatingWaitsLongerBeforeEachWrite(t *testing.T) {
	t.Parallel()

	var waited []time.Duration
	targets := invalidationTargets{
		keyValues: &contendedKeyValues{Store: fake.NewKeyValues(), beaten: 3},
		partition: newInvalidationPartition(environment.TierProduction, "shop"),
		wait: func(_ context.Context, delay time.Duration) error {
			waited = append(waited, delay)
			return nil
		},
	}

	if err := targets.add(context.Background(), "E123"); err != nil {
		t.Fatalf("add after three lost writes = %v, want it recorded", err)
	}
	if len(waited) != 3 {
		t.Fatalf("waited %v, want a wait before each of the three writes after a lost one", waited)
	}
	for i := 1; i < len(waited); i++ {
		if waited[i] <= waited[i-1] {
			t.Errorf("waited %v, want each wait longer than the last", waited)
		}
	}
}

func TestAnInvalidationTargetIsRemovedWithoutTouchingTheOthers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	targets := invalidationTargets{keyValues: store, partition: newInvalidationPartition(environment.TierProduction, "shop")}
	for _, distribution := range []string{"E2", "E1"} {
		if err := targets.add(ctx, distribution); err != nil {
			t.Fatalf("add(%s) = %v", distribution, err)
		}
	}
	if err := targets.remove(ctx, "E2"); err != nil {
		t.Fatalf("remove(E2) = %v", err)
	}

	recorded, err := keyvalue.ReadOrEmpty(ctx, store, targets.partition.Key("invalidation"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(recorded.Value); got != `["E1"]` {
		t.Errorf("the targets read %s, want only E1 left", got)
	}
}
