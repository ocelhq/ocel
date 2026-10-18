package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestAContainersAddressIsTheOneItHoldsOnItsNetwork(t *testing.T) {
	rig := newBench(nil)
	rig.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, "docker inspect") && strings.Contains(command, shell.Quote("shop-infra-orders-postgres")) {
			return ssh.Result{Stdout: "172.18.0.5 \n"}, true
		}
		return ssh.Result{}, false
	}

	address, err := rig.host().containers.ReadAddress(context.Background(), "shop-infra-orders-postgres")
	if err != nil || address != "172.18.0.5" {
		t.Errorf("ReadContainerAddress() = %q, %v, want 172.18.0.5", address, err)
	}
}

func TestAContainerWithNoAddressIsRefusedNamingIt(t *testing.T) {
	rig := newBench(nil)
	rig.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, "docker inspect") {
			return ssh.Result{Stdout: " \n"}, true
		}
		return ssh.Result{}, false
	}

	_, err := rig.host().containers.ReadAddress(context.Background(), "shop-infra-orders-postgres")
	if err == nil || !strings.Contains(err.Error(), "shop-infra-orders-postgres") || !strings.Contains(err.Error(), "not running") {
		t.Errorf("ReadContainerAddress() of a container on no network = %v, want a refusal naming it and saying it is not running", err)
	}
	if strings.Contains(err.Error(), "deploy again") {
		t.Errorf("ReadContainerAddress() = %v, want no advice to deploy again: a build is forwarded to before its deploy, so every deploy would meet the same refusal", err)
	}
}
