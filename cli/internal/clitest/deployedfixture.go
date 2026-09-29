package clitest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func RecordEdgeStack(t *testing.T, project FakeProject, tier environment.Tier, kind edge.Kind) {
	t.Helper()

	ctx := context.Background()
	store := project.Provider.KeyValues()
	name := stackrecords.EdgeStackKey(tier, FixtureSlug)
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	state := stackrecords.EdgeState{Kind: kind, Edge: edge.StackState{
		Slug:     FixtureSlug,
		Tier:     tier,
		Endpoint: "https://" + FixtureSlug + ".fake.invalid",
	}}
	if recorded.Value, err = json.Marshal(state); err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	if _, err := store.Write(ctx, recorded); err != nil {
		t.Fatalf("record %s: %v", name, err)
	}
}

func RecordStacks(t *testing.T, project FakeProject, tier environment.Tier, stacks ...naming.StackName) {
	t.Helper()

	for _, stack := range stacks {
		recorded := stackrecords.Stack{Kind: provider.StackApp, App: stack.App, Release: stack.Release.String()}
		if stack.IsInfra() {
			recorded = stackrecords.Stack{Kind: provider.StackInfra}
		}
		if err := stackrecords.Write(context.Background(), project.Provider.KeyValues(), tier, FixtureSlug, stack, recorded); err != nil {
			t.Fatalf("record stack %s: %v", stack, err)
		}
	}
}

func RecordPromotions(t *testing.T, project FakeProject, promotionIDs ...string) {
	t.Helper()

	RecordEdgeStack(t, project, environment.TierProduction, fake.KindRelay)
	ctx := context.Background()
	releases := ledger.New(project.Provider.KeyValues(), environment.TierProduction, FixtureSlug)
	replaces := ""
	for i, id := range promotionIDs {
		build := fmt.Sprintf("%032x~%012x", i+1, i+1)
		if err := releases.PutStaged(ctx, router.DeploymentRecord{App: "web", Build: build}); err != nil {
			t.Fatalf("stage %s: %v", build, err)
		}
		promotion := router.Promotion{PromotionID: id, Ts: int64(i + 1), Builds: map[string]string{"web": build}}
		if _, err := releases.Promote(ctx, promotion, "", replaces); err != nil {
			t.Fatalf("promote %s: %v", id, err)
		}
		replaces = id
	}
}
