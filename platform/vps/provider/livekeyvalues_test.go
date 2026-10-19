//go:build integration

package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func TestLiveTheDeployPrincipalReadsAndWritesTheEntriesARootBootstrapWrote(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	bootstrapped(t, vm, environment.TierProduction)

	store := vm.deploying(t).KeyValues()
	name := stackrecords.ProjectKey(environment.TierProduction, "keyvalues-induction")
	ctx := context.Background()

	entry, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		t.Fatalf("read %s as %s = %v, want the tier a bootstrap wrote as root readable by the login every deploy runs as: the whole deploy path reads before it writes, so a tier this login cannot open is a box nothing can deploy to",
			name, deployLogin, err)
	}
	entry.Value = []byte(`{}`)
	if _, err := store.Write(ctx, entry); err != nil {
		t.Fatalf("write %s as %s = %v, want the key-value tier a bootstrap wrote as root writable by the login every deploy runs as", name, deployLogin, err)
	}
	read, err := store.Read(ctx, name)
	if err != nil {
		t.Fatalf("read back %s as %s = %v", name, deployLogin, err)
	}
	if string(read.Value) != `{}` {
		t.Errorf("%s reads %q after %s wrote it", name, read.Value, deployLogin)
	}
}

func TestLiveTheRootRecordsCommandHandsOwnershipToNothingItDidNotCreate(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	bootstrapped(t, vm, environment.TierProduction)

	const victim = "/tmp/keyvalues-victim"
	vm.ssh(t, "sudo install -m 0644 -o root -g root /etc/hostname "+victim)
	t.Cleanup(func() { vm.ssh(t, "sudo rm -f "+victim) })
	if owner := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%U:%G' "+victim)); owner != "root:root" {
		t.Fatalf("%s reads %q before the records command is driven at all, so nothing it reads afterwards is a claim about what the command did", victim, owner)
	}

	records := "sudo " + boxapi.LiveBinary + " " + boxapi.RecordsCommand + " " + string(environment.TierProduction)
	tier := host.KeyValuesDir(environment.TierProduction)
	lock := tier + "/.lock"
	vm.sshAs(t, deployLogin, "rm -f "+quote(lock)+" && ln -sf "+victim+" "+quote(lock))
	if target := strings.TrimSpace(vm.ssh(t, "sudo readlink "+quote(lock))); target != victim {
		t.Fatalf("%s points at %q, so the login that deploys cannot plant a name inside the tier and nothing below proves anything", lock, target)
	}
	if wrote, err := vm.attempt(vm.user, `printf '[{"path":"app/one","revision":"","value":"one"}]' | `+records+" "+boxapi.RecordsVerbCompareAndSet); err == nil {
		t.Errorf("the records command took the lock through a symlink %s planted and answered %q", deployLogin, strings.TrimSpace(wrote))
	}
	if owner := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%U:%G' "+victim)); owner != "root:root" {
		t.Errorf("%s belongs to %q after root drove the records command over a symlink %s planted at %s, and the one login on this box that cannot elevate must not be handed a file root owns",
			victim, owner, deployLogin, lock)
	}
	vm.sshAs(t, deployLogin, "rm -f "+quote(lock))

	planted := tier + "/planted.json"
	vm.sshAs(t, deployLogin, "ln -sf "+victim+" "+quote(planted))
	if read, err := vm.attempt(vm.user, records+" "+boxapi.RecordsVerbGet+" planted"); err == nil {
		t.Errorf("the records command read %s through a symlink %s planted and answered %q", victim, deployLogin, strings.TrimSpace(read))
	}

	if climbed, err := vm.attempt(vm.user, records+" "+boxapi.RecordsVerbList+" .."); err == nil {
		t.Errorf("the records command listed %q, and it keeps entries under one tier and walks out of none", strings.TrimSpace(climbed))
	}
}
