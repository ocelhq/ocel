//go:build unix

package enginetest

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

const heldSlotDir = "OCEL_ENGINETEST_HOLD_SLOT_IN"

func leased(t *testing.T, dir string, count int) (int, *os.File) {
	t.Helper()
	slot, held, err := leaseSlot(dir, count)
	if err != nil {
		t.Fatalf("leaseSlot = %v", err)
	}
	return slot, held
}

func TestARunLeasesTheFirstFreeSlotAndALaterRunTakesItBack(t *testing.T) {
	dir := t.TempDir()

	first, held := leased(t, dir, 3)
	second, other := leased(t, dir, 3)
	defer other.Close()
	if first != 0 || second != 1 {
		t.Fatalf("two runs at once leased slots %d and %d, want 0 and 1: runs that share a slot share its networks and resolve each other's switchboard", first, second)
	}
	held.Close()
	if again, held := leased(t, dir, 3); again != 0 {
		t.Errorf("a run after the first ended leased slot %d, want 0: every slot a later run skips is another set of networks left on the machine", again)
	} else {
		held.Close()
	}
}

func TestAChildHoldsASlotAndDies(t *testing.T) {
	dir := os.Getenv(heldSlotDir)
	if dir == "" {
		t.Skip("run only as the child of the test that kills it")
	}
	if _, _, err := leaseSlot(dir, 1); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestASlotIsFreedWhenTheRunHoldingItDies(t *testing.T) {
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestAChildHoldsASlotAndDies$")
	child.Env = append(os.Environ(), heldSlotDir+"="+dir)
	if said, err := child.CombinedOutput(); err != nil {
		t.Fatalf("the child that holds the slot: %v\n%s", err, said)
	}

	got := make(chan int, 1)
	go func() {
		slot, held, err := leaseSlot(dir, 1)
		if err == nil {
			defer held.Close()
		}
		got <- slot
	}()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the only slot was still held after the run holding it died: a killed run would then keep its networks from every run after it")
	}
}

func TestWhenEverySlotIsHeldTheNextRunWaitsForOne(t *testing.T) {
	dir := t.TempDir()
	_, held := leased(t, dir, 1)

	got := make(chan error, 1)
	go func() {
		_, other, err := leaseSlot(dir, 1)
		if err == nil {
			defer other.Close()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("a run leased a slot another run holds (err = %v): two runs would share the slot's networks", err)
	case <-time.After(300 * time.Millisecond):
	}
	held.Close()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("the waiting run's lease = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting run never leased the slot the other run gave back")
	}
}
