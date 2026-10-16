package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestABootstrapMakesTheSyncAccountAndLeavesTheSyncToTheFirstScheduledEnvSource(t *testing.T) {
	t.Parallel()
	names := Names{namespace: "ocel", project: "acme-prod"}
	for _, tier := range bothTiers {
		for _, emulated := range []bool{false, true} {
			ids := idsOf(bootstrapItems(names, tier, emulated))
			if !slices.Contains(ids, (item{Kind: KindServiceAccount, Name: names.EnvSourceSyncAccount(tier)}).ID()) {
				t.Errorf("a %s bootstrap (emulated=%t) provisions %v, want the sync's account among them: only a bootstrap may grant it the tier key", tier, emulated, ids)
			}
			for _, each := range []item{{Kind: KindService, Name: names.EnvSourceSync(tier)}, {Kind: KindSchedule, Name: names.EnvSourceSync(tier)}} {
				if slices.Contains(ids, each.ID()) {
					t.Errorf("a %s bootstrap (emulated=%t) provisions %s, which the first scheduled env source adds", tier, emulated, each.ID())
				}
			}
		}
	}
}

func syncPartBootstrap(t *testing.T) (*syncServer, bootstrap) {
	t.Helper()
	server := newSyncServer()
	b := server.open(t)
	b.records = fake.NewKeyValues()
	return server, b
}

func TestTheFirstScheduledEnvSourceAddsTheSyncServiceAndScheduleAsAPartItRecords(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierProduction
	name := b.clients.EnvSourceSync(tier)

	if err := b.ensureEnvSourceSync(ctx, tier, "shop"); err != nil {
		t.Fatalf("ensureEnvSourceSync() = %v", err)
	}

	if server.service(b.clients.servicePath(name)) == nil {
		t.Errorf("no sync service at %s", b.clients.servicePath(name))
	}
	if found, err := b.schedulePresence(ctx, tier, name); err != nil || !found.present || found.mends != "" {
		t.Errorf("schedulePresence() = %+v, %v, want the schedule calling the service", found, err)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, envSourceSyncPart)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" || len(recorded.Resources) != 2 {
		t.Errorf("part record = %+v, %v, %v, want it ready, added by shop, naming the service and the schedule", recorded, present, err)
	}

	pushes := len(server.pushed)
	if err := b.ensureEnvSourceSync(ctx, tier, "blog"); err != nil {
		t.Fatalf("ensureEnvSourceSync() again = %v", err)
	}
	if len(server.pushed) != pushes {
		t.Errorf("a second scheduled env source pushed the sync again, want it to read the record and stop")
	}
}

func TestABootstrapResumesAnEnvSourceSyncSchedulePausedByHand(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierProduction
	path := schedulePath(b.clients, b.clients.EnvSourceSync(tier))
	if err := b.ensureEnvSourceSync(ctx, tier, "shop"); err != nil {
		t.Fatal(err)
	}
	server.change(func() { server.schedules[path].State = schedulePaused })

	repaired, err := b.repairParts(ctx, tier)
	if err != nil {
		t.Fatalf("repairParts() = %v", err)
	}

	if !slices.Equal(repaired, []string{envSourceSyncPart}) {
		t.Errorf("repairParts() = %v, want the env source sync made again", repaired)
	}
	if mended := server.schedule(path); mended == nil || mended.State != scheduleEnabled {
		t.Errorf("the schedule is %s after a bootstrap, want it calling the sync again", encoded(t, mended))
	}
	recorded, _, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, envSourceSyncPart)
	if err != nil || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" {
		t.Errorf("part record = %+v (%v), want it ready and still added by shop", recorded, err)
	}
}

func TestABootstrapLeavesACurrentEnvSourceSyncAsItIs(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if err := b.ensureEnvSourceSync(ctx, tier, "shop"); err != nil {
		t.Fatal(err)
	}
	pushes, patches := len(server.pushed), len(server.patched)

	if _, err := b.repairParts(ctx, tier); err != nil {
		t.Fatalf("repairParts() = %v", err)
	}

	if len(server.pushed) != pushes || len(server.patched) != patches {
		t.Errorf("a bootstrap over a sync with no drift pushed or redeployed it, want it read and left alone")
	}
}

func TestABootstrapAddsNoEnvSourceSyncNoScheduledEnvSourceAdded(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	tier := environment.TierProduction

	if repaired, err := b.repairParts(context.Background(), tier); err != nil || len(repaired) != 0 {
		t.Fatalf("repairParts() = %v, %v, want nothing made", repaired, err)
	}
	if server.service(b.clients.servicePath(b.clients.EnvSourceSync(tier))) != nil {
		t.Error("a bootstrap made the env source sync, which only a scheduled env source adds")
	}
}

func TestAnEnvSourceSyncANewerReleaseWroteIsLeftAlone(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	b.writtenBy = "1.2.0"
	ctx := context.Background()
	tier := environment.TierProduction
	newer := stackrecords.BootstrapPart{WrittenBy: "1.3.0", Digest: "what 1.3.0 deploys", AddedBy: "shop"}
	if err := stackrecords.MarkBootstrapPartReady(ctx, b.records, tier, envSourceSyncPart, newer); err != nil {
		t.Fatal(err)
	}

	if err := b.ensureEnvSourceSync(ctx, tier, "blog"); err != nil {
		t.Fatalf("ensureEnvSourceSync() = %v", err)
	}

	recorded, _, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, envSourceSyncPart)
	if len(server.pushed) != 0 || err != nil || recorded.WrittenBy != "1.3.0" || recorded.Digest != newer.Digest {
		t.Errorf("1.2.0 pushed %d images and left %+v (%v), want what 1.3.0 wrote untouched", len(server.pushed), recorded, err)
	}
}

func TestATierWithAnEnvSourceSyncDescribesItAsAPartUsedByItsScheduledProjects(t *testing.T) {
	t.Parallel()
	_, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierPreview
	if err := b.ensureEnvSourceSync(ctx, tier, "shop"); err != nil {
		t.Fatal(err)
	}

	part, present, err := b.envSourceSyncPart(ctx, tier)
	if err != nil || !present {
		t.Fatalf("envSourceSyncPart() = %v, %v, want the part", present, err)
	}
	if part.Name != envSourceSyncPart || !part.Current || len(part.Resources) != 2 || part.Shape == nil || len(part.Shape.Resources) != 2 {
		t.Errorf("part = %+v, want it current, with the service and schedule and the shape that prices them", part)
	}
}

func TestRemovingATierTakesItsEnvSourceSyncAndAPlanToRemoveItListsIt(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierProduction
	name := b.clients.EnvSourceSync(tier)

	none, err := b.envSourceSyncRemovals(ctx, tier)
	if err != nil || len(none) != 0 {
		t.Fatalf("envSourceSyncRemovals() before any env source = %v, %v, want nothing", none, err)
	}
	if err := b.ensureEnvSourceSync(ctx, tier, "shop"); err != nil {
		t.Fatal(err)
	}
	changes, err := b.envSourceSyncRemovals(ctx, tier)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, change := range changes {
		if change.Action != provider.ActionDelete || change.Name != name {
			t.Errorf("change %+v, want %s deleted", change, name)
		}
		kinds = append(kinds, change.Kind)
	}
	if !slices.Equal(kinds, []string{string(KindSchedule), string(KindService)}) {
		t.Errorf("a plan to remove the tier deletes %v, want the schedule, then the service it calls", kinds)
	}

	if err := b.removeEnvSourceSync(ctx, tier, nil); err != nil {
		t.Fatalf("removeEnvSourceSync() = %v", err)
	}
	if server.service(b.clients.servicePath(name)) != nil {
		t.Error("the sync service outlived its tier")
	}
	if found, err := b.schedulePresence(ctx, tier, name); err != nil || found.present {
		t.Errorf("schedulePresence() after removal = %+v, %v, want it gone", found, err)
	}
}
