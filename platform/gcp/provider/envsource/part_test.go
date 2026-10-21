package envsource

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestTheFirstScheduledEnvSourceAddsTheSyncWithTheAccountItRunsAsAndNoBootstrapMakesThem(t *testing.T) {
	t.Parallel()
	names := names.New("ocel", "acme-prod")
	for _, tier := range googletest.BothTiers {
		for _, emulated := range []bool{false, true} {
			ids := googletest.IdsOf(bootstrap.Resources(names, tier, emulated))
			for _, each := range []bootstrap.Resource{
				{Kind: bootstrap.KindServiceAccount, Name: names.EnvSourceSyncAccount(tier)},
				{Kind: bootstrap.KindService, Name: names.EnvSourceSync(tier)},
				{Kind: bootstrap.KindSchedule, Name: names.EnvSourceSync(tier)},
			} {
				if slices.Contains(ids, each.ID()) {
					t.Errorf("a %s bootstrap (emulated=%t) provisions %s, which the first scheduled env source adds", tier, emulated, each.ID())
				}
			}
		}
	}
}

func TestTheFirstScheduledEnvSourceMakesTheKeysTheSyncSealsUnderAndLetsTheSyncSealForItsProject(t *testing.T) {
	t.Parallel()
	server := googletest.NewSync()
	b := syncOpened(server, t)
	b.Records = fake.NewKeyValues()
	ctx := context.Background()
	tier := environment.TierPreview
	key := b.Clients.Workload().ProjectKeyPath(tier, "shop")

	if err := Ensure(ctx, b, tier, "shop"); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	if !server.HasKey(gcloud.KeyPath(b.Clients, string(tier))) {
		t.Errorf("the first scheduled env source of %s left no %s key, and the sync seals under it", tier, tier)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, bootstrap.KeysPart); err != nil || !present {
		t.Errorf("the keys part is recorded %t, %v, want it added with the sync", present, err)
	}
	member := "serviceAccount:" + b.Clients.EnvSourceSyncAccountEmail(tier)
	for _, role := range SyncKeyRoles {
		if !slices.Contains(server.KeyMembers(key, role), member) {
			t.Errorf("project shop's key binds %v to %s, want %s: the sync seals what it reads for shop under shop's key", server.KeyMembers(key, role), role, member)
		}
	}
}

func syncPartBootstrap(t *testing.T) (*googletest.Sync, bootstrap.Engine) {
	t.Helper()
	server := googletest.NewSync()
	server.Accounts = map[string]bool{}
	b := syncOpened(server, t)
	b.Records = fake.NewKeyValues()
	return server, b
}

func TestTheFirstScheduledEnvSourceAddsTheSyncServiceAndScheduleAsAPartItRecords(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierProduction
	name := b.Clients.EnvSourceSync(tier)

	if err := Ensure(ctx, b, tier, "shop"); err != nil {
		t.Fatalf("ensureEnvSourceSync() = %v", err)
	}

	if server.Service(b.Clients.ServicePath(name)) == nil {
		t.Errorf("no sync service at %s", b.Clients.ServicePath(name))
	}
	if found, err := schedulePresence(ctx, b, tier, name); err != nil || !found.Present || found.Mends != "" {
		t.Errorf("schedulePresence() = %+v, %v, want the schedule calling the service", found, err)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, PartName)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" || len(recorded.Resources) != 3 {
		t.Errorf("part record = %+v, %v, %v, want it ready, added by shop, naming the account, the service and the schedule", recorded, present, err)
	}

	pushes := len(server.Pushed)
	if err := Ensure(ctx, b, tier, "blog"); err != nil {
		t.Fatalf("ensureEnvSourceSync() again = %v", err)
	}
	if len(server.Pushed) != pushes {
		t.Errorf("a second scheduled env source pushed the sync again, want it to read the record and stop")
	}
}

func TestABootstrapResumesAnEnvSourceSyncSchedulePausedByHand(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierProduction
	path := gcloud.SchedulePath(b.Clients, b.Clients.EnvSourceSync(tier))
	if err := Ensure(ctx, b, tier, "shop"); err != nil {
		t.Fatal(err)
	}
	server.Change(func() { server.Schedules[path].State = gcloud.SchedulePaused })

	repaired, err := b.RepairParts(ctx, tier, false)
	if err != nil {
		t.Fatalf("repairParts() = %v", err)
	}

	if !slices.Equal(repaired, []string{PartName}) {
		t.Errorf("repairParts() = %v, want the env source sync made again", repaired)
	}
	if mended := server.Schedule(path); mended == nil || mended.State != gcloud.ScheduleEnabled {
		t.Errorf("the schedule is %s after a bootstrap, want it calling the sync again", googletest.Encoded(t, mended))
	}
	recorded, _, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, PartName)
	if err != nil || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" {
		t.Errorf("part record = %+v (%v), want it ready and still added by shop", recorded, err)
	}
}

func TestABootstrapLeavesACurrentEnvSourceSyncAsItIs(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if err := Ensure(ctx, b, tier, "shop"); err != nil {
		t.Fatal(err)
	}
	pushes, patches := len(server.Pushed), len(server.Patched)

	if _, err := b.RepairParts(ctx, tier, false); err != nil {
		t.Fatalf("repairParts() = %v", err)
	}

	if len(server.Pushed) != pushes || len(server.Patched) != patches {
		t.Errorf("a bootstrap over a sync with no drift pushed or redeployed it, want it read and left alone")
	}
}

func TestABootstrapAddsNoEnvSourceSyncNoScheduledEnvSourceAdded(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	tier := environment.TierProduction

	if repaired, err := b.RepairParts(context.Background(), tier, false); err != nil || len(repaired) != 0 {
		t.Fatalf("repairParts() = %v, %v, want nothing made", repaired, err)
	}
	if server.Service(b.Clients.ServicePath(b.Clients.EnvSourceSync(tier))) != nil {
		t.Error("a bootstrap made the env source sync, which only a scheduled env source adds")
	}
}

func TestAnEnvSourceSyncANewerReleaseWroteIsLeftAlone(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	b.WrittenBy = "1.2.0"
	ctx := context.Background()
	tier := environment.TierProduction
	newer := stackrecords.BootstrapPart{WrittenBy: "1.3.0", Digest: "what 1.3.0 deploys", AddedBy: "shop"}
	if err := stackrecords.MarkBootstrapPartReady(ctx, b.Records, tier, PartName, newer); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(ctx, b, tier, "blog"); err != nil {
		t.Fatalf("ensureEnvSourceSync() = %v", err)
	}

	recorded, _, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, PartName)
	if len(server.Pushed) != 0 || err != nil || recorded.WrittenBy != "1.3.0" || recorded.Digest != newer.Digest {
		t.Errorf("1.2.0 pushed %d images and left %+v (%v), want what 1.3.0 wrote untouched", len(server.Pushed), recorded, err)
	}
}

func TestATierWithAnEnvSourceSyncDescribesItAsAPartUsedByItsScheduledProjects(t *testing.T) {
	t.Parallel()
	_, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierPreview
	if err := Ensure(ctx, b, tier, "shop"); err != nil {
		t.Fatal(err)
	}

	part, present, err := describeEnvSourceSyncPart(ctx, b, tier)
	if err != nil || !present {
		t.Fatalf("envSourceSyncPart() = %v, %v, want the part", present, err)
	}
	if part.Name != PartName || !part.Current || len(part.Resources) != 3 || part.Shape == nil || len(part.Shape.Resources) != 2 {
		t.Errorf("part = %+v, want it current, with the account, the service and schedule and the shape that prices the last two", part)
	}
}

func TestRemovingATierTakesItsEnvSourceSyncAndAPlanToRemoveItListsIt(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierProduction
	name := b.Clients.EnvSourceSync(tier)

	none, err := envSourceSyncRemovals(ctx, b, tier)
	if err != nil || len(none) != 0 {
		t.Fatalf("envSourceSyncRemovals() before any env source = %v, %v, want nothing", none, err)
	}
	if err := Ensure(ctx, b, tier, "shop"); err != nil {
		t.Fatal(err)
	}
	changes, err := envSourceSyncRemovals(ctx, b, tier)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, change := range changes {
		if change.Action != provider.ActionDelete || (change.Name != name && change.Name != b.Clients.EnvSourceSyncAccount(tier)) {
			t.Errorf("change %+v, want %s deleted", change, name)
		}
		kinds = append(kinds, change.Kind)
	}
	if !slices.Equal(kinds, []string{string(bootstrap.KindSchedule), string(bootstrap.KindService), string(bootstrap.KindServiceAccount)}) {
		t.Errorf("a plan to remove the tier deletes %v, want the schedule, then the service it calls, then the account it runs as", kinds)
	}

	if err := removeEnvSourceSync(ctx, b, tier, nil); err != nil {
		t.Fatalf("removeEnvSourceSync() = %v", err)
	}
	if server.Service(b.Clients.ServicePath(name)) != nil {
		t.Error("the sync service outlived its tier")
	}
	if found, err := schedulePresence(ctx, b, tier, name); err != nil || found.Present {
		t.Errorf("schedulePresence() after removal = %+v, %v, want it gone", found, err)
	}
	if !slices.ContainsFunc(server.DeletedAccounts, func(path string) bool { return strings.Contains(path, "/"+b.Clients.EnvSourceSyncAccount(tier)+"@") }) {
		t.Errorf("removal deleted the accounts %v, want the sync's own among them", server.DeletedAccounts)
	}
}

func TestRemovingATierTakesAnEnvSourceSyncThatHasNoPartRecord(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierProduction
	name := b.Clients.EnvSourceSync(tier)
	if err := Ensure(ctx, b, tier, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.ForgetBootstrapPart(ctx, b.Records, tier, PartName); err != nil {
		t.Fatal(err)
	}

	changes, err := envSourceSyncRemovals(ctx, b, tier)
	if err != nil || len(changes) != 3 {
		t.Errorf("envSourceSyncRemovals() of a sync with no part record = %v, %v, want its schedule, service and account deleted", changes, err)
	}
	if err := removeEnvSourceSync(ctx, b, tier, nil); err != nil {
		t.Fatalf("removeEnvSourceSync() = %v", err)
	}
	if found, err := schedulePresence(ctx, b, tier, name); err != nil || found.Present {
		t.Errorf("schedulePresence() after removing the tier = %+v, %v, want the schedule gone: it calls the sync every minute", found, err)
	}
	if server.Service(b.Clients.ServicePath(name)) != nil {
		t.Error("the sync service outlived its tier")
	}
}

func TestRemovingATierWhoseSyncNeverHadAKeyTakesItsAccount(t *testing.T) {
	t.Parallel()
	server, b := syncPartBootstrap(t)
	ctx := context.Background()
	tier := environment.TierPreview
	account := b.Clients.EnvSourceSyncAccount(tier)
	if err := ensureSyncAccount(ctx, b, tier); err != nil {
		t.Fatal(err)
	}

	if err := removeEnvSourceSync(ctx, b, tier, nil); err != nil {
		t.Fatalf("removeEnvSourceSync() of a sync whose tier has no key = %v", err)
	}
	if !slices.ContainsFunc(server.DeletedAccounts, func(path string) bool { return strings.Contains(path, "/"+account+"@") }) {
		t.Errorf("removal deleted the accounts %v, want the sync's own among them", server.DeletedAccounts)
	}
}

func TestRemovingATierWithAServiceTheSyncNeedsSwitchedOffTakesWhatIsLeftOfTheSync(t *testing.T) {
	t.Parallel()
	for _, off := range []string{"cloudscheduler.googleapis.com", "run.googleapis.com", "cloudkms.googleapis.com"} {
		t.Run(off, func(t *testing.T) {
			t.Parallel()
			server, b := syncPartBootstrap(t)
			server.ServicesOff = []string{off}
			ctx := context.Background()
			tier := environment.TierProduction

			changes, err := envSourceSyncRemovals(ctx, b, tier)
			if err != nil || len(changes) != 0 {
				t.Errorf("envSourceSyncRemovals() with %s off and no sync made = %v, %v, want nothing to remove", off, changes, err)
			}
			if err := removeEnvSourceSync(ctx, b, tier, nil); err != nil {
				t.Errorf("removeEnvSourceSync() with %s off = %v, want the tier removed: no sync can stand where it is off", off, err)
			}
		})
	}
}
