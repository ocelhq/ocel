package deploy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pinWindow is how long the test holds appConcurrency workers pinned while
// watching for an over-admission. Proving nothing more was admitted needs a
// window; an unbounded fan-out submits every remaining worker in the time it
// takes to run a for loop, so this is generous by orders of magnitude.
const pinWindow = 250 * time.Millisecond

func TestRunAppStacksAdmitsAtMostAppConcurrency(t *testing.T) {
	n := 3 * appConcurrency

	var mu sync.Mutex
	live, peak := 0, 0
	saturated := make(chan struct{})
	excess := make(chan struct{})
	var once, excessOnce sync.Once
	release := make(chan struct{})

	ran := make([]int32, n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAppStacks(n, func(i int) {
			atomic.AddInt32(&ran[i], 1)

			mu.Lock()
			live++
			if live > peak {
				peak = live
			}
			atSaturation := live == appConcurrency
			over := live > appConcurrency
			mu.Unlock()

			if atSaturation {
				once.Do(func() { close(saturated) })
			}
			if over {
				excessOnce.Do(func() { close(excess) })
			}

			<-release

			mu.Lock()
			live--
			mu.Unlock()
		})
	}()

	// Under-admission: if the limit were below appConcurrency, that many
	// workers could never be live at once and this never fires.
	select {
	case <-saturated:
	case <-time.After(5 * time.Second):
		close(release)
		mu.Lock()
		got := peak
		mu.Unlock()
		t.Fatalf("never reached %d concurrent workers (peak %d): the limit is below appConcurrency", appConcurrency, got)
	}

	// Over-admission: appConcurrency workers are now pinned and cannot retire
	// until release closes, so any further worker entry is a limit violation.
	select {
	case <-excess:
		close(release)
		mu.Lock()
		got := peak
		mu.Unlock()
		t.Fatalf("admitted %d concurrent workers, want at most %d: the limit is not enforced", got, appConcurrency)
	case <-time.After(pinWindow):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runAppStacks did not return after release")
	}

	if peak != appConcurrency {
		t.Errorf("peak concurrency = %d, want %d", peak, appConcurrency)
	}
	for i, c := range ran {
		if c != 1 {
			t.Errorf("app %d ran %d times, want exactly 1", i, c)
		}
	}
}

// A fan-out no larger than the limit must not be serialized by it: every
// worker runs concurrently, which is the behaviour single- and two-app
// deploys have today.
func TestRunAppStacksBelowLimitRunsFullyConcurrently(t *testing.T) {
	n := appConcurrency

	var all sync.WaitGroup
	all.Add(n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAppStacks(n, func(i int) {
			all.Done()
			all.Wait() // deadlocks unless all n are live at once
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%d workers did not all run concurrently", n)
	}
}

func TestRunAppStacksRunsEveryIndexExactlyOnce(t *testing.T) {
	n := 5*appConcurrency + 1
	ran := make([]int32, n)

	runAppStacks(n, func(i int) { atomic.AddInt32(&ran[i], 1) })

	for i, c := range ran {
		if c != 1 {
			t.Errorf("index %d ran %d times, want exactly 1", i, c)
		}
	}
}
