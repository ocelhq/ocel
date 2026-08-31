package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func TestLiveTheDeployPrincipalReadsAndWritesTheRecordsARootBootstrapWrote(t *testing.T) {
	vm := live(t)
	vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel /usr/local/lib/ocel")
	bootstrapped(t, vm, providerkit.ClassProduction)

	records := vm.deploying(t).Records()
	name := providerkit.ProjectRecord(providerkit.ClassProduction, "records-induction")
	ctx := context.Background()

	held, err := providerkit.Held(ctx, records, name)
	if err != nil {
		t.Fatalf("read %s as %s = %v, want the tier a bootstrap wrote as root readable by the login every deploy runs as: the whole deploy path reads before it writes, so a tier this login cannot open is a box nothing can deploy to",
			name, deployLogin, err)
	}
	held.Bytes = []byte(`{}`)
	if _, err := records.Write(ctx, held); err != nil {
		t.Fatalf("write %s as %s = %v, want the record tier a bootstrap wrote as root writable by the login every deploy runs as", name, deployLogin, err)
	}
	read, err := records.Read(ctx, name)
	if err != nil {
		t.Fatalf("read back %s as %s = %v", name, deployLogin, err)
	}
	if string(read.Bytes) != `{}` {
		t.Errorf("%s reads %q after %s wrote it", name, read.Bytes, deployLogin)
	}
}

func TestLiveTheRootRecordsHelperHandsOwnershipToNothingItDidNotCreate(t *testing.T) {
	vm := live(t)
	vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel /usr/local/lib/ocel")
	bootstrapped(t, vm, providerkit.ClassProduction)

	const victim = "/tmp/records-victim"
	vm.ssh(t, "sudo install -m 0644 -o root -g root /etc/hostname "+victim)
	t.Cleanup(func() { vm.ssh(t, "sudo rm -f "+victim) })
	if held := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%U:%G' "+victim)); held != "root:root" {
		t.Fatalf("%s reads %q before the helper is driven at all, so nothing it reads afterwards is a claim about what the helper did", victim, held)
	}

	tier := host.RecordsDir(providerkit.ClassProduction)
	lock := tier + "/.lock"
	vm.sshAs(t, deployLogin, "rm -f "+quote(lock)+" && ln -sf "+victim+" "+quote(lock))
	if held := strings.TrimSpace(vm.ssh(t, "sudo readlink "+quote(lock))); held != victim {
		t.Fatalf("%s points at %q, so the login that deploys cannot plant a name inside the tier and nothing below proves anything", lock, held)
	}
	if wrote, err := vm.attempt(vm.user, "printf aGk= | sudo "+recordsHelper+" production write app/one ''"); err == nil {
		t.Errorf("the records helper took the lock through a symlink %s planted and answered %q", deployLogin, strings.TrimSpace(wrote))
	}
	if held := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%U:%G' "+victim)); held != "root:root" {
		t.Errorf("%s belongs to %q after root drove the records helper over a symlink %s planted at %s, and the one login on this box that cannot elevate must not be handed a file root owns",
			victim, held, deployLogin, lock)
	}
	vm.sshAs(t, deployLogin, "rm -f "+quote(lock))

	planted := tier + "/planted.rec"
	vm.sshAs(t, deployLogin, "ln -sf "+victim+" "+quote(planted))
	if read, err := vm.attempt(vm.user, "sudo "+recordsHelper+" production read planted"); err == nil {
		t.Errorf("the records helper read %s through a symlink %s planted and answered %q", victim, deployLogin, strings.TrimSpace(read))
	}

	if climbed, err := vm.attempt(vm.user, "sudo "+recordsHelper+" production list .."); err == nil {
		t.Errorf("the records helper listed %q, and it keeps records under one tier and walks out of none", strings.TrimSpace(climbed))
	}
}

func TestLiveARecordAHelperCouldNotHandOverIsARecordItNeverFlipped(t *testing.T) {
	vm := live(t)
	vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel /usr/local/lib/ocel")
	bootstrapped(t, vm, providerkit.ClassProduction)

	const failing = "/tmp/records-failing"
	vm.ssh(t, "sudo install -d -m 0755 "+failing)
	vm.feeds(t, "sudo install -m 0755 /dev/stdin "+failing+"/chown",
		[]byte("#!/bin/sh\nfor arg; do case $arg in *.lock) exec /bin/chown \"$@\" ;; esac; done\nexit 1\n"))
	t.Cleanup(func() { vm.ssh(t, "sudo rm -rf "+failing) })

	minted := strings.TrimSpace(vm.sshAs(t, vm.user, "printf aGk= | sudo "+recordsHelper+" production write app/one ''"))
	if len(minted) != 32 {
		t.Fatalf("the records helper minted %q for a first write, so there is no revision the writes below can be compared against", minted)
	}

	if wrote, err := vm.attempt(vm.user, "printf aGk2 | sudo env PATH="+failing+":$PATH "+recordsHelper+
		" production write app/one "+minted); err == nil {
		t.Fatalf("a write whose chown failed reported the revision %q it minted, so the caller would learn one this box never handed over", strings.TrimSpace(wrote))
	}

	if _, err := vm.attempt(vm.user, "printf aGk3 | sudo "+recordsHelper+" production write app/one "+minted); err != nil {
		t.Errorf("the write that follows a failed one = %v, want it taken against the revision the caller still holds: a helper that flips the record and then reports failure wedges it at a revision nothing knows, and every write after it is refused as stale", err)
	}
}
