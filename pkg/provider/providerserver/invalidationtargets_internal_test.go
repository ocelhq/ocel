package providerserver

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type writesUntilRefused struct {
	keyvalue.Store
	left int
}

var errWriteRefused = errors.New("the store refused the write")

func (s *writesUntilRefused) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if s.left == 0 {
		return "", errWriteRefused
	}
	s.left--
	return s.Store.Write(ctx, entry)
}

func (s *writesUntilRefused) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	if s.left == 0 {
		return errWriteRefused
	}
	s.left--
	return s.Store.Remove(ctx, key, expected)
}

func TestMovingTheInvalidationTargetIsOneWriteThatLeavesATargetEitherWay(t *testing.T) {
	const old, moved = "https://shop.old.workers.dev/purge", "https://shop.new.workers.dev/purge"
	backing := fake.NewKeyValues()
	if err := recordInvalidationTarget(t.Context(), backing, environment.TierProduction, "relay", "shop", "", old); err != nil {
		t.Fatalf("record the first target: %v", err)
	}
	store := &writesUntilRefused{Store: backing, left: 1}

	_ = recordInvalidationTarget(t.Context(), store, environment.TierProduction, "relay", "shop", old, moved)

	entry, err := keyvalue.ReadOrEmpty(t.Context(), backing, buildInvalidationTargetsKey(environment.TierProduction, "relay", "shop"))
	if err != nil {
		t.Fatalf("read the targets: %v", err)
	}
	targets, err := decodeInvalidationTargets(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(targets, []string{moved}) && !slices.Equal(targets, []string{old}) {
		t.Errorf("invalidation targets = %v, want the move made in the one write the store took, or not at all", targets)
	}
}
