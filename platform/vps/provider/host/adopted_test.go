package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type yours struct {
	file   string
	handed *proxy.Spec
}

func (y yours) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (y yours) Render(spec proxy.Spec) ([]byte, error) {
	if y.handed != nil {
		*y.handed = spec
	}
	return []byte("routes " + strings.Join(spec.Hostnames, " ") + "\n"), nil
}

func (y yours) File() string { return y.file }

func (yours) Unrendered([]byte, proxy.Permission) string { return "" }

func (yours) Reload(context.Context) error { return nil }

func (yours) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (yours) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func TestAProxyIsHandedEveryHostnameTheBoxServesAndThePreviewBase(t *testing.T) {
	t.Parallel()

	state := previewing()
	state.Claims = []HostClaim{
		{Hostname: claimed, Owner: surface, Pointer: pointed},
		{Hostname: "pr-7." + previewBase, Owner: surface, Pointer: "@pr-7"},
	}
	state.Connector = "console.example.com"
	var handed proxy.Spec
	if _, err := RenderProxyConfig(yours{handed: &handed}, state); err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	want := []string{"console.example.com", edge.ProbeHostname(edge.PreviewWildcard(previewBase)), "pr-7." + previewBase, claimed}
	slices.Sort(want)
	if !slices.Equal(handed.Hostnames, want) {
		t.Errorf("the proxy is handed hostnames %q, want %q: a proxy that names what it routes routes nothing it is not told of, and the preview claim, the connector and the edge probe are each served here", handed.Hostnames, want)
	}
	if handed.PreviewBase != previewBase {
		t.Errorf("the proxy is handed preview base %q, want %q for a wildcard certificate over the previews", handed.PreviewBase, previewBase)
	}
}

const coolifyDynamic = "/data/coolify/proxy/dynamic"

func TestTheSwitchboardIsHandedTheDirectoryOfAFileOutsideOcelsOwnToPlaceInAndNoOther(t *testing.T) {
	t.Parallel()

	board := placing(switchboardOf(t, routedByHand()), coolifyDynamic+"/ocel.yml")
	running := board.run()
	if at := slices.Index(running, coolifyDynamic+":"+coolifyDynamic); at < 1 || running[at-1] != "--volume" {
		t.Errorf("the switchboard runs as %q, want %s bound into it read-write: ocel-deploy cannot write there, and the switchboard places what it renders", running, coolifyDynamic)
	}
	if at := slices.Index(running, switchboard.PlaceEnv+"="+coolifyDynamic); at < 1 || running[at-1] != "--env" {
		t.Errorf("the switchboard runs as %q, want %s set to %s: it refuses to place anywhere else", running, switchboard.PlaceEnv, coolifyDynamic)
	}
	if !strings.Contains(string(board.facts()), "bind="+coolifyDynamic+":"+coolifyDynamic+"\n") {
		t.Errorf("the switchboard's facts read\n%s\nand never name the directory it places in, so a bootstrap that moved it plans nothing", board.facts())
	}

	for what, front := range map[string]Front{"ocel's own proxy": {}, "a proxy routed by hand": routedByHand()} {
		for _, bound := range switchboardOf(t, front).binds {
			if !strings.HasSuffix(bound, ":ro") && !slices.Contains([]string{switchboard.ControlDir, switchboard.FrontDir}, strings.SplitN(bound, ":", 2)[0]) {
				t.Errorf("the switchboard beside %s is bound %s read-write, want no directory to place in: nothing it renders goes anywhere ocel-deploy cannot write", what, bound)
			}
		}
		if env := switchboardOf(t, front).env; slices.ContainsFunc(env, func(set string) bool { return strings.HasPrefix(set, switchboard.PlaceEnv+"=") }) {
			t.Errorf("the switchboard beside %s runs with %q, want no directory to place in", what, env)
		}
	}
}

const coolifyFile = coolifyDynamic + "/ocel.yml"

func places(command string) bool { return strings.Contains(command, quoted("place")+" ") }

func sums(command string) bool { return strings.Contains(command, quoted("placed")+" ") }

type adoptedBench struct {
	*claimBench
	placed  string
	gone    bool
	refused []string
	summing func(*adoptedBench)
}

func (a *adoptedBench) refusal() string {
	if len(a.refused) == 0 {
		return ""
	}
	refused := a.refused[0]
	a.refused = a.refused[1:]
	return refused
}

func adoptedBox(t *testing.T, state RoutingTable) *adoptedBench {
	t.Helper()

	stood := &adoptedBench{claimBench: &claimBench{bench: machine(nil), held: string(mustWrite(t, state))}}
	stood.placed = string(mustPlace(t, state))
	absent := ""
	proxied := servesPair(stood.bench, &stood.held, &absent)
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case sums(command):
			stood.mu.Lock()
			defer stood.mu.Unlock()
			if stood.summing != nil {
				stood.summing(stood)
				stood.summing = nil
			}
			if stood.gone {
				return session.Result{}, true
			}
			return session.Result{Stdout: digested(stood.placed) + "\n"}, true
		case writesProxy(command):
			stood.mu.Lock()
			defer stood.mu.Unlock()
			if expected := expectedDigest(command); expected != digested(stood.held) {
				return session.Result{Code: routingMoved, Stderr: digested(stood.held)}, true
			}
			fed := stood.fed[len(stood.fed)-1]
			stood.held = tableOf(fed)
			if !places(command) {
				return session.Result{Stdout: digested(stood.held)}, true
			}
			if refused := stood.refusal(); refused != "" {
				return session.Result{Code: routingPlaceFailed, Stdout: digested(stood.held), Stderr: refused}, true
			}
			stood.placed, stood.gone = configOf(fed), false
			return session.Result{Stdout: digested(stood.held)}, true
		case places(command):
			stood.mu.Lock()
			defer stood.mu.Unlock()
			if expected := expectedDigest(command); expected != digested(stood.held) {
				return session.Result{Code: routingMoved, Stderr: digested(stood.held)}, true
			}
			if refused := stood.refusal(); refused != "" {
				return session.Result{Code: routingPlaceFailed, Stderr: refused}, true
			}
			stood.placed, stood.gone = stood.fed[len(stood.fed)-1], false
			return session.Result{}, true
		case gates(command), flips(command):
			return session.Result{}, true
		case idles(command):
			return everyIdle(command), true
		default:
			return proxied(command)
		}
	}
	return stood
}

func (a *adoptedBench) host() *Host {
	h := a.fronted(routedByHand())
	h.front = yours{file: coolifyFile}
	return h
}

func mustPlace(t *testing.T, state RoutingTable) []byte {
	t.Helper()
	rendered, err := RenderProxyConfig(yours{file: coolifyFile}, state)
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}

func placedUnderLock(t *testing.T, command string) {
	t.Helper()
	locked := strings.Index(command, "exec 9<"+quoted(routingLock)+"\nflock -x 9")
	compared := strings.Index(command, `if [ "$held" != `)
	placing := strings.Index(command, words(switchboardFed("place", coolifyFile)))
	if locked < 0 || compared < locked || placing < compared {
		t.Errorf("the placement runs\n%s\nwant it through the switchboard, fed on stdin, after the table is compared under the routing lock: a deploy that wrote the table after this one can otherwise see its rendering overwritten by this one's", command)
	}
}

func TestAClaimOnABoxWhoseProxyKeepsItsFileElsewherePlacesItsRenderingUnderTheLockItWritesTheTableUnder(t *testing.T) {
	t.Parallel()

	stood := adoptedBox(t, routed())
	if err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	commands := stood.commands()
	wrote := slices.IndexFunc(commands, writesProxy)
	if wrote < 0 {
		t.Fatalf("the claim ran %q and never wrote the table", commands)
	}
	written := commands[wrote]
	if strings.Contains(written, ProxyConfig) {
		t.Errorf("the table write asks for %s, which a proxy that keeps its file elsewhere never reads: %s", ProxyConfig, written)
	}
	placedUnderLock(t, written)
	if moved, placing := strings.Index(written, `mv "$staged" `), strings.Index(written, words(switchboardFed("place", coolifyFile))); moved < 0 || placing < moved {
		t.Errorf("the write runs\n%s\nwant the table moved into place before the rendering is placed: the table is what every later write and rollback reads", written)
	}
	if placed := stood.count(places); placed != 1 {
		t.Errorf("the claim placed %d times, want once, in the write that holds the lock: %q", placed, commands)
	}
	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	if want := string(mustPlace(t, state)); stood.placed != want {
		t.Errorf("the switchboard placed %q, want the rendering of the table the claim wrote, %q", stood.placed, want)
	}
}

func TestAPlacementTheSwitchboardRefusesPutsTheTableAndThePlacedFileBackAndSaysWhy(t *testing.T) {
	t.Parallel()

	stood := adoptedBox(t, routed())
	prior, placedBefore := stood.held, stood.placed
	refused := "open /data/coolify/proxy/dynamic/.ocel.123.tmp: read-only file system"
	stood.refused = []string{refused}
	err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil {
		t.Fatal("ClaimHosts() over a placement the switchboard refused = nil, want the claim refused")
	}
	if !strings.Contains(err.Error(), refused) {
		t.Errorf("the claim refused with %q, which never says why the placement failed", err)
	}
	if strings.Contains(err.Error(), "also failed") {
		t.Errorf("the claim refused with %q, want the table and the file restored", err)
	}
	if stood.held != prior {
		t.Errorf("%s holds\n%s\nafter a placement that failed, want the table before the claim\n%s\nthe switchboard would route a hostname the proxy in front of it was never told of", live.RoutingTable, stood.held, prior)
	}
	if stood.placed != placedBefore {
		t.Errorf("the placed file reads %q, want the rendering of the table put back, %q", stood.placed, placedBefore)
	}
	if writes := stood.count(writesProxy); writes != 2 {
		t.Errorf("the claim wrote the table %d times, want twice, once forward and once back: %q", writes, stood.commands())
	}
}

func TestADeployThatFindsThePlacedFileGoneOrRewrittenPlacesItsRenderingAgain(t *testing.T) {
	t.Parallel()

	serving := RoutingTable{Grace: DrainWindow, Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: retired}}}
	for what, drift := range map[string]func(*adoptedBench){
		"deleted from the proxy's ui":     func(a *adoptedBench) { a.gone = true },
		"re-serialised by the proxy's ui": func(a *adoptedBench) { a.placed = "routes: {}\n" },
		"emptied by hand":                 func(a *adoptedBench) { a.placed = "" },
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			stood := adoptedBox(t, serving)
			drift(stood)
			if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
				t.Fatalf("Release() = %v", err)
			}
			state, err := ReadRoutingTable([]byte(stood.held))
			if err != nil {
				t.Fatal(err)
			}
			if want := string(mustPlace(t, state)); stood.placed != want || stood.gone {
				t.Errorf("a deploy onto a placed file %s leaves it %q (gone %v), want the rendering of the table it wrote, %q", what, stood.placed, stood.gone, want)
			}
		})
	}
}

func TestAPlacedFileThatDriftedUnderATableThatDidNotIsPlacedUnderTheLockWithoutRewritingTheTable(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood := adoptedBox(t, state)
	stood.gone = true
	if err := stood.host().ClaimHosts(context.Background(), state.Claims); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if want := string(mustPlace(t, state)); stood.placed != want || stood.gone {
		t.Errorf("a claim already standing over a placed file that is gone leaves it %q (gone %v), want %q", stood.placed, stood.gone, want)
	}
	if stood.count(writesProxy) != 0 {
		t.Errorf("a placed file that drifted under a table that did not rewrote the table: %v", stood.commands())
	}
	commands := stood.commands()
	placing := commands[slices.IndexFunc(commands, places)]
	placedUnderLock(t, placing)
	if expected := expectedDigest(placing); expected != digested(stood.held) {
		t.Errorf("the placement is compared against table digest %s, want %s, the table it renders", expected, digested(stood.held))
	}
}

func TestAPlacedFileThatDriftedUnderATableAnotherDeployMovesIsPlacedFromTheTableThatDeployLeft(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood := adoptedBox(t, state)
	stood.gone = true
	other := state
	other.Claims = append(slices.Clone(state.Claims), HostClaim{Hostname: "blog.example.com", Owner: "ocel--blog--production", Pointer: pointed})
	stood.summing = func(a *adoptedBench) { a.held = string(mustWrite(t, other)) }
	if err := stood.host().ClaimHosts(context.Background(), state.Claims); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if want := string(mustPlace(t, other)); stood.placed != want || stood.gone {
		t.Errorf("a placement over a table another deploy moved leaves the file %q (gone %v), want the rendering of the table that deploy left, %q: a placement skipped because the table moved is a hostname the proxy is never told of", stood.placed, stood.gone, want)
	}
}

func TestAPlacedFileThatDriftedAndTheSwitchboardRefusesToPlaceFailsTheClaimAndSaysWhy(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood := adoptedBox(t, state)
	stood.gone = true
	refused := "open /data/coolify/proxy/dynamic/.ocel.123.tmp: no space left on device"
	stood.refused = []string{refused}
	err := stood.host().ClaimHosts(context.Background(), state.Claims)
	if err == nil || !strings.Contains(err.Error(), refused) {
		t.Errorf("ClaimHosts() over a placement the switchboard refused = %v, want it refused saying why", err)
	}
	if !stood.gone {
		t.Errorf("the placed file reads %q after a placement that failed, want it still gone", stood.placed)
	}
}

func TestAPlacedFileThatHoldsWhatTheTableRendersIsLeftAlone(t *testing.T) {
	t.Parallel()

	stood := adoptedBox(t, RoutingTable{Grace: DrainWindow, Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: retired}}})
	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if stood.count(writesProxy) == 0 {
		t.Fatal("the release never wrote the table, so this test proves nothing about a write that leaves the rendering as it was")
	}
	if placed := stood.count(places); placed != 0 {
		t.Errorf("a release onto a placed file that already holds what the table renders placed it %d times, want never: every placement is a rewrite the proxy watching the directory reloads", placed)
	}
}

type placingShell struct {
	table, fed, asked, refusing, bin, dir string
}

func shellPlacing(t *testing.T) *placingShell {
	t.Helper()
	for _, needed := range []string{"sh", "flock", "sha256sum", "cut", "mktemp", "base64", "mv"} {
		if _, err := exec.LookPath(needed); err != nil {
			t.Skipf("no %s on this machine, and the placement under test is the shell one a box runs", needed)
		}
	}
	dir := t.TempDir()
	box := &placingShell{
		table:    filepath.Join(dir, "table.json"),
		fed:      filepath.Join(dir, "fed"),
		asked:    filepath.Join(dir, "asked"),
		refusing: filepath.Join(dir, "refusing"),
		bin:      t.TempDir(),
		dir:      dir,
	}
	if err := os.WriteFile(box.table, mustWrite(t, routed()), 0o640); err != nil {
		t.Fatal(err)
	}
	executable(t, filepath.Join(box.bin, "docker"), "#!/bin/sh\n"+
		"printf '%s\\n' \"$*\" > "+quoted(box.asked)+"\n"+
		"cat > "+quoted(box.fed)+"\n"+
		"if [ -f "+quoted(box.refusing)+" ]; then echo 'no space left on device' >&2; exit 2; fi\n")
	return box
}

func (b *placingShell) refuse(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(b.refusing, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (b *placingShell) run(t *testing.T, script, stdin string) (int, string, string) {
	t.Helper()
	for _, left := range []string{b.fed, b.asked} {
		if err := os.Remove(left); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	command := exec.Command("/bin/sh", "-c", strings.NewReplacer(live.RoutingTable, b.table, routingLock, b.dir).Replace(script))
	command.Env = append(os.Environ(), "PATH="+b.bin+":"+os.Getenv("PATH"))
	command.Stdin = strings.NewReader(stdin)
	var out, errs strings.Builder
	command.Stdout, command.Stderr = &out, &errs
	err := command.Run()
	var exited *exec.ExitError
	if err != nil && !errors.As(err, &exited) {
		t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), out.String(), errs.String()
}

func (b *placingShell) asks(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(b.asked); errors.Is(err, os.ErrNotExist) {
		return ""
	}
	return strings.TrimSpace(held(t, b.asked))
}

func TestTheWriteFeedsTheSwitchboardTheRenderingOnlyOnceTheTableItRendersIsInPlace(t *testing.T) {
	t.Parallel()

	box := shellPlacing(t)
	before := held(t, box.table)
	after := string(mustWrite(t, previewing()))
	fed := pairFed(routingPair{table: []byte(after), config: []byte("routes shop.example.com\n")})
	asking := strings.Join(switchboardFed("place", coolifyFile)[1:], " ")

	if code, _, _ := box.run(t, stagedWrite(tableDigest(digested("a table another deploy has since replaced")), coolifyFile), fed); code != routingMoved {
		t.Errorf("a write over a table that moved = %d, want %d", code, routingMoved)
	}
	if asked := box.asks(t); asked != "" {
		t.Errorf("a write over a table that moved still asked docker %q, leaving a rendering of a table the box no longer holds", asked)
	}
	if got := held(t, box.table); got != before {
		t.Errorf("a write over a table that moved left it\n%s\nwant it untouched", got)
	}

	code, out, errs := box.run(t, stagedWrite(tableDigest(digested(before)), coolifyFile), fed)
	if code != 0 {
		t.Fatalf("a write over the table it read = %d: %s", code, errs)
	}
	if got := held(t, box.table); got != after {
		t.Errorf("the table reads\n%s\nwant the one written", got)
	}
	if strings.TrimSpace(out) != digested(after) {
		t.Errorf("the write said %q, want the digest of the table it wrote", out)
	}
	if got := held(t, box.fed); got != "routes shop.example.com\n" {
		t.Errorf("the switchboard was fed %q, want the rendering whole", got)
	}
	if asked := box.asks(t); asked != asking {
		t.Errorf("docker was asked %q, want %q", asked, asking)
	}
}

func TestAWriteWhoseRenderingTheSwitchboardRefusesSaysSoAndNamesTheTableItLeft(t *testing.T) {
	t.Parallel()

	box := shellPlacing(t)
	before := held(t, box.table)
	after := string(mustWrite(t, previewing()))
	box.refuse(t)

	code, out, errs := box.run(t, stagedWrite(tableDigest(digested(before)), coolifyFile),
		pairFed(routingPair{table: []byte(after), config: []byte("routes shop.example.com\n")}))
	if code != routingPlaceFailed {
		t.Errorf("a write whose placement the switchboard refused = %d, want %d so the deploy puts the table back", code, routingPlaceFailed)
	}
	if !strings.Contains(errs, "no space left on device") {
		t.Errorf("a write whose placement the switchboard refused said %q, which never says why", errs)
	}
	if strings.TrimSpace(out) != digested(held(t, box.table)) {
		t.Errorf("a write whose placement the switchboard refused said %q, want the digest of the table it left, which the revert compares against", out)
	}
}

func TestAPlacementAloneFeedsTheSwitchboardOnlyWhileTheTableIsTheOneItRenders(t *testing.T) {
	t.Parallel()

	box := shellPlacing(t)
	standing := held(t, box.table)

	if code, _, _ := box.run(t, replacement(tableDigest(digested("a table another deploy has since replaced")), coolifyFile), "routes shop.example.com\n"); code != routingMoved {
		t.Errorf("a placement over a table that moved = %d, want %d: the deploy reads the table again and places what it renders", code, routingMoved)
	}
	if asked := box.asks(t); asked != "" {
		t.Errorf("a placement over a table that moved still asked docker %q, leaving a rendering of a table the box no longer holds", asked)
	}

	if code, _, errs := box.run(t, replacement(tableDigest(digested(standing)), coolifyFile), "routes shop.example.com\n"); code != 0 {
		t.Fatalf("a placement over the table it renders = %d: %s", code, errs)
	}
	if got := held(t, box.fed); got != "routes shop.example.com\n" {
		t.Errorf("the switchboard was fed %q, want the rendering whole", got)
	}
	if asked, want := box.asks(t), strings.Join(switchboardFed("place", coolifyFile)[1:], " "); asked != want {
		t.Errorf("docker was asked %q, want %q", asked, want)
	}

	box.refuse(t)
	if code, _, errs := box.run(t, replacement(tableDigest(digested(standing)), coolifyFile), "routes shop.example.com\n"); code != routingPlaceFailed || !strings.Contains(errs, "no space left on device") {
		t.Errorf("a placement the switchboard refused = %d, %q, want %d saying why", code, errs, routingPlaceFailed)
	}
}

func TestEveryRoutingFailureNamesTheFilesTheProxyActuallyKeeps(t *testing.T) {
	t.Parallel()

	claim := []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	failing := func(stood *adoptedBench, result session.Result) {
		proxied := stood.answer
		stood.answer = func(command string) (session.Result, bool) {
			if writesProxy(command) {
				return result, true
			}
			return proxied(command)
		}
	}
	for what, run := range map[string]func(t *testing.T) ([]string, []string, error){
		"a write that fails beside a proxy routed by hand": func(t *testing.T) ([]string, []string, error) {
			stood := adoptedBox(t, routed())
			failing(stood, session.Result{Code: 1, Stderr: "mv: no space left on device"})
			return []string{live.RoutingTable}, []string{ProxyConfig}, stood.fronted(routedByHand()).ClaimHosts(context.Background(), claim)
		},
		"a write onto an unseeded box beside a proxy routed by hand": func(t *testing.T) ([]string, []string, error) {
			stood := adoptedBox(t, routed())
			failing(stood, session.Result{Code: routingUnseeded})
			return []string{live.RoutingTable}, []string{ProxyConfig}, stood.fronted(routedByHand()).ClaimHosts(context.Background(), claim)
		},
		"a release whose write fails beside a proxy routed by hand": func(t *testing.T) ([]string, []string, error) {
			stood := adoptedBox(t, RoutingTable{Grace: DrainWindow, Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: retired}}})
			failing(stood, session.Result{Code: 1, Stderr: "mv: no space left on device"})
			return []string{live.RoutingTable}, []string{ProxyConfig}, stood.fronted(routedByHand()).Release(context.Background(), aRelease(), nil)
		},
		"a release whose write fails beside a proxy that keeps its file elsewhere": func(t *testing.T) ([]string, []string, error) {
			stood := adoptedBox(t, RoutingTable{Grace: DrainWindow, Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: retired}}})
			failing(stood, session.Result{Code: 1, Stderr: "mv: no space left on device"})
			return []string{live.RoutingTable, coolifyFile}, []string{ProxyConfig}, stood.host().Release(context.Background(), aRelease(), nil)
		},
		"a placement whose revert fails too": func(t *testing.T) ([]string, []string, error) {
			stood := adoptedBox(t, routed())
			stood.refused = []string{"no space left on device", "no space left on device"}
			return []string{live.RoutingTable, coolifyFile}, []string{ProxyConfig}, stood.host().ClaimHosts(context.Background(), claim)
		},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			named, unnamed, err := run(t)
			if err == nil {
				t.Fatalf("%s = nil, want it refused", what)
			}
			for _, file := range named {
				if !strings.Contains(err.Error(), file) {
					t.Errorf("%s refused with\n%s\nwhich never names %s", what, err, file)
				}
			}
			for _, file := range unnamed {
				if strings.Contains(err.Error(), file) {
					t.Errorf("%s refused with\n%s\nwhich names %s, a file this proxy never reads and nothing wrote", what, err, file)
				}
			}
		})
	}
}
