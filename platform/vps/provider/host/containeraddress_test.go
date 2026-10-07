package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestAContainersAddressIsTheOneItHoldsOnItsNetwork(t *testing.T) {
	rig := machine(nil)
	rig.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker inspect") && strings.Contains(command, quoted("shop-infra-orders-postgres")) {
			return session.Result{Stdout: "172.18.0.5 \n"}, true
		}
		return session.Result{}, false
	}

	address, err := rig.host().ReadContainerAddress(context.Background(), "shop-infra-orders-postgres")
	if err != nil || address != "172.18.0.5" {
		t.Errorf("ReadContainerAddress() = %q, %v, want 172.18.0.5", address, err)
	}
}

func TestAContainerWithNoAddressIsRefusedNamingIt(t *testing.T) {
	rig := machine(nil)
	rig.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker inspect") {
			return session.Result{Stdout: " \n"}, true
		}
		return session.Result{}, false
	}

	_, err := rig.host().ReadContainerAddress(context.Background(), "shop-infra-orders-postgres")
	if err == nil || !strings.Contains(err.Error(), "shop-infra-orders-postgres") || !strings.Contains(err.Error(), "not running") {
		t.Errorf("ReadContainerAddress() of a container on no network = %v, want a refusal naming it and saying it is not running", err)
	}
	if strings.Contains(err.Error(), "deploy again") {
		t.Errorf("ReadContainerAddress() = %v, want no advice to deploy again: a build is forwarded to before its deploy, so every deploy would meet the same refusal", err)
	}
}
