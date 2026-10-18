package gate

import (
	"runtime"
	"testing"
	"time"
)

func TestADerivationWaitsWhileEverySlotIsTaken(t *testing.T) {
	held := Gate{Key: "k", PasswordHash: HashPassword("waits for a slot")}
	for range cap(derivationSlots) {
		derivationSlots <- struct{}{}
	}
	checked := make(chan bool)
	go func() { checked <- held.HasPassword("waits for a slot") }()

	select {
	case <-checked:
		t.Fatal("a password was derived while every slot was taken")
	case <-time.After(300 * time.Millisecond):
	}
	<-derivationSlots
	select {
	case matched := <-checked:
		if !matched {
			t.Error("the password its hash was made from was refused once a slot freed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the derivation never ran after a slot freed")
	}
	for range cap(derivationSlots) - 1 {
		<-derivationSlots
	}
}

func TestAGateKeepsADerivationSlotForEveryOtherCPU(t *testing.T) {
	if want := max(1, runtime.GOMAXPROCS(0)/2); cap(derivationSlots) != want {
		t.Errorf("the gate has %d derivation slots, want %d: each derivation holds 16 MiB and a CPU", cap(derivationSlots), want)
	}
}
