package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func heardAll(t *testing.T, progress *fake.Progress, want ...string) {
	t.Helper()
	lines := progress.Lines()
	for _, line := range want {
		if !slices.Contains(lines, line) {
			t.Errorf("progress never said %q:\n%s", line, strings.Join(lines, "\n"))
		}
	}
}

func TestABootstrapRemovalSaysWhatItRemovedAndWhatItKeptAndWhy(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})
	box.answer = func(command string) (session.Result, bool) {
		if strings.HasPrefix(command, "rmdir "+quoted("/usr/local/lib/ocel")+" ") {
			return session.Result{Stdout: dirNonEmpty + "\n"}, true
		}
		return session.Result{}, false
	}
	progress := &fake.Progress{}

	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, progress); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	heardAll(t, progress,
		"INFO Kept the Docker engine: docker and its containers stay",
		"INFO Removed directory /var/lib/ocel/production",
		"INFO Removed seal key /etc/ocel/production/seal.key",
		"INFO Removed user ocel-deploy",
		"INFO Kept directory /usr/local/lib/ocel: something else on this box still uses it",
	)
	lines := progress.Lines()
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "INFO Your known_hosts still trusts this box: to drop it, run `ssh-keygen -R ") {
		t.Errorf("Remove() ends on %q, want the known_hosts line naming what to run", last)
	}
}

func TestAnApplySaysWhatItInstalledAndLeavesWhatWasCurrentToDebug(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := bootstrappedOn(t, tier)
	box.installed[tier] = slices.DeleteFunc(box.installed[tier], func(item Item) bool {
		return item.Kind == KindUnit && item.Name == BackupsTimer
	})
	progress := &fake.Progress{}

	if err := NewBootstrap(box.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite"}, progress); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	heardAll(t, progress,
		"INFO Installed systemd unit ocel-backups.timer",
		"DEBUG Directory /etc/ocel is already current",
	)
}

func TestAHealSaysWhatItRewroteAndWhatItLeftAsItIs(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	installed := bootstrapped(t, tier)
	for at, item := range installed {
		if (item.Kind == KindDir && item.Name == KeyValuesDir(tier)) || (item.Kind == KindUnit && item.Name == dockerUnit) {
			installed[at].Mode = 0o700
		}
	}
	box := machine(map[environment.Tier][]Item{tier: installed})
	box.answer = func(command string) (session.Result, bool) {
		if command != "cat ~/.ssh/authorized_keys 2>/dev/null" {
			return session.Result{}, false
		}
		return session.Result{Stdout: aKey + "\n"}, true
	}
	progress := &fake.Progress{}

	if err := NewBootstrap(box.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite", Heal: true}, progress); err != nil {
		t.Fatalf("heal = %v", err)
	}
	heardAll(t, progress,
		"INFO Left systemd unit docker.service as it is: a refresh rewrites only what deploys own",
		"INFO Installed directory /var/lib/ocel/production/keyvalues",
	)
}

func TestAConnectorInstallSaysWhatItWroteAndWhereTheConsoleReachesIt(t *testing.T) {
	t.Parallel()

	box := &claimBench{bench: machine(nil), recorded: string(mustWrite(t, routed()))}
	absent := ""
	box.answer = servesPair(box.bench, &box.recorded, &absent)
	progress := &fake.Progress{}

	if _, err := NewConnector(box.host()).Install(context.Background(), "box.example.com", []byte("a connector"), connectorConfig(), progress); err != nil {
		t.Fatalf("Install() = %v", err)
	}
	heardAll(t, progress,
		"INFO Installed file /usr/local/lib/ocel/connector",
		"INFO Installed systemd unit ocel-connector.service",
		"INFO Routed https://box.example.com"+switchboard.ConnectorPath+" to the connector",
	)
}

func TestAConnectorRemovalSaysWhatItUnroutedAndRemoved(t *testing.T) {
	t.Parallel()

	box := &claimBench{bench: machine(nil), recorded: string(mustWrite(t, routed()))}
	absent := ""
	box.answer = servesPair(box.bench, &box.recorded, &absent)
	progress := &fake.Progress{}

	if err := NewConnector(box.host()).Remove(context.Background(), progress); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	heardAll(t, progress,
		"INFO Stopped routing "+switchboard.ConnectorPath+" to the connector",
		"INFO Removed the connector's systemd unit ocel-connector.service, its binary /usr/local/lib/ocel/connector and directory /etc/ocel/connector",
	)
}

func TestReconcilingAnAppsImagesNamesEveryImageItRemoved(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	box.answer = func(command string) (session.Result, bool) {
		if !strings.HasPrefix(command, quoted(releasesHelper)) || !strings.Contains(command, quoted("reconcile")) {
			return session.Result{}, false
		}
		return session.Result{Stdout: "ocel/shop-web:1111\nocel/shop-web:2222\n"}, true
	}
	progress := &fake.Progress{}

	if err := box.host().Reconcile(context.Background(), "shop", "web", "ocel/shop-web:3333", progress); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	heardAll(t, progress,
		"INFO Removed web's unused image ocel/shop-web:1111",
		"INFO Removed web's unused image ocel/shop-web:2222",
	)
}
