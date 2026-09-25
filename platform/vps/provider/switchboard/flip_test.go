package switchboard_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type drains struct {
	mu   sync.Mutex
	told []string
}

func (d *drains) tell(drain switchboard.Drain) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.told = append(d.told, drain.String())
}

func (d *drains) lines() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.told)
}

type holding struct {
	address string
	arrived chan struct{}
	release chan struct{}
}

func holdingBackend(t *testing.T, name string) holding {
	t.Helper()
	held := holding{arrived: make(chan struct{}, 16), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/held" {
			held.arrived <- struct{}{}
			<-held.release
		}
		_, _ = io.WriteString(w, name)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-held.release:
		default:
			close(held.release)
		}
	})
	held.address = strings.TrimPrefix(server.URL, "http://")
	return held
}

func asking(at, host, path string) {
	request, err := http.NewRequest(http.MethodGet, "http://"+at+path, nil)
	if err != nil {
		return
	}
	request.Host = host
	if said, err := http.DefaultClient.Do(request); err == nil {
		_, _ = io.Copy(io.Discard, said.Body)
		_ = said.Body.Close()
	}
}

func TestAFlipUnderSustainedLoadDropsNothingAndEverythingAskedAfterItIsServedByTheNewUpstream(t *testing.T) {
	t.Parallel()

	for _, keepAlive := range []bool{true, false} {
		blue, green := backend(t, "blue"), backend(t, "green")
		board, at := standing(t, routing(t, map[string]string{"shop.example.com": blue}))
		flipped := tableAt(t, routing(t, map[string]string{"shop.example.com": green}))

		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: !keepAlive, MaxIdleConnsPerHost: 16}}
		var done atomic.Int64
		stop := make(chan struct{})
		var asked sync.WaitGroup
		var failed, stale, served atomic.Int64
		for range 16 {
			asked.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					started := time.Now().UnixNano()
					request, _ := http.NewRequest(http.MethodGet, "http://"+at+"/", nil)
					request.Host = "shop.example.com"
					said, err := client.Do(request)
					if err != nil {
						failed.Add(1)
						continue
					}
					body, err := io.ReadAll(said.Body)
					_ = said.Body.Close()
					if err != nil || said.StatusCode != http.StatusOK {
						failed.Add(1)
						continue
					}
					served.Add(1)
					if flippedAt := done.Load(); flippedAt != 0 && started > flippedAt && string(body) != "green" {
						stale.Add(1)
					}
				}
			})
		}
		for served.Load() < 500 {
			time.Sleep(time.Millisecond)
		}
		var told drains
		if err := board.Flip(t.Context(), flipped, []string{blue}, 10*time.Second, told.tell); err != nil {
			t.Fatal(err)
		}
		done.Store(time.Now().UnixNano())
		before := served.Load()
		for served.Load() < before+500 {
			time.Sleep(time.Millisecond)
		}
		close(stop)
		asked.Wait()

		if failed.Load() != 0 {
			t.Errorf("keep-alive %t: %d of %d requests failed across the flip, want none", keepAlive, failed.Load(), served.Load()+failed.Load())
		}
		if stale.Load() != 0 {
			t.Errorf("keep-alive %t: %d requests asked after the flip returned were served by the retiree, want none", keepAlive, stale.Load())
		}
		if lines := told.lines(); !slices.Equal(lines, []string{caddyadmin.Drained + " " + blue}) {
			t.Errorf("keep-alive %t: the flip told %v, want %s drained", keepAlive, lines, blue)
		}
	}
}

func TestADrainIsAcknowledgedOnlyOnceTheLastRequestOnTheRetireeHasReturned(t *testing.T) {
	t.Parallel()

	blue, green := holdingBackend(t, "blue"), backend(t, "green")
	board, at := standing(t, routing(t, map[string]string{"shop.example.com": blue.address}))

	held := make(chan answered, 1)
	go func() { held <- ask(t, http.DefaultClient, at, "shop.example.com", "/held") }()
	<-blue.arrived

	var told drains
	flipped := make(chan error, 1)
	go func() {
		flipped <- board.Flip(t.Context(), tableAt(t, routing(t, map[string]string{"shop.example.com": green})), []string{blue.address}, 30*time.Second, told.tell)
	}()
	time.Sleep(500 * time.Millisecond)
	if lines := told.lines(); len(lines) != 0 {
		t.Fatalf("the flip told %v while a request was still open on the retiree, want nothing yet", lines)
	}
	if said := ask(t, http.DefaultClient, at, "shop.example.com", "/"); said.body != "green" {
		t.Errorf("a request during the drain was served by %q, want green: the switch is made before the drain starts", said.body)
	}
	close(blue.release)
	if said := <-held; said.status != http.StatusOK || said.body != "blue" {
		t.Errorf("the held request answered %d %q, want the retiree's own 200 once it returned", said.status, said.body)
	}
	select {
	case err := <-flipped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the flip never returned after the retiree's last request did")
	}
	if lines := told.lines(); !slices.Equal(lines, []string{caddyadmin.Drained + " " + blue.address}) {
		t.Errorf("the flip told %v, want %s drained", lines, blue.address)
	}
}

func TestADrainWhoseCeilingPassesFirstNamesTheRetireeAndWhatItStillHeld(t *testing.T) {
	t.Parallel()

	blue, green := holdingBackend(t, "blue"), backend(t, "green")
	board, at := standing(t, routing(t, map[string]string{"shop.example.com": blue.address}))
	for range 2 {
		go asking(at, "shop.example.com", "/held")
		<-blue.arrived
	}

	var told drains
	started := time.Now()
	if err := board.Flip(t.Context(), tableAt(t, routing(t, map[string]string{"shop.example.com": green})), []string{blue.address}, time.Second, told.tell); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took < time.Second || took > 3*time.Second {
		t.Errorf("the flip returned after %s, want it to wait out its one second ceiling and no longer", took)
	}
	if lines := told.lines(); !slices.Equal(lines, []string{caddyadmin.DrainExpired + " " + blue.address + " 2"}) {
		t.Errorf("the flip told %v, want %s %s 2", lines, caddyadmin.DrainExpired, blue.address)
	}
}

func TestAFlipThatCannotReadItsTableRetiresNothingAndSwitchesNothing(t *testing.T) {
	t.Parallel()

	blue := backend(t, "blue")
	board, at := standing(t, routing(t, map[string]string{"shop.example.com": blue}))

	var told drains
	if err := board.Flip(t.Context(), tableAt(t, []byte(`{"grace":"soon"}`)), []string{blue}, time.Second, told.tell); err == nil {
		t.Fatal("a flip onto a table that cannot be read was taken, want it refused")
	}
	if err := board.Flip(t.Context(), tableAt(t, routing(t, map[string]string{})), []string{"blue"}, time.Second, told.tell); err == nil {
		t.Fatal("a flip retiring an address with no port was taken, want it refused before it switches anything")
	}
	if lines := told.lines(); len(lines) != 0 {
		t.Errorf("the refused flips told %v, want nothing drained", lines)
	}
	if said := ask(t, http.DefaultClient, at, "shop.example.com", "/"); said.body != "blue" {
		t.Errorf("after the refused flips shop.example.com answered %q, want blue still serving", said.body)
	}
	if idle, err := board.Idle([]string{blue}); err != nil || len(idle) != 0 {
		t.Errorf("Idle(%s) = %v, %v after refused flips, want it still routed", blue, idle, err)
	}
	if idle, err := board.Idle([]string{"blue"}); err == nil {
		t.Errorf("Idle(blue) = %v, want it refused: an address with no port keys no route and no drain, so it would read as idle whatever holds it", idle)
	}
}
