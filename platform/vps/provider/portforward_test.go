package vps_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestAPostgresOrKVBindingIsForwardedFromLoopbackToItsContainersAddressOnTheBox(t *testing.T) {
	t.Parallel()

	machine := &box{refuses: func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "docker inspect") && strings.Contains(command, "shop-infra-orders-postgres"):
			return session.Result{Stdout: "172.18.0.5 \n"}, true
		case strings.Contains(command, "docker inspect") && strings.Contains(command, "shop-infra-cache-kv"):
			return session.Result{Stdout: "172.18.0.6 \n"}, true
		}
		return session.Result{}, false
	}}

	forwards, err := over(machine).Hooks().ForwardPorts(context.Background(), provider.PortForwardRequest{
		Bindings: []provider.Binding{
			{Type: provider.BindingPostgres, Name: "orders", Properties: map[string]string{provider.PropertyHost: "shop-infra-orders-postgres", provider.PropertyPort: "5432"}},
			{Type: provider.BindingKV, Name: "cache", Properties: map[string]string{provider.PropertyHost: "shop-infra-cache-kv", provider.PropertyPort: "6379"}},
		},
	})
	if err != nil {
		t.Fatalf("ForwardPorts() = %v", err)
	}

	if want := []string{"172.18.0.5:5432", "172.18.0.6:6379"}; !slices.Equal(machine.forwardedTo(), want) {
		t.Errorf("the box forwarded to %v, want each container's own address and port %v", machine.forwardedTo(), want)
	}
	if len(forwards) != 2 || forwards[0].Binding != "orders" || forwards[1].Binding != "cache" ||
		!strings.HasPrefix(forwards[0].LocalAddress, "127.0.0.1:") {
		t.Errorf("ForwardPorts() = %+v, want orders and cache each on a loopback port", forwards)
	}
}
