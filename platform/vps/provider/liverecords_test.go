package vps_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
