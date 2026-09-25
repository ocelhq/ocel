package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func ran(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var out, errs strings.Builder
	return run(t.Context(), argv, &out, &errs), out.String(), errs.String()
}

func controlAt(t *testing.T) string {
	t.Helper()
	control := filepath.Join(t.TempDir(), "control.sock")
	if len(control) > 100 {
		t.Skipf("a unix socket path this host accepts does not fit under %s", control)
	}
	t.Setenv(controlEnv, control)
	return control
}

func frontAt(t *testing.T) string {
	t.Helper()
	front := filepath.Join(t.TempDir(), "front.sock")
	if len(front) > 100 {
		t.Skipf("a unix socket path this host accepts does not fit under %s", front)
	}
	return front
}

func freeAddress(t *testing.T) string {
	t.Helper()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	return held.Addr().String()
}

func tableFile(t *testing.T, upstreams map[string]string) string {
	t.Helper()
	type claim struct {
		Owner    string `json:"owner"`
		Hostname string `json:"hostname"`
		Pointer  string `json:"pointer"`
	}
	type route struct {
		Owner    string `json:"owner"`
		Pointer  string `json:"pointer"`
		App      string `json:"app"`
		Upstream string `json:"upstream"`
	}
	table := struct {
		Grace  string  `json:"grace"`
		Claims []claim `json:"claims"`
		Routes []route `json:"routes"`
	}{Grace: "30s"}
	for at, hostname := range slices.Sorted(maps.Keys(upstreams)) {
		owner := fmt.Sprintf("ocel--site-%d--production", at)
		table.Claims = append(table.Claims, claim{Owner: owner, Hostname: hostname, Pointer: "@production"})
		table.Routes = append(table.Routes, route{Owner: owner, Pointer: "@production", App: "web", Upstream: upstreams[hostname]})
	}
	written, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	return documentAt(t, written)
}

func documentAt(t *testing.T, document []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routing.json")
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func backend(t *testing.T, name string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, name)
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

type serving struct {
	data    string
	front   string
	control string
	stop    context.CancelFunc
	done    chan int
	errs    *strings.Builder
}

func served(t *testing.T, table string, flags ...string) serving {
	t.Helper()
	stood := serving{data: freeAddress(t), front: frontAt(t), control: controlAt(t), done: make(chan int, 1), errs: &strings.Builder{}}
	ctx, stop := context.WithCancel(t.Context())
	stood.stop = stop
	argv := append([]string{"serve", "--listen", stood.data, "--front", stood.front, "--table", table}, flags...)
	go func() { stood.done <- run(ctx, argv, io.Discard, stood.errs) }()
	t.Cleanup(func() {
		stop()
		<-stood.done
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("unix", stood.control); err == nil {
			_ = conn.Close()
			return stood
		}
		select {
		case code := <-stood.done:
			t.Fatalf("serve exited %d before its control socket answered: %s", code, stood.errs)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("serve never answered on %s", stood.control)
	return stood
}

func (s serving) ask(t *testing.T, host string) (int, string, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+s.data+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	said, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", host, err)
	}
	defer said.Body.Close()
	body, _ := io.ReadAll(said.Body)
	return said.StatusCode, string(body), said.Header.Get(edge.HeaderEdge)
}

func TestServeAnswersEachHostnameItsTableClaimsAndNamesTheBox(t *testing.T) {
	web := backend(t, "web")
	stood := served(t, tableFile(t, map[string]string{"shop.example.com": web}))

	if status, body, named := stood.ask(t, "shop.example.com"); status != http.StatusOK || body != "web" || named != switchboard.EdgeName {
		t.Errorf("shop.example.com answered %d %q naming %q, want the app's 200 naming %s", status, body, named, switchboard.EdgeName)
	}
	if status, _, named := stood.ask(t, "unclaimed.example.com"); status != http.StatusNotFound || named != switchboard.EdgeName {
		t.Errorf("an unclaimed hostname answered %d naming %q, want a 404 naming %s", status, named, switchboard.EdgeName)
	}
}

func TestServeTrustsWhatArrivesOverTheFrontSocketAndNothingElse(t *testing.T) {
	seen := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("X-Forwarded-Proto")
	}))
	t.Cleanup(upstream.Close)
	stood := served(t, tableFile(t, map[string]string{"shop.example.com": strings.TrimPrefix(upstream.URL, "http://")}))
	dialer := &net.Dialer{}
	fronting := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", stood.front)
		},
	}}

	for what, client := range map[string]*http.Client{"http": http.DefaultClient, "https": fronting} {
		request, err := http.NewRequest(http.MethodGet, "http://"+stood.data+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = "shop.example.com"
		request.Header.Set("X-Forwarded-Proto", "https")
		said, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = said.Body.Close()
		if proto := <-seen; proto != what {
			t.Errorf("a request that said it forwarded https reached the app as %q, want %q: only what arrives over the front socket is the front proxy's, whatever address it was recreated at", proto, what)
		}
	}
}

func TestTheControlAndFrontSocketsAreTheServingUsersAlone(t *testing.T) {
	stood := served(t, tableFile(t, nil))

	for socket, why := range map[string]string{
		stood.control: "whoever can connect to it can reroute every hostname on the box",
		stood.front:   "whoever can connect to it is trusted to say who the client was",
	} {
		held, err := os.Stat(socket)
		if err != nil {
			t.Fatal(err)
		}
		if mode := held.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s is mode %v, want 0600: %s", socket, mode, why)
		}
	}
}

func TestServeRefusesToTakeTheControlSocketFromASwitchboardStillAnsweringOnIt(t *testing.T) {
	stood := served(t, tableFile(t, nil))

	var errs strings.Builder
	code := run(t.Context(), []string{"serve", "--listen", freeAddress(t), "--front", frontAt(t), "--table", tableFile(t, nil)}, io.Discard, &errs)
	if code != exitRefused {
		t.Errorf("a second serve on %s = %d, want %d: it would unlink the socket the first answers on and leave it unreachable", stood.control, code, exitRefused)
	}
	if code, _, errs := ran(t, "upstreams"); code != 0 {
		t.Errorf("the first switchboard answered upstreams = %d, %q after the second was refused, want it still reachable", code, errs)
	}
}

func TestOfServesStartedTogetherOnOneControlSocketExactlyOneTakesIt(t *testing.T) {
	controlAt(t)
	table := tableFile(t, nil)
	ctx, stop := context.WithCancel(t.Context())
	const racing = 16
	exited := make(chan int, racing)
	var errs [racing]strings.Builder
	for at := range racing {
		argv := []string{"serve", "--listen", freeAddress(t), "--front", frontAt(t), "--table", table}
		go func() { exited <- run(ctx, argv, io.Discard, &errs[at]) }()
	}
	refused := 0
	t.Cleanup(func() {
		stop()
		for range racing - refused {
			<-exited
		}
	})

	for refused < racing-1 {
		select {
		case code := <-exited:
			refused++
			if code != exitRefused {
				t.Errorf("a serve that lost the control socket exited %d, want %d", code, exitRefused)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%d of %d serves started together are still serving on one control socket, want exactly one", racing-refused, racing)
		}
	}
	if code, _, errs := ran(t, "upstreams"); code != 0 {
		t.Errorf("the serve that kept the control socket answered upstreams = %d, %q, want it reachable", code, errs)
	}
}

func TestServeReplacesASocketNothingAnswersOn(t *testing.T) {
	control := controlAt(t)
	stale, err := net.Listen("unix", control)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()

	web := backend(t, "web")
	stood := served(t, tableFile(t, map[string]string{"shop.example.com": web}))
	if status, body, _ := stood.ask(t, "shop.example.com"); status != http.StatusOK || body != "web" {
		t.Errorf("a switchboard started over a dead socket answered %d %q, want it serving", status, body)
	}
}

func TestServeRefusesATableOrAFrontSocketItCannotTake(t *testing.T) {
	controlAt(t)
	for what, argv := range map[string][]string{
		"a table that is not there":     {"serve", "--listen", freeAddress(t), "--front", frontAt(t), "--table", filepath.Join(t.TempDir(), "routing.json")},
		"a table it cannot render":      {"serve", "--listen", freeAddress(t), "--front", frontAt(t), "--table", documentAt(t, []byte(`{"grace":"soon"}`))},
		"a front socket it cannot open": {"serve", "--listen", freeAddress(t), "--front", filepath.Join(t.TempDir(), "absent", "front.sock"), "--table", tableFile(t, nil)},
		"no front socket":               {"serve", "--listen", freeAddress(t), "--table", tableFile(t, nil)},
		"no listen address":             {"serve", "--front", frontAt(t), "--table", tableFile(t, nil)},
		"no table":                      {"serve", "--listen", freeAddress(t), "--front", frontAt(t)},
	} {
		if code, _, errs := ran(t, argv...); code != exitRefused {
			t.Errorf("serve with %s = %d, %q, want %d", what, code, errs, exitRefused)
		}
	}
}

func TestLoadSwapsTheTableTheSwitchboardServesAndRefusesOneItCannotRead(t *testing.T) {
	blue, green := backend(t, "blue"), backend(t, "green")
	stood := served(t, tableFile(t, map[string]string{"shop.example.com": blue}))

	if code, out, errs := ran(t, "load", tableFile(t, map[string]string{"shop.example.com": green})); code != 0 {
		t.Fatalf("load = %d, %q %q", code, out, errs)
	}
	if _, body, _ := stood.ask(t, "shop.example.com"); body != "green" {
		t.Errorf("after the load shop.example.com answered %q, want green", body)
	}
	code, _, errs := ran(t, "load", documentAt(t, []byte(`{"grace":"30s","routes":[{"owner":"o","pointer":"p","app":"web","upstream":"nowhere"}]}`)))
	if code != exitRefused || !strings.Contains(errs, "nowhere") {
		t.Errorf("loading a table routing to no address = %d, %q, want %d naming what it could not dial", code, errs, exitRefused)
	}
	if _, body, _ := stood.ask(t, "shop.example.com"); body != "green" {
		t.Errorf("after a refused load shop.example.com answered %q, want green still serving", body)
	}
}

func TestLoadTakesATablePathRelativeToWhereItRan(t *testing.T) {
	green := backend(t, "green")
	stood := served(t, tableFile(t, nil))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "routing.json"), []byte(`{"grace":"1s","claims":[{"owner":"o","hostname":"shop.example.com","pointer":"p"}],"routes":[{"owner":"o","pointer":"p","app":"web","upstream":"`+green+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if code, out, errs := ran(t, "load", "routing.json"); code != 0 {
		t.Fatalf("load routing.json = %d, %q %q", code, out, errs)
	}
	if _, body, _ := stood.ask(t, "shop.example.com"); body != "green" {
		t.Errorf("after loading a relative path shop.example.com answered %q, want green", body)
	}
}

func TestFlipSwitchesThenPrintsEachRetireeTheMomentItDrains(t *testing.T) {
	blue, green := backend(t, "blue"), backend(t, "green")
	stood := served(t, tableFile(t, map[string]string{"shop.example.com": blue}))

	code, out, errs := ran(t, "flip", "--drain-timeout", "5", "--retire", "tcp/"+blue, tableFile(t, map[string]string{"shop.example.com": green}))
	if code != 0 {
		t.Fatalf("flip = %d, %q %q", code, out, errs)
	}
	if out != switchboard.Drained+" "+blue+"\n" {
		t.Errorf("flip printed %q, want %q: the release reads each retiree's outcome off these lines", out, switchboard.Drained+" "+blue+"\n")
	}
	if _, body, _ := stood.ask(t, "shop.example.com"); body != "green" {
		t.Errorf("after the flip shop.example.com answered %q, want green", body)
	}
	if code, out, errs := ran(t, "flip", tableFile(t, map[string]string{"shop.example.com": blue})); code != 0 || out != "" {
		t.Errorf("a flip retiring nothing = %d, %q %q, want it silent", code, out, errs)
	}
}

func TestAFlipWhoseCeilingPassesPrintsWhatTheRetireeStillHeldAndSucceeds(t *testing.T) {
	release := make(chan struct{})
	held := make(chan struct{}, 1)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		held <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "slow")
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	blue, green := strings.TrimPrefix(slow.URL, "http://"), backend(t, "green")
	stood := served(t, tableFile(t, map[string]string{"shop.example.com": blue}))
	go func() {
		request, _ := http.NewRequest(http.MethodGet, "http://"+stood.data+"/", nil)
		request.Host = "shop.example.com"
		if said, err := http.DefaultClient.Do(request); err == nil {
			_ = said.Body.Close()
		}
	}()
	<-held

	code, out, errs := ran(t, "flip", "--drain-timeout", "1", "--retire", blue, tableFile(t, map[string]string{"shop.example.com": green}))
	if code != 0 {
		t.Fatalf("a flip whose drain expired = %d, %q, want it borne as a warning: the new release is serving", code, errs)
	}
	if want := switchboard.DrainExpired + " " + blue + " 1\n"; out != want {
		t.Errorf("flip printed %q, want %q", out, want)
	}
}

func TestAFlipWhoseAnswerIsCutShortAfterADrainLineExitsRefused(t *testing.T) {
	control := controlAt(t)
	listener, err := net.Listen("unix", control)
	if err != nil {
		t.Fatal(err)
	}
	partial := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, switchboard.Drained+" 127.0.0.1:1\n")
		_ = http.NewResponseController(w).Flush()
		panic(http.ErrAbortHandler)
	}))
	partial.Listener = listener
	partial.Start()
	t.Cleanup(partial.Close)

	code, out, errs := ran(t, "flip", "--drain-timeout", "5", "--retire", "127.0.0.1:1", "--retire", "127.0.0.1:2", tableFile(t, nil))
	if code != exitRefused {
		t.Errorf("a flip whose answer stopped after one of two retirees = %d, %q %q, want %d: a partial drain report is not a finished flip", code, out, errs, exitRefused)
	}
	if !strings.Contains(errs, "short") {
		t.Errorf("the refusal read %q, want it to say the switchboard cut its answer short", errs)
	}
}

func TestAFlipTheSwitchboardRefusesSwitchesNothing(t *testing.T) {
	blue := backend(t, "blue")
	stood := served(t, tableFile(t, map[string]string{"shop.example.com": blue}))

	for what, argv := range map[string][]string{
		"a table it cannot read":   {"flip", "--drain-timeout", "5", "--retire", blue, documentAt(t, []byte(`{}`))},
		"a retiree with no port":   {"flip", "--drain-timeout", "5", "--retire", "blue", tableFile(t, nil)},
		"a retiree and no ceiling": {"flip", "--retire", blue, tableFile(t, nil)},
		"no table":                 {"flip"},
		"two tables":               {"flip", tableFile(t, nil), tableFile(t, nil)},
	} {
		if code, out, _ := ran(t, argv...); code != exitRefused || out != "" {
			t.Errorf("a flip with %s = %d, %q, want %d and nothing drained", what, code, out, exitRefused)
		}
	}
	if _, body, _ := stood.ask(t, "shop.example.com"); body != "blue" {
		t.Errorf("after the refused flips shop.example.com answered %q, want blue still serving", body)
	}
}

func TestIdleNamesOnlyTheTargetsNoRouteDialsAndNoFlipIsDraining(t *testing.T) {
	blue, green := backend(t, "blue"), backend(t, "green")
	served(t, tableFile(t, map[string]string{"shop.example.com": blue}))

	code, out, errs := ran(t, "idle", "tcp/"+blue, green)
	if code != 0 {
		t.Fatalf("idle = %d, %q", code, errs)
	}
	if out != green+"\n" {
		t.Errorf("idle printed %q, want only %s: the routed upstream is serving", out, green)
	}
	if code, out, _ := ran(t, "idle", "blue"); code != exitRefused || out != "" {
		t.Errorf("idle blue = %d, %q, want it refused: an address with no port keys nothing and would read as idle whatever holds it", code, out)
	}
}

func TestUpstreamsListsWhatEachUpstreamHoldsInFlight(t *testing.T) {
	blue := backend(t, "blue")
	served(t, tableFile(t, map[string]string{"shop.example.com": blue}))

	code, out, errs := ran(t, "upstreams")
	if code != 0 {
		t.Fatalf("upstreams = %d, %q", code, errs)
	}
	var listed []switchboard.Upstream
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("upstreams printed %q: %v", out, err)
	}
	if !slices.Equal(listed, []switchboard.Upstream{{Address: blue}}) {
		t.Errorf("upstreams listed %+v, want the one routed upstream holding nothing", listed)
	}

	served(t, tableFile(t, nil))
	if code, out, _ := ran(t, "upstreams"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("upstreams on a box routing nothing = %d, %q, want []", code, out)
	}
}

func TestEveryVerbThatSpeaksToTheSwitchboardNamesTheSocketItCouldNotReach(t *testing.T) {
	control := controlAt(t)
	for _, argv := range [][]string{
		{"upstreams"}, {"load", tableFile(t, nil)}, {"flip", tableFile(t, nil)}, {"idle", "a:1"},
	} {
		code, _, errs := ran(t, argv...)
		if code != exitRefused || !strings.Contains(errs, control) {
			t.Errorf("%s with nothing serving = %d, %q, want %d naming %s", argv[0], code, errs, exitRefused, control)
		}
	}
}

func TestServeStopsOnItsContextAndLetsARequestInFlightFinishWithinTheGrace(t *testing.T) {
	release := make(chan struct{})
	held := make(chan struct{}, 1)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		held <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "slow")
	}))
	t.Cleanup(slow.Close)
	stood := served(t, tableFile(t, map[string]string{"shop.example.com": strings.TrimPrefix(slow.URL, "http://")}))

	answered := make(chan string, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, "http://"+stood.data+"/", nil)
		request.Host = "shop.example.com"
		said, err := http.DefaultClient.Do(request)
		if err != nil {
			answered <- err.Error()
			return
		}
		defer said.Body.Close()
		body, _ := io.ReadAll(said.Body)
		answered <- string(body)
	}()
	<-held
	stood.stop()
	deadline := time.Now().Add(5 * time.Second)
	for conn, err := net.Dial("tcp", stood.data); err == nil; conn, err = net.Dial("tcp", stood.data) {
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("serve still takes connections on %s five seconds after it was told to stop", stood.data)
		}
	}
	close(release)
	if body := <-answered; body != "slow" {
		t.Errorf("the request in flight when serve was told to stop answered %q, want its upstream's answer", body)
	}
	select {
	case code := <-stood.done:
		stood.done <- code
		if code != 0 {
			t.Errorf("serve exited %d on its context, want 0: %s", code, stood.errs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve never returned after its context ended")
	}
}

func TestTheGateCallsATargetUpOnlyOnATwoHundredAndNamesTheOneThatWasNot(t *testing.T) {
	var mu sync.Mutex
	refusals := 2
	warming := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if refusals > 0 {
			refusals--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(warming.Close)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	up := strings.TrimPrefix(warming.URL, "http://")

	if code, out, errs := ran(t, "gate", "--deploy-timeout", "5", up+"/up"); code != 0 || out != "204\n" {
		t.Errorf("gating a target that warms up into a 204 = %d, %q %q, want it passed with the status it read", code, out, errs)
	}
	code, out, errs := ran(t, "gate", "--deploy-timeout", "1", up+"/up", strings.TrimPrefix(down.URL, "http://")+"/up")
	if code != exitNotServingYet {
		t.Errorf("gating a target answering 503 throughout = %d, %q, want %d", code, errs, exitNotServingYet)
	}
	if want := switchboard.Ungated + " " + strings.TrimPrefix(down.URL, "http://") + "/up\n"; !strings.HasSuffix(out, want) {
		t.Errorf("the gate printed %q, want it to end naming the target that failed: %q", out, want)
	}
	if code, out, errs := ran(t, "gate", "--deploy-timeout", "1", "127.0.0.1:1/up"); code != exitSilent || !strings.Contains(out, switchboard.Ungated) {
		t.Errorf("gating a target that never answers = %d, %q %q, want %d naming it ungated", code, out, errs, exitSilent)
	}
	for what, argv := range map[string][]string{
		"no target":             {"gate", "--deploy-timeout", "1"},
		"a target with no path": {"gate", "--deploy-timeout", "1", "127.0.0.1:1"},
		"no window":             {"gate", "127.0.0.1:1/up"},
	} {
		if code, _, _ := ran(t, argv...); code != exitRefused {
			t.Errorf("gating %s = %d, want the usage refusal", what, code)
		}
	}
}

func TestAVerbTheSwitchboardDoesNotCarryIsRefusedWithTheOnesItDoes(t *testing.T) {
	for _, argv := range [][]string{{}, {"forget", "shop.example.com"}, {"config", "/"}, {"listeners"}} {
		code, _, errs := ran(t, argv...)
		if code != exitRefused {
			t.Errorf("%v = %d, want the usage refusal", argv, code)
		}
		for _, verb := range []string{"serve", "load", "gate", "flip", "idle", "upstreams", "leaf", "probe", "inodes", "holds"} {
			if !strings.Contains(errs, verb) {
				t.Errorf("the usage %q never names %s", errs, verb)
			}
		}
	}
}
