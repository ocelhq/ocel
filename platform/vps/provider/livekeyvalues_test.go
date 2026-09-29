package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func TestLiveTheDeployPrincipalReadsAndWritesTheEntriesARootBootstrapWrote(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	bootstrapped(t, vm, environment.TierProduction)

	store := vm.deploying(t).KeyValues()
	name := stackrecords.ProjectKey(environment.TierProduction, "keyvalues-induction")
	ctx := context.Background()

	entry, err := keyvalue.ReadOrEmpty(ctx, store, name)
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

func TestLiveTheRootKeyValueHelperHandsOwnershipToNothingItDidNotCreate(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	bootstrapped(t, vm, environment.TierProduction)

	const victim = "/tmp/keyvalues-victim"
	vm.ssh(t, "sudo install -m 0644 -o root -g root /etc/hostname "+victim)
	t.Cleanup(func() { vm.ssh(t, "sudo rm -f "+victim) })
	if owner := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%U:%G' "+victim)); owner != "root:root" {
		t.Fatalf("%s reads %q before the helper is driven at all, so nothing it reads afterwards is a claim about what the helper did", victim, owner)
	}

	tier := host.KeyValuesDir(environment.TierProduction)
	lock := tier + "/.lock"
	vm.sshAs(t, deployLogin, "rm -f "+quote(lock)+" && ln -sf "+victim+" "+quote(lock))
	if target := strings.TrimSpace(vm.ssh(t, "sudo readlink "+quote(lock))); target != victim {
		t.Fatalf("%s points at %q, so the login that deploys cannot plant a name inside the tier and nothing below proves anything", lock, target)
	}
	if wrote, err := vm.attempt(vm.user, "printf '\"one\"\\n' | sudo "+keyValuesHelper+" production write app/one ''"); err == nil {
		t.Errorf("the key-value helper took the lock through a symlink %s planted and answered %q", deployLogin, strings.TrimSpace(wrote))
	}
	if owner := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%U:%G' "+victim)); owner != "root:root" {
		t.Errorf("%s belongs to %q after root drove the key-value helper over a symlink %s planted at %s, and the one login on this box that cannot elevate must not be handed a file root owns",
			victim, owner, deployLogin, lock)
	}
	vm.sshAs(t, deployLogin, "rm -f "+quote(lock))

	planted := tier + "/planted.json"
	vm.sshAs(t, deployLogin, "ln -sf "+victim+" "+quote(planted))
	if read, err := vm.attempt(vm.user, "sudo "+keyValuesHelper+" production read planted"); err == nil {
		t.Errorf("the key-value helper read %s through a symlink %s planted and answered %q", victim, deployLogin, strings.TrimSpace(read))
	}

	if climbed, err := vm.attempt(vm.user, "sudo "+keyValuesHelper+" production list .."); err == nil {
		t.Errorf("the key-value helper listed %q, and it keeps entries under one tier and walks out of none", strings.TrimSpace(climbed))
	}
}

func TestLiveAnEntryTheHelperCouldNotHandOverIsAnEntryItNeverWrote(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	bootstrapped(t, vm, environment.TierProduction)

	const failing = "/tmp/keyvalues-failing"
	vm.ssh(t, "sudo install -d -m 0755 "+failing)
	vm.feeds(t, "sudo install -m 0755 /dev/stdin "+failing+"/chown",
		[]byte("#!/bin/sh\nfor arg; do case $arg in *.lock) exec /bin/chown \"$@\" ;; esac; done\nexit 1\n"))
	t.Cleanup(func() { vm.ssh(t, "sudo rm -rf "+failing) })

	minted := strings.TrimSpace(vm.sshAs(t, vm.user, "printf '\"one\"\\n' | sudo "+keyValuesHelper+" production write app/one ''"))
	if len(minted) != 32 {
		t.Fatalf("the key-value helper minted %q for a first write, so there is no revision the writes below can be compared against", minted)
	}

	if wrote, err := vm.attempt(vm.user, "printf '\"two\"\\n' | sudo env PATH="+failing+":$PATH "+keyValuesHelper+
		" production write app/one "+minted); err == nil {
		t.Fatalf("a write whose chown failed reported the revision %q it minted, so the caller would learn one this box never handed over", strings.TrimSpace(wrote))
	}

	if _, err := vm.attempt(vm.user, "printf '\"three\"\\n' | sudo "+keyValuesHelper+" production write app/one "+minted); err != nil {
		t.Errorf("the write that follows a failed one = %v, want it taken against the revision the caller still has: a helper that writes the entry and then reports failure wedges it at a revision nothing knows, and every write after it is refused as stale", err)
	}
}
