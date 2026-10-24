//go:build unix

package enginetest

import (
	"strings"
	"testing"
	"time"
)

func TestATestTakingHostPortsAnotherTestHoldsWaitsUntilThatTestsCleanupRuns(t *testing.T) {
	dir := t.TempDir()
	waitedOn := make(chan string, 2)
	got := make(chan error, 1)

	t.Run("the holder", func(t *testing.T) {
		hostPortsIn(t, dir, []string{"80", "443"})
		go func() {
			release, err := holdHostPorts(dir, "the second run", []string{"443", "80"}, time.Time{}, func(_, other string) { waitedOn <- other })
			if err == nil {
				defer release()
			}
			got <- err
		}()
		select {
		case err := <-got:
			t.Fatalf("a second run took host ports another run holds (err = %v): both publish the box's proxy on them, and docker refuses the second with port is already allocated", err)
		case <-time.After(500 * time.Millisecond):
		}
		if other := <-waitedOn; !strings.Contains(other, t.Name()) {
			t.Errorf("the waiting run read the ports as held by %q, want the test holding them, %q: a run left waiting cannot tell which run to wait for", other, t.Name())
		}
	})

	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("the waiting run's hold = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting run never took the host ports after the holding test's cleanup ran")
	}
}

func TestAWaitForHostPortsPastItsDeadlineNamesTheRunHoldingThem(t *testing.T) {
	dir := t.TempDir()
	release, err := holdHostPorts(dir, "the first run", []string{"80"}, time.Time{}, func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = holdHostPorts(dir, "the second run", []string{"80"}, time.Now().Add(300*time.Millisecond), func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "the first run") {
		t.Errorf("a wait past its deadline = %v, want a refusal naming the first run: a test killed by its -timeout says nothing of what it waited on", err)
	}
}
