package main

import "testing"

func TestATasksAddressThatListensEverywhereIsRefused(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:7001", "[::]:7001", ":7001", "127.0.0.1:7001,0.0.0.0:7001"} {
		t.Run(listen, func(t *testing.T) {
			if _, err := tasksAddresses(listen); err == nil {
				t.Errorf("tasksAddresses(%q) accepted an address that listens on every interface", listen)
			}
		})
	}
}

func TestTasksAddressesOnLoopbackAndTheGatewayAtOnePortAreAccepted(t *testing.T) {
	got, err := tasksAddresses("127.0.0.1:7001,172.17.0.1:7001")

	if err != nil || len(got) != 2 || got[0] != "127.0.0.1:7001" || got[1] != "172.17.0.1:7001" {
		t.Errorf("tasksAddresses() = %v, %v; want both addresses", got, err)
	}
}

func TestTasksAddressesAtDifferentPortsAreRefused(t *testing.T) {
	if _, err := tasksAddresses("127.0.0.1:7001,172.17.0.1:7002"); err == nil {
		t.Error("tasksAddresses() accepted two ports, and a service reaches both through one")
	}
}
