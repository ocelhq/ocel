package deploy

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func namedApps(n int) []*deploymentsv1.ManifestApp {
	apps := make([]*deploymentsv1.ManifestApp, n)
	for i := range apps {
		apps[i] = &deploymentsv1.ManifestApp{Name: fmt.Sprintf("app-%d", i)}
	}
	return apps
}

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
		runAppStacks(namedApps(n), func(i int, _ *deploymentsv1.ManifestApp) {
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

	select {
	case <-saturated:
	case <-time.After(5 * time.Second):
		close(release)
		mu.Lock()
		got := peak
		mu.Unlock()
		t.Fatalf("never reached %d concurrent workers (peak %d): the limit is below appConcurrency", appConcurrency, got)
	}

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

func TestRunAppStacksBelowLimitRunsFullyConcurrently(t *testing.T) {
	n := appConcurrency

	var all sync.WaitGroup
	all.Add(n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAppStacks(namedApps(n), func(int, *deploymentsv1.ManifestApp) {
			all.Done()
			all.Wait()
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%d workers did not all run concurrently", n)
	}
}

func TestRunAppStacksRunsEveryAppExactlyOnceAtItsOwnIndex(t *testing.T) {
	n := 5*appConcurrency + 1
	apps := namedApps(n)
	ran := make([]int32, n)
	got := make([]*deploymentsv1.ManifestApp, n)

	runAppStacks(apps, func(i int, app *deploymentsv1.ManifestApp) {
		atomic.AddInt32(&ran[i], 1)
		got[i] = app
	})

	for i, c := range ran {
		if c != 1 {
			t.Errorf("index %d ran %d times, want exactly 1", i, c)
		}
		if got[i] != apps[i] {
			t.Errorf("index %d got app %q, want %q", i, got[i].GetName(), apps[i].GetName())
		}
	}
}
