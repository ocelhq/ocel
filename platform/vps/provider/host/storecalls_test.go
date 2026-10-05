package host

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestAStoreThatIsBusyOrSilentExitsSoTheCallIsTriedAgainAndARefusalDoesNot(t *testing.T) {
	t.Parallel()

	spec := aStore()
	calls, err := spec.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	req, err := spec.signed(calls[0], time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	before, driven, _ := strings.Cut(curlCommand(spec.Store, req, calls[0]), "answered=$(")
	_, tail, _ := strings.Cut(driven, "\n")
	for answered, want := range map[string]int{"503": storeBusyExit, "429": storeBusyExit, "000": storeBusyExit, "403": 1} {
		run := exec.Command("sh", "-c", before+"answered="+answered+"\n"+tail)
		out, _ := run.CombinedOutput()
		if got := run.ProcessState.ExitCode(); got != want {
			t.Errorf("the store answering %s exited %d, want %d: %s", answered, got, want, strings.TrimSpace(string(out)))
		}
	}
}

func TestEveryStoreCallBoundsTheDockerExecItRunsCurlIn(t *testing.T) {
	t.Parallel()

	spec := aStore()
	calls, err := spec.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	req, err := spec.signed(calls[0], time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	for what, script := range map[string]string{
		"a bucket call":   curlCommand(spec.Store, req, calls[0]),
		"a store listing": storeScript(spec.Store, []*http.Request{req}, calls[:1]),
	} {
		at := strings.Index(script, quoted("docker")+" "+quoted("exec"))
		if at < 0 || !strings.Contains(script[:at], quoted("timeout")) {
			t.Errorf("%s runs docker exec with no time bound of its own, so a docker that stops answering stalls the deploy:\n%s", what, script)
		}
	}
}

func storeAnswering(codes ...int) *bench {
	box := machine(nil)
	var mu sync.Mutex
	box.answer = func(command string) (session.Result, bool) {
		if !strings.Contains(command, quoted("docker")+" "+quoted("exec")) {
			return session.Result{}, false
		}
		mu.Lock()
		defer mu.Unlock()
		code := codes[0]
		if len(codes) > 1 {
			codes = codes[1:]
		}
		return session.Result{Code: code, Stderr: "the store answered 503"}, true
	}
	return box
}

func storeCalls(box *bench) int {
	called := 0
	for _, command := range box.commands() {
		if strings.Contains(command, quoted("docker")+" "+quoted("exec")) {
			called++
		}
	}
	return called
}

func TestAStoreThatIsBusyIsAskedAgainAfterABackoff(t *testing.T) {
	t.Parallel()

	box := storeAnswering(storeBusyExit, storeBusyExit, 0)
	if err := box.host().ApplyOrigins(context.Background(), aStore()); err != nil {
		t.Fatalf("ApplyOrigins() = %v, want a store busy twice and then answering to pass", err)
	}
	if called := storeCalls(box); called != 3 {
		t.Errorf("the store was asked %d times, want 3", called)
	}
	waits := box.waits()
	if len(waits) != 2 {
		t.Fatalf("the call waited %v between tries, want two waits", waits)
	}
	for _, wait := range waits {
		if wait <= 0 || wait > time.Duration(storeCallBackoff.ceiling+storeCallBackoff.spread)*time.Second {
			t.Errorf("a wait of %s is outside the backoff's ceiling of %ds", wait, storeCallBackoff.ceiling)
		}
	}
}

func TestAStoreThatStaysBusyIsRefusedAfterItsTries(t *testing.T) {
	t.Parallel()

	box := storeAnswering(storeBusyExit)
	err := box.host().ApplyOrigins(context.Background(), aStore())
	if err == nil {
		t.Fatal("ApplyOrigins() passed against a store that never stopped being busy")
	}
	if called := storeCalls(box); called != storeCallTries {
		t.Errorf("the store was asked %d times, want %d", called, storeCallTries)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("ApplyOrigins() = %v, want it to say what the store last answered", err)
	}
}

func TestAStoreThatRefusesACallIsNotAskedAgain(t *testing.T) {
	t.Parallel()

	box := storeAnswering(1)
	if err := box.host().ApplyOrigins(context.Background(), aStore()); err == nil {
		t.Fatal("ApplyOrigins() passed against a store that refused the call")
	}
	if called := storeCalls(box); called != 1 {
		t.Errorf("a refused call was made %d times, and a refusal does not change on a retry", called)
	}
}
