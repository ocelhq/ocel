package switchboard_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

type stallingBackend struct {
	address string
	arrived chan struct{}
	release chan struct{}
}

func aStallingBackend(t *testing.T, name string) stallingBackend {
	t.Helper()
	stalling := stallingBackend{arrived: make(chan struct{}, 16), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stall" {
			stalling.arrived <- struct{}{}
			<-stalling.release
		}
		_, _ = io.WriteString(w, name)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-stalling.release:
		default:
			close(stalling.release)
		}
	})
	stalling.address = strings.TrimPrefix(server.URL, "http://")
	return stalling
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
		board, at := served(t, routing(t, map[string]string{"shop.example.com": blue}))
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
		if lines := told.lines(); !slices.Equal(lines, []string{switchboard.Drained + " " + blue}) {
			t.Errorf("keep-alive %t: the flip told %v, want %s drained", keepAlive, lines, blue)
		}
	}
}

func TestADrainIsAcknowledgedOnlyOnceTheLastRequestOnTheRetireeHasReturned(t *testing.T) {
	t.Parallel()

	blue, green := aStallingBackend(t, "blue"), backend(t, "green")
	board, at := served(t, routing(t, map[string]string{"shop.example.com": blue.address}))

	stalled := make(chan answered, 1)
	go func() { stalled <- ask(t, http.DefaultClient, at, "shop.example.com", "/stall") }()
	<-blue.arrived

	var told drains
	var released atomic.Bool
	early := make(chan string, 1)
	flipped := make(chan error, 1)
	go func() {
		flipped <- board.Flip(t.Context(), tableAt(t, routing(t, map[string]string{"shop.example.com": green})), []string{blue.address}, 30*time.Second, func(drain switchboard.Drain) {
			if !released.Load() {
				early <- drain.String()
			}
			told.tell(drain)
		})
	}()
	switchedTo(t, at, "shop.example.com", "green")
	released.Store(true)
	close(blue.release)
	if said := <-stalled; said.status != http.StatusOK || said.body != "blue" {
		t.Errorf("the stalled request answered %d %q, want the retiree's own 200 once it returned", said.status, said.body)
	}
	select {
	case err := <-flipped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the flip never returned after the retiree's last request did")
	}
	if lines := told.lines(); !slices.Equal(lines, []string{switchboard.Drained + " " + blue.address}) {
		t.Errorf("the flip told %v, want %s drained", lines, blue.address)
	}
	select {
	case line := <-early:
		t.Errorf("the flip told %q while a request was still open on the retiree, want nothing until it returned", line)
	default:
	}
}

func TestADrainWhoseCeilingPassesFirstNamesTheRetireeAndWhatItStillHadInFlight(t *testing.T) {
	t.Parallel()

	blue, green := aStallingBackend(t, "blue"), backend(t, "green")
	board, at := served(t, routing(t, map[string]string{"shop.example.com": blue.address}))
	for range 2 {
		go asking(at, "shop.example.com", "/stall")
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
	if lines := told.lines(); !slices.Equal(lines, []string{switchboard.DrainExpired + " " + blue.address + " 2"}) {
		t.Errorf("the flip told %v, want %s %s 2", lines, switchboard.DrainExpired, blue.address)
	}
}

func TestAFlipThatCannotReadItsTableRetiresNothingAndSwitchesNothing(t *testing.T) {
	t.Parallel()

	blue := backend(t, "blue")
	board, at := served(t, routing(t, map[string]string{"shop.example.com": blue}))

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
		t.Errorf("Idle(blue) = %v, want it refused: an address with no port keys no route and no drain, so it would read as idle whatever listens on it", idle)
	}
}

func TestADrainCeilingCutsEveryRequestAndStreamStillOpenOnARetireeNoLongerRouted(t *testing.T) {
	t.Parallel()

	arrived, release := make(chan struct{}, 2), make(chan struct{})
	retiree := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: first\n\n")
			_ = http.NewResponseController(w).Flush()
		}
		arrived <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "blue")
	}))
	t.Cleanup(retiree.Close)
	t.Cleanup(func() { close(release) })
	blue, green := strings.TrimPrefix(retiree.URL, "http://"), backend(t, "green")
	board, at := served(t, routing(t, map[string]string{"shop.example.com": blue}))

	stalled := make(chan answered, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, "http://"+at+"/stall", nil)
		request.Host = "shop.example.com"
		said, err := http.DefaultClient.Do(request)
		if err != nil {
			stalled <- answered{}
			return
		}
		defer said.Body.Close()
		body, _ := io.ReadAll(said.Body)
		stalled <- answered{status: said.StatusCode, body: string(body)}
	}()
	request, err := http.NewRequest(http.MethodGet, "http://"+at+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "shop.example.com"
	stream, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	events := bufio.NewReader(stream.Body)
	if first, err := events.ReadString('\n'); err != nil || first != "data: first\n" {
		t.Fatalf("the stream opened with %q, %v, want its first event", first, err)
	}
	<-arrived
	<-arrived

	var told drains
	if err := board.Flip(t.Context(), tableAt(t, routing(t, map[string]string{"shop.example.com": green})), []string{blue}, 200*time.Millisecond, told.tell); err != nil {
		t.Fatal(err)
	}
	if lines := told.lines(); !slices.Equal(lines, []string{switchboard.DrainExpired + " " + blue + " 2"}) {
		t.Errorf("the flip told %v, want %s %s 2", lines, switchboard.DrainExpired, blue)
	}
	select {
	case said := <-stalled:
		if said.status == http.StatusOK && said.body == "blue" {
			t.Errorf("the request left open across the ceiling answered the retiree's own 200, want it cut")
		}
	case <-time.After(5 * time.Second):
		t.Error("the request left open across the ceiling is still open, want it cut once the drain expired")
	}
	rest := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(events)
		rest <- err
	}()
	select {
	case <-rest:
	case <-time.After(5 * time.Second):
		t.Error("the stream left open across the ceiling is still open, want it cut once the drain expired")
	}
}

func TestAFlipCutShortAfterItsFirstDrainLineEndsItsAnswerIncomplete(t *testing.T) {
	t.Parallel()

	blue, green := aStallingBackend(t, "blue"), backend(t, "green")
	board, at := served(t, routing(t, map[string]string{"shop.example.com": blue.address}))
	go asking(at, "shop.example.com", "/stall")
	<-blue.arrived

	stopping, stop := context.WithCancel(t.Context())
	control := httptest.NewUnstartedServer(board.Control())
	control.Config.BaseContext = func(net.Listener) context.Context { return stopping }
	control.Start()
	t.Cleanup(control.Close)
	idle := "127.0.0.1:1"
	form := url.Values{
		switchboard.TableField:  {tableAt(t, routing(t, map[string]string{"shop.example.com": green}))},
		switchboard.RetireField: {idle, blue.address},
		switchboard.WindowField: {"30s"},
	}
	answer, err := http.PostForm(control.URL+switchboard.FlipPath, form)
	if err != nil {
		t.Fatal(err)
	}
	defer answer.Body.Close()
	lines := bufio.NewReader(answer.Body)
	if first, err := lines.ReadString('\n'); err != nil || first != switchboard.Drained+" "+idle+"\n" {
		t.Fatalf("the flip first answered %q, %v, want the idle retiree drained", first, err)
	}
	stop()
	if rest, err := io.ReadAll(lines); err == nil {
		t.Errorf("a flip stopped while %s still drained ended its answer cleanly after %q, want it cut short so no caller reads a partial drain report as a whole one", blue.address, rest)
	}
}

func TestARetireeKeepsNoConnectionFromTheSwitchboardOnceItHasDrained(t *testing.T) {
	t.Parallel()

	var open atomic.Int64
	closed := make(chan struct{}, 64)
	retiree := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "blue")
	}))
	retiree.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
			closed <- struct{}{}
		}
	}
	retiree.Start()
	t.Cleanup(retiree.Close)
	blue, green := strings.TrimPrefix(retiree.URL, "http://"), backend(t, "green")
	board, at := served(t, routing(t, map[string]string{"shop.example.com": blue}))
	for range 3 {
		if said := ask(t, http.DefaultClient, at, "shop.example.com", "/"); said.body != "blue" {
			t.Fatalf("shop.example.com answered %q before the flip, want blue", said.body)
		}
	}
	if open.Load() == 0 {
		t.Fatal("the switchboard has no connection open to blue after asking it, want one kept alive for the next request")
	}

	var told drains
	if err := board.Flip(t.Context(), tableAt(t, routing(t, map[string]string{"shop.example.com": green})), []string{blue}, 10*time.Second, told.tell); err != nil {
		t.Fatal(err)
	}
	for open.Load() > 0 {
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatalf("the switchboard still has %d connections open to %s after it drained, want none left to keep the retired container open", open.Load(), blue)
		}
	}
}
