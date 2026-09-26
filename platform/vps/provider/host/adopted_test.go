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
	refused string
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
			if stood.gone {
				return session.Result{}, true
			}
			return session.Result{Stdout: digested(stood.placed) + "\n"}, true
		case places(command):
			stood.mu.Lock()
			defer stood.mu.Unlock()
			if stood.refused != "" {
				return session.Result{Code: 2, Stderr: stood.refused}, true
			}
			if expected := expectedDigest(command); expected != digested(stood.held) {
				return session.Result{Code: routingMoved, Stderr: digested(stood.held)}, true
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

func TestAClaimOnABoxWhoseProxyKeepsItsFileElsewherePlacesItsRenderingThroughTheSwitchboardAfterTheTable(t *testing.T) {
	t.Parallel()

	stood := adoptedBox(t, routed())
	if err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	commands := stood.commands()
	wrote := slices.IndexFunc(commands, writesProxy)
	placedAt := slices.IndexFunc(commands, places)
	if wrote < 0 || placedAt < wrote {
		t.Fatalf("the claim ran %q, want the table written and then the rendering placed: the table is what every later write and rollback reads", commands)
	}
	if strings.Contains(commands[wrote], ProxyConfig) {
		t.Errorf("the table write asks for %s, which a proxy that keeps its file elsewhere never reads: %s", ProxyConfig, commands[wrote])
	}
	if want := words(switchboardCommand("place", coolifyFile)); !strings.Contains(commands[placedAt], strings.Replace(want, quoted("exec"), quoted("exec")+" "+quoted("-i"), 1)) {
		t.Errorf("the placement runs\n%s\nwant it through the switchboard, fed on stdin: %s", commands[placedAt], want)
	}
	if !strings.Contains(commands[placedAt], "exec 9<"+quoted(routingLock)+"\nflock -x 9") {
		t.Errorf("the placement runs\n%s\nwithout the routing lock, so a deploy that wrote the table after this one can see its rendering overwritten by this one's", commands[placedAt])
	}
	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	if want := string(mustPlace(t, state)); stood.placed != want {
		t.Errorf("the switchboard placed %q, want the rendering of the table the claim wrote, %q", stood.placed, want)
	}
	if expected := expectedDigest(commands[placedAt]); expected != digested(stood.held) {
		t.Errorf("the placement is compared against table digest %s, want %s, the one the claim wrote", expected, digested(stood.held))
	}
}

func TestAPlacementTheSwitchboardRefusesPutsTheTableBackAndSaysWhy(t *testing.T) {
	t.Parallel()

	stood := adoptedBox(t, routed())
	prior, placedBefore := stood.held, stood.placed
	stood.refused = "open /data/coolify/proxy/dynamic/.ocel.123.tmp: read-only file system"
	err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil {
		t.Fatal("ClaimHosts() over a placement the switchboard refused = nil, want the claim refused")
	}
	if !strings.Contains(err.Error(), stood.refused) {
		t.Errorf("the claim refused with %q, which never says why the placement failed", err)
	}
	if stood.held != prior {
		t.Errorf("%s holds\n%s\nafter a placement that failed, want the table before the claim\n%s\nthe switchboard would route a hostname the proxy in front of it was never told of", live.RoutingTable, stood.held, prior)
	}
	if stood.placed != placedBefore {
		t.Errorf("the placed file reads %q, want it as it stood, %q", stood.placed, placedBefore)
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

func TestAPlacedFileThatDriftedUnderATableThatDidNotIsPlacedWithoutRewritingTheTable(t *testing.T) {
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
}

func TestAPlacedFileThatHoldsWhatTheTableRendersIsLeftAlone(t *testing.T) {
	t.Parallel()

	stood := adoptedBox(t, RoutingTable{Grace: DrainWindow, Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: retired}}})
	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if placed := stood.count(places); placed != 0 {
		t.Errorf("a release onto a placed file that already holds what the table renders placed it %d times, want never: every placement is a rewrite the proxy watching the directory reloads", placed)
	}
}

func TestThePlacementFeedsTheSwitchboardOnlyWhileTheTableIsTheOneItRenders(t *testing.T) {
	t.Parallel()

	for _, needed := range []string{"sh", "flock", "sha256sum", "cut"} {
		if _, err := exec.LookPath(needed); err != nil {
			t.Skipf("no %s on this machine, and the placement under test is the shell one a box runs", needed)
		}
	}
	dir, bin := t.TempDir(), t.TempDir()
	table := filepath.Join(dir, "table.json")
	written := mustWrite(t, routed())
	if err := os.WriteFile(table, written, 0o640); err != nil {
		t.Fatal(err)
	}
	fedTo, asked := filepath.Join(dir, "fed"), filepath.Join(dir, "asked")
	executable(t, filepath.Join(bin, "docker"), "#!/bin/sh\nprintf '%s\\n' \"$*\" > "+quoted(asked)+"\ncat > "+quoted(fedTo)+"\n")
	here := strings.NewReplacer(live.RoutingTable, table, routingLock, dir).Replace

	placement := func(expected string) error {
		run := exec.Command("/bin/sh", "-c", here(placedWrite(coolifyFile, tableDigest(expected))))
		run.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		run.Stdin = strings.NewReader("routes shop.example.com\n")
		return run.Run()
	}

	var exited *exec.ExitError
	if err := placement(digested("a table another deploy has since replaced")); !errors.As(err, &exited) || exited.ExitCode() != routingMoved {
		t.Errorf("a placement over a table that moved = %v, want exit %d: the deploy that moved it places its own rendering", err, routingMoved)
	}
	if _, err := os.Stat(asked); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a placement over a table that moved still asked the switchboard to place, leaving a rendering of a table the box no longer holds")
	}

	if err := placement(digested(string(written))); err != nil {
		t.Fatalf("a placement over the table it renders = %v", err)
	}
	if got := held(t, fedTo); got != "routes shop.example.com\n" {
		t.Errorf("the switchboard was fed %q, want the rendering whole", got)
	}
	if got, want := strings.TrimSpace(held(t, asked)), strings.Join(switchboardFed("place", coolifyFile)[1:], " "); got != want {
		t.Errorf("docker was asked %q, want %q", got, want)
	}
}
