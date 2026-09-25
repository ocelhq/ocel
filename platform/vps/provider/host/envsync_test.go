package host

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func envSyncTemplateOf(t *testing.T, class providerkit.Class) string {
	t.Helper()
	return string(itemAt(t, Items(class, []byte(aKey+"\n"), ArchAMD64), KindFile, envSyncTemplateFile).Content)
}

func TestEachClassRunsItsOwnSyncerThatWritesThatClassesRecordsAlone(t *testing.T) {
	t.Parallel()

	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		items := Items(class, []byte(aKey+"\n"), ArchAMD64)
		unit := itemAt(t, items, KindUnit, "ocel-envsync@"+string(class)+".service")
		if unit.Owner != rootOwner {
			t.Errorf("%s is enabled as %s, and the syncer reads the class key, which is root's alone", unit.Name, unit.Owner)
		}
		for _, item := range items {
			if item.Kind == KindUnit && strings.HasPrefix(item.Name, "ocel-envsync@") && item.Name != unit.Name {
				t.Errorf("the %s class enables %s, a syncer over another class's records", class, item.Name)
			}
		}
	}

	template := envSyncTemplateOf(t, providerkit.ClassProduction)
	for _, want := range []string{
		"ExecStart=" + LiveBinary + " envsync --class %i",
		"ReadWritePaths=" + stateRoot + "/%i/records",
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
			t.Errorf("the syncer's unit reads:\n%s\nand never says %q", template, want)
		}
	}
	for _, never := range []string{"User=", ConnectorUnit, "docker"} {
		if strings.Contains(template, never) {
			t.Errorf("the syncer's unit reads:\n%s\nand names %q: it runs as root bounded to two capabilities, and stands on nothing but the helpers", template, never)
		}
	}
}

func TestTheSyncerRestartsWhenTheAgentBinaryOrItsUnitMovesAndIsWrittenAfterBoth(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	items := Items(class, []byte(aKey+"\n"), ArchAMD64)
	at := func(kind, name string) int {
		return slices.IndexFunc(items, func(item Item) bool { return item.Kind == kind && item.Name == name })
	}
	service := EnvSyncService(class)
	unit := at(KindUnit, service)
	if unit < 0 {
		t.Fatalf("nothing in the item set enables %s", service)
	}
	for _, watched := range []string{envSyncTemplateFile, LiveBinary} {
		if !slices.Contains(items[unit].Watch, watched) {
			t.Errorf("%s does not watch %s, so a changed one would not restart it", service, watched)
		}
		if written := at(KindFile, watched); written < 0 || written > unit {
			t.Errorf("%s watches %s, which is written at %d, after the unit at %d", service, watched, written, unit)
		}
	}
	template := itemAt(t, items, KindFile, envSyncTemplateFile)
	if !bytes.Equal(items[unit].Content, unitWatchFacts(template.Content, liveAgent(ArchAMD64))) {
		t.Error("the unit's facts do not follow the unit file and the agent binary, so a changed syncer would not restart it")
	}
	if template.Owner != rootOwner || template.Mode != 0o644 {
		t.Errorf("the syncer's unit is written as %s %04o, and it is root's alone", template.Owner, template.Mode)
	}
}

func TestAnApplyOverAHostBootstrappedBeforeTheSyncerWritesAndStartsIt(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	missing := []string{
		KindFile + " " + envSyncTemplateFile,
		KindUnit + " " + EnvSyncService(class),
	}
	stood := settledOn(t, class)
	stood.stands[class] = slices.DeleteFunc(stood.stands[class], func(item Item) bool { return slices.Contains(missing, item.ID()) })
	report := &said{}
	if err := Bootstrap(stood.host(), testVendor).Apply(context.Background(),
		providerkit.BootstrapRequest{Class: class, Writer: "the-suite"}, report); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	for _, id := range missing {
		if report.at("wrote "+id) < 0 {
			t.Errorf("an apply never wrote %s:\n%s", id, strings.Join(report.lines, "\n"))
		}
	}
	if stood.at("systemctl restart "+quoted(EnvSyncService(class))) < 0 {
		t.Errorf("an apply never started %s:\n%s", EnvSyncService(class), strings.Join(stood.commands(), "\n"))
	}
}

func TestDestroyingAClassStopsItsSyncerBeforeItsRecordsAndTheLastTakesTheBinary(t *testing.T) {
	t.Parallel()

	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	keys := []byte(aKey + "\n")
	standing := Reading{Arch: ArchAMD64, Class: production, Keys: keys, Observed: digests(Items(production, keys, ArchAMD64))}
	beside := Reading{Arch: ArchAMD64, Class: preview, Keys: keys, Observed: digests(Items(preview, keys, ArchAMD64))}

	shared := removing(standing, beside, appsStanding{})
	own := removalOf(shared, EnvSyncService(production))
	if own.action != providerkit.ActionDelete || own.kind != KindUnit {
		t.Errorf("destroying %s plans its syncer %s as %q", production, EnvSyncService(production), own.action)
	}
	if index(shared, EnvSyncService(production)) > index(shared, StateDir(production)) {
		t.Errorf("destroying %s takes its records before it stops the syncer writing them", production)
	}
	for _, kept := range []string{EnvSyncService(preview), LiveBinary, envSyncTemplateFile} {
		if removalOf(shared, kept).action == providerkit.ActionDelete {
			t.Errorf("destroying %s takes %s, and the %s syncer still runs from it", production, kept, preview)
		}
	}

	last := removing(standing, Reading{Arch: ArchAMD64, Class: preview, Observed: map[string]string{}}, appsStanding{})
	for _, gone := range []string{EnvSyncService(production), LiveBinary, envSyncTemplateFile} {
		if removalOf(last, gone).action != providerkit.ActionDelete {
			t.Errorf("destroying the last class plans %s as %q", gone, removalOf(last, gone).action)
		}
	}

	stood := machine(map[providerkit.Class][]Item{production: bootstrapped(t, production)})
	if err := Bootstrap(stood.host(), testVendor).Remove(context.Background(), production, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := strings.Join(stood.commands(), "\n")
	if !strings.Contains(taken, "systemctl disable --now "+quoted(EnvSyncService(production))) {
		t.Errorf("the last destroy never stopped %s:\n%s", EnvSyncService(production), taken)
	}
	for _, file := range []string{LiveBinary, envSyncTemplateFile} {
		if !strings.Contains(taken, "rm -rf "+quoted(file)) {
			t.Errorf("the last destroy left %s:\n%s", file, taken)
		}
	}
}

func TestTheDeployLoginIsToldTheSyncerIsRootsAndWhatItMayWrite(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassPreview
	var named bool
	for _, grant := range Grants(class) {
		if !strings.Contains(grant.Name, EnvSyncService(class)) {
			continue
		}
		named = true
		for _, want := range []string{LiveBinary, deployUser, RecordsDir(class), "CAP_CHOWN", "CAP_DAC_OVERRIDE", SealHelper} {
			if !strings.Contains(grant.Detail, want) {
				t.Errorf("the grant reads %q and never says %q", grant.Detail, want)
			}
		}
	}
	if !named {
		t.Error("`ocel permissions deploy` never mentions the syncer that writes this class's values as root")
	}
}
