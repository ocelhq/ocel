package mountedgate_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/runtime/mountedgate"
)

type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func TestAGateIsReadFromTheMountedFileAgainOnceTheCacheLapses(t *testing.T) {
	t.Parallel()

	reads := 0
	contents := `{"key":"first","passwordMac":"m"}`
	clk := &clock{at: time.Unix(1700000000, 0)}
	source := mountedgate.New(func() ([]byte, error) { reads++; return []byte(contents), nil }, clk.now)

	for range 3 {
		if held, err := source.Current(); err != nil || held == nil || held.Key != "first" {
			t.Fatalf("Current = %+v, %v, want the first gate", held, err)
		}
	}
	if reads != 1 {
		t.Errorf("read the mount %d times inside the cache window, want 1", reads)
	}

	contents = `{"key":"rotated","passwordMac":"m"}`
	clk.at = clk.at.Add(mountedgate.CacheFor)
	if held, err := source.Current(); err != nil || held == nil || held.Key != "rotated" {
		t.Errorf("Current after the cache lapsed = %+v, %v, want the rotated gate", held, err)
	}
}

func TestAMountThatCannotBeReadIsAnErrorAndRecoversWhenItCan(t *testing.T) {
	t.Parallel()

	failure := errors.New("the mount is gone")
	clk := &clock{at: time.Unix(1700000000, 0)}
	source := mountedgate.New(func() ([]byte, error) {
		if failure != nil {
			return nil, failure
		}
		return []byte(`{"key":"k","passwordMac":"m"}`), nil
	}, clk.now)

	if held, err := source.Current(); err == nil || held != nil {
		t.Fatalf("Current = %+v, %v, want an error: a gated service with no readable gate serves no one", held, err)
	}
	failure = nil
	clk.at = clk.at.Add(mountedgate.CacheFor)
	if held, err := source.Current(); err != nil || held == nil {
		t.Errorf("Current once the mount reads = %+v, %v, want the gate", held, err)
	}
}

func TestAMalformedMountIsAnError(t *testing.T) {
	t.Parallel()

	source := mountedgate.New(func() ([]byte, error) { return []byte("{"), nil }, time.Now)

	if held, err := source.Current(); !errors.Is(err, gate.ErrMountedGateUnreadable) || held != nil {
		t.Errorf("Current = %+v, %v, want the unreadable error", held, err)
	}
}

func TestAWithdrawnMountIsNoGate(t *testing.T) {
	t.Parallel()

	source := mountedgate.New(func() ([]byte, error) { return []byte("null\n"), nil }, time.Now)

	if held, err := source.Current(); err != nil || held != nil {
		t.Errorf("Current = %+v, %v, want no gate and no error", held, err)
	}
}

func TestTheEnvironmentNamesTheMountedFileAndTheServiceIsGatedFromIt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "gate")
	if err := os.WriteFile(path, []byte(`{"key":"k","passwordMac":"m"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	source, kept := mountedgate.FromEnv([]string{"A=1", gate.MountedFileVar + "=" + path, "B=2"})

	if source == nil {
		t.Fatal("FromEnv = nil, want a source for the named file")
	}
	if len(kept) != 2 || kept[0] != "A=1" || kept[1] != "B=2" {
		t.Errorf("kept = %v, want the variables the app may see, without the gate's", kept)
	}
	if held, err := source.Current(); err != nil || held == nil || held.Key != "k" {
		t.Errorf("Current = %+v, %v, want the file's gate", held, err)
	}
}

func TestAnInlineGateIsTheGateWhenNoFileIsNamed(t *testing.T) {
	t.Parallel()

	source, kept := mountedgate.FromEnv([]string{gate.MountedInlineVar + `={"key":"inline","passwordMac":"m"}`})

	if len(kept) != 0 {
		t.Errorf("kept = %v, want the gate withheld from the app", kept)
	}
	if held, err := source.Current(); err != nil || held == nil || held.Key != "inline" {
		t.Errorf("Current = %+v, %v, want the inline gate", held, err)
	}
}

func TestAReadThatHangsFailsClosedOnceItsTimeoutPasses(t *testing.T) {
	t.Parallel()

	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	source := mountedgate.New(func() ([]byte, error) { <-unblock; return []byte(`{"key":"k","passwordMac":"m"}`), nil }, time.Now)
	source.SetReadTimeout(20 * time.Millisecond)

	answered := make(chan error, 1)
	go func() {
		held, err := source.Current()
		if held != nil {
			err = errors.New("a gate was served from a read that never finished")
		}
		answered <- err
	}()

	select {
	case err := <-answered:
		if !errors.Is(err, mountedgate.ErrReadTimedOut) {
			t.Errorf("Current = %v, want %v: a gated service whose gate cannot be read in time serves no one", err, mountedgate.ErrReadTimedOut)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Current never returned while the read hung, and every request waits on it")
	}
}

func TestRequestsThatArriveDuringARefreshShareOneRead(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	unblock := make(chan struct{})
	source := mountedgate.New(func() ([]byte, error) {
		reads.Add(1)
		<-unblock
		return []byte(`{"key":"k","passwordMac":"m"}`), nil
	}, time.Now)
	source.SetReadTimeout(5 * time.Second)

	var wg sync.WaitGroup
	keys := make(chan string, 5)
	for range 5 {
		wg.Go(func() {
			held, err := source.Current()
			if err != nil || held == nil {
				keys <- fmt.Sprint(err)
				return
			}
			keys <- held.Key
		})
	}
	for reads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(unblock)
	wg.Wait()
	close(keys)

	for key := range keys {
		if key != "k" {
			t.Errorf("a request read %q, want the gate", key)
		}
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("read the mount %d times for requests that arrived together, want 1", got)
	}
}

func TestAReadThatTimedOutServesItsGateOnceItFinishes(t *testing.T) {
	t.Parallel()

	unblock := make(chan struct{})
	source := mountedgate.New(func() ([]byte, error) { <-unblock; return []byte(`{"key":"k","passwordMac":"m"}`), nil }, time.Now)
	source.SetReadTimeout(20 * time.Millisecond)

	if _, err := source.Current(); !errors.Is(err, mountedgate.ErrReadTimedOut) {
		t.Fatalf("Current = %v, want %v", err, mountedgate.ErrReadTimedOut)
	}
	close(unblock)
	source.SetReadTimeout(5 * time.Second)

	if held, err := source.Current(); err != nil || held == nil || held.Key != "k" {
		t.Errorf("Current once the read finished = %+v, %v, want the gate", held, err)
	}
}

func TestAnEnvironmentThatNamesNoGateLeavesTheServiceUngated(t *testing.T) {
	t.Parallel()

	if source, _ := mountedgate.FromEnv([]string{"A=1"}); source != nil {
		t.Error("FromEnv = a source, want nil: nothing asked this service to gate")
	}
}
