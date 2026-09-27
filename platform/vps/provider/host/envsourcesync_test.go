package host

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func TestEachClassRunsItsOwnEnvSourceSyncThatWritesThatClassesRecordsAlone(t *testing.T) {
	t.Parallel()

	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		items := Items(class, []byte(aKey+"\n"), ArchAMD64, Front{})
		unit := itemAt(t, items, KindUnit, "ocel-envsourcesync@"+string(class)+".service")
		if unit.Owner != rootOwner {
			t.Errorf("%s is enabled as %s, and the sync reads the class key, which is root's alone", unit.Name, unit.Owner)
		}
		for _, item := range items {
			if item.Kind == KindUnit && strings.HasPrefix(item.Name, "ocel-envsourcesync@") && item.Name != unit.Name {
				t.Errorf("the %s class enables %s, a sync over another class's records", class, item.Name)
			}
		}
	}

	template := string(itemAt(t, Items(edge.ClassProduction, []byte(aKey+"\n"), ArchAMD64, Front{}), KindFile, envSourceSyncTemplateFile).Content)
	for _, want := range []string{
		"ExecStart=" + LiveBinary + " " + live.EnvSourceSyncCommand + " --class %i",
		"ProtectSystem=strict",
		"ProtectHome=yes",
		"PrivateTmp=yes",
		"NoNewPrivileges=yes",
		"CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE",
		"After=network-online.target",
		"Wants=network-online.target",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(template, want) {
			t.Errorf("the sync's unit reads:\n%s\nand never says %q", template, want)
		}
	}
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		if want := "ReadWritePaths=" + RecordsDir(class) + "\n"; !strings.Contains(strings.ReplaceAll(template, "%i", string(class)), want) {
			t.Errorf("the sync's unit reads:\n%s\nand as the %s instance never says %q: it writes that class's records and nothing else", template, class, want)
		}
	}
	for _, never := range []string{"User=", ConnectorUnit, "docker"} {
		if strings.Contains(template, never) {
			t.Errorf("the sync's unit reads:\n%s\nand names %q: it runs as root bounded to two capabilities, and needs nothing but the helpers", template, never)
		}
	}
}

func TestTheEnvSourceSyncRestartsWhenTheAgentBinaryOrItsUnitChangesAndIsWrittenAfterBoth(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	items := Items(class, []byte(aKey+"\n"), ArchAMD64, Front{})
	at := func(kind, name string) int {
		return slices.IndexFunc(items, func(item Item) bool { return item.Kind == kind && item.Name == name })
	}
	service := EnvSourceSyncService(class)
	unit := at(KindUnit, service)
	if unit < 0 {
		t.Fatalf("nothing in the item set enables %s", service)
	}
	for _, watched := range []string{envSourceSyncTemplateFile, LiveBinary} {
		if !slices.Contains(items[unit].Watch, watched) {
			t.Errorf("%s does not watch %s, so a changed one would not restart it", service, watched)
		}
		if written := at(KindFile, watched); written < 0 || written > unit {
			t.Errorf("%s watches %s, which is written at %d, after the unit at %d", service, watched, written, unit)
		}
	}
	template := itemAt(t, items, KindFile, envSourceSyncTemplateFile)
	if !bytes.Equal(items[unit].Content, unitWatchFacts(template.Content, liveAgent(ArchAMD64))) {
		t.Error("the unit's facts do not follow the unit file and the agent binary, so a changed sync would not restart it")
	}
	if template.Owner != rootOwner || template.Mode != 0o644 {
		t.Errorf("the sync's unit is written as %s %04o, and it is root's alone", template.Owner, template.Mode)
	}
}

func TestAnApplyOverAHostBootstrappedBeforeTheEnvSourceSyncWritesAndStartsIt(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	missing := []Item{
		{Kind: KindFile, Name: envSourceSyncTemplateFile},
		{Kind: KindUnit, Name: EnvSourceSyncService(class)},
	}
	older := bootstrappedOn(t, class)
	older.installed[class] = slices.DeleteFunc(older.installed[class], func(item Item) bool {
		return slices.ContainsFunc(missing, func(gone Item) bool { return gone.ID() == item.ID() })
	})
	progress := &said{}
	if err := NewBootstrap(older.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Class: class, WrittenBy: "the-suite"}, progress); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	for _, item := range missing {
		if progress.at("Installed "+item.phrase()) < 0 {
			t.Errorf("an apply never wrote %s:\n%s", item.ID(), strings.Join(progress.lines, "\n"))
		}
	}
	if older.at("systemctl restart "+quoted(EnvSourceSyncService(class))) < 0 {
		t.Errorf("an apply never started %s:\n%s", EnvSourceSyncService(class), strings.Join(older.commands(), "\n"))
	}
}

func TestDestroyingAClassStopsItsEnvSourceSyncBeforeItsRecordsAndTheLastTakesTheTemplate(t *testing.T) {
	t.Parallel()

	production, preview := edge.ClassProduction, edge.ClassPreview
	keys := []byte(aKey + "\n")
	current := Reading{Arch: ArchAMD64, Class: production, Keys: keys, Observed: digests(Items(production, keys, ArchAMD64, Front{}))}
	beside := Reading{Arch: ArchAMD64, Class: preview, Keys: keys, Observed: digests(Items(preview, keys, ArchAMD64, Front{}))}

	shared := removing(current, beside, appsPresent{})
	own := removalOf(shared, EnvSourceSyncService(production))
	if own.action != provider.ActionDelete || own.kind != KindUnit {
		t.Errorf("destroying %s plans its sync %s as %q", production, EnvSourceSyncService(production), own.action)
	}
	if index(shared, EnvSourceSyncService(production)) > index(shared, StateDir(production)) {
		t.Errorf("destroying %s takes its records before it stops the sync writing them", production)
	}
	for _, kept := range []string{EnvSourceSyncService(preview), LiveBinary, envSourceSyncTemplateFile} {
		if removalOf(shared, kept).action == provider.ActionDelete {
			t.Errorf("destroying %s takes %s, and the %s sync still runs from it", production, kept, preview)
		}
	}

	last := removing(current, Reading{Arch: ArchAMD64, Class: preview, Observed: map[string]string{}}, appsPresent{})
	for _, gone := range []string{EnvSourceSyncService(production), LiveBinary, envSourceSyncTemplateFile} {
		if removalOf(last, gone).action != provider.ActionDelete {
			t.Errorf("destroying the last class plans %s as %q", gone, removalOf(last, gone).action)
		}
	}

	box := machine(map[edge.Class][]Item{production: bootstrapped(t, production)})
	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), production, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := strings.Join(box.commands(), "\n")
	if !strings.Contains(taken, "systemctl disable --now "+quoted(EnvSourceSyncService(production))) {
		t.Errorf("the last destroy never stopped %s:\n%s", EnvSourceSyncService(production), taken)
	}
	for _, file := range []string{LiveBinary, envSourceSyncTemplateFile} {
		if !strings.Contains(taken, "rm -rf "+quoted(file)) {
			t.Errorf("the last destroy left %s:\n%s", file, taken)
		}
	}
}

func TestTheDeployLoginIsToldTheEnvSourceSyncIsRootsAndWhatItMayWrite(t *testing.T) {
	t.Parallel()

	class := edge.ClassPreview
	var named bool
	for _, grant := range Grants(class) {
		if !strings.Contains(grant.Name, EnvSourceSyncService(class)) {
			continue
		}
		named = true
		for _, want := range []string{LiveBinary, deployUser, RecordsDir(class), "CAP_CHOWN", "CAP_DAC_OVERRIDE", boxstore.SealHelper} {
			if !strings.Contains(grant.Detail, want) {
				t.Errorf("the grant reads %q and never says %q", grant.Detail, want)
			}
		}
	}
	if !named {
		t.Error("`ocel permissions deploy` never mentions the sync that writes this class's values as root")
	}
}
