package host

import (
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

func executable(t *testing.T, path, body string) {
	t.Helper()
	runnable(t, path, []byte(body), 0o755)
}

func runnable(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

type misses struct {
	held  chan string
	count atomic.Int64
}

func newMisses() *misses { return &misses{held: make(chan string, 64)} }

func (m *misses) progress(what string) {
	m.count.Add(1)
	select {
	case m.held <- what:
	default:
	}
}

func (m *misses) tally() (int, string) {
	select {
	case first := <-m.held:
		return int(m.count.Load()), first
	default:
		return int(m.count.Load()), ""
	}
}

func TestEveryMissedCycleIsCountedAndNotJustTheOnesThatFitTheReport(t *testing.T) {
	t.Parallel()

	missed := newMisses()
	var reporting sync.WaitGroup
	for worker := range 8 {
		reporting.Add(1)
		go func() {
			defer reporting.Done()
			for range 150 {
				missed.progress("worker " + string(rune('a'+worker)) + " read no account")
			}
		}()
	}
	reporting.Wait()

	count, first := missed.tally()
	if count != 8*150 {
		t.Errorf("%d of %d misses were counted, and a failure that under-reports its own size is one nobody sizes the fix against", count, 8*150)
	}
	if first == "" {
		t.Error("the tally quoted none of the misses it counted, so the failure says how many without saying what")
	}
}

func TestAStubTheHarnessWritesRunsWhileTheRestOfTheSuiteForks(t *testing.T) {
	t.Parallel()

	var forking atomic.Bool
	forking.Store(true)
	var busy sync.WaitGroup
	for range 8 {
		busy.Add(1)
		go func() {
			defer busy.Done()
			for forking.Load() {
				_ = exec.Command("/bin/sh", "-c", ":").Run()
			}
		}()
	}
	t.Cleanup(func() {
		forking.Store(false)
		busy.Wait()
	})

	missed := newMisses()
	var surveying sync.WaitGroup
	for range 8 {
		surveying.Add(1)
		go func() {
			defer surveying.Done()
			for range 150 {
				held := standing()
				cmd := exec.Command("/bin/sh", "-c", deployLogin().survey())
				cmd.Env = append(os.Environ(), "PATH="+stubs(t, &held)+":"+os.Getenv("PATH"))
				rendered, err := cmd.CombinedOutput()
				if err != nil {
					missed.progress(string(rendered))
					continue
				}
				observed, _, err := readSurvey(string(rendered))
				if err != nil {
					missed.progress(err.Error())
					continue
				}
				if _, stood := observed[principal().ID()]; !stood {
					missed.progress("the survey read no account where one stands")
				}
			}
		}()
	}
	surveying.Wait()

	if count, first := missed.tally(); count > 0 {
		t.Errorf("%d of the harness's %d write-then-exec cycles read a host carrying no account, and every assertion the stubs carry is unreadable the same way. The first of them said: %s",
			count, 8*150, first)
	}
}
