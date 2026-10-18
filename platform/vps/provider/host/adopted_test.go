package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type userProxy struct {
	file   string
	handed *proxy.Spec
	routes map[string]string
	asked  *[][]string

	invalid  string
	unloaded *[]string
	reloaded *[]proxy.Spec
	origins  []proxy.OriginFile
}

func (y userProxy) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (y userProxy) Render(spec proxy.Spec) ([]byte, error) {
	if y.handed != nil {
		*y.handed = spec
	}
	return []byte("routes " + strings.Join(spec.Hostnames, " ") + "\n"), nil
}

func (y userProxy) File() string { return y.file }

func (userProxy) Unrendered([]byte, proxy.Permission) string { return "" }

func (y userProxy) RefuseRouted(_ context.Context, hostnames []string) error {
	if y.asked != nil {
		*y.asked = append(*y.asked, hostnames)
	}
	for _, hostname := range hostnames {
		if router, routed := y.routes[hostname]; routed {
			return refusal.Refuse(refusal.CodeBusy, "%s is already routed by your proxy's router %s", hostname, router)
		}
	}
	return nil
}

func (y userProxy) Validate(_ context.Context, rendered []byte) error {
	if y.invalid != "" && len(rendered) > 0 {
		return refusal.Refuse(refusal.CodeInvalid, "your proxy refuses %q: %s", rendered, y.invalid)
	}
	return nil
}

func (y userProxy) Reload(_ context.Context, served proxy.Spec) error {
	if y.reloaded != nil {
		*y.reloaded = append(*y.reloaded, served)
	}
	if y.unloaded == nil || len(*y.unloaded) == 0 {
		return nil
	}
	refused := (*y.unloaded)[0]
	*y.unloaded = (*y.unloaded)[1:]
	return refusal.Refuse(refusal.CodeNotReady, "%s", refused)
}

func (userProxy) Inspect(context.Context) (proxy.Checks, error) { return nil, nil }

func (userProxy) RefuseUnshielded(context.Context, string) error { return nil }

func (y userProxy) OriginFiles(proxy.Spec) ([]proxy.OriginFile, error) { return y.origins, nil }

func (userProxy) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func TestAProxyIsHandedEveryHostnameTheBoxServesAndThePreviewBase(t *testing.T) {
	t.Parallel()

	state := previewing()
	state.Claims = []boxapi.Claim{
		{Hostname: claimed, Owner: surface, Pointer: pointed},
		{Hostname: "pr-7." + previewBase, Owner: surface, Pointer: "@pr-7"},
	}
	state.Connector = "console.example.com"
	var handed proxy.Spec
	if _, err := routing.RenderProxyConfig(userProxy{handed: &handed}, state); err != nil {
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

func TestTheSwitchboardMountsNoDirectoryOfYourProxysSoMovingItLeavesTheSwitchboardAsItIs(t *testing.T) {
	t.Parallel()

	for what, front := range map[string]front.Proxy{"ocel's own proxy": {}, "a proxy routed by hand": routedByHand(), "Coolify's Traefik": coolifysTraefik(), "Coolify's Caddy": coolifysCaddy()} {
		board := switchboardOf(t, front)
		for _, bound := range board.Binds {
			if !strings.HasSuffix(bound, ":ro") && !slices.Contains([]string{boxapi.SwitchboardControlDir, boxapi.FrontDir}, strings.SplitN(bound, ":", 2)[0]) {
				t.Errorf("the switchboard beside %s is bound %s read-write, want nothing ocel places bound into it", what, bound)
			}
		}
		if slices.ContainsFunc(board.Env, func(set string) bool { return strings.HasPrefix(set, boxapi.PlaceEnv+"=") }) {
			t.Errorf("the switchboard beside %s runs with %q, want no directory to place in", what, board.Env)
		}
	}
	moved := coolifysTraefik()
	moved.Traefik.Directory = "/data/coolify/proxy/elsewhere"
	if !bytes.Equal(switchboardOf(t, moved).Facts(), switchboardOf(t, coolifysTraefik()).Facts()) {
		t.Error("the switchboard's facts change with the directory your proxy reads, so moving the directory starts it again and drops the requests it serves")
	}
}

func TestAFileIsPlacedByAContainerThatMountsOnlyItsDirectoryAndTheSwitchboardBinary(t *testing.T) {
	t.Parallel()

	placing := routing.PlacementFed(coolifyDynamic, "place", coolifyFile)
	var mounted []string
	for at, arg := range placing {
		switch arg {
		case "--volume":
			t.Errorf("the placement runs as %q, want no --volume: docker creates a missing directory for it, empty and root-owned where your proxy reads", placing)
		case "--mount":
			mounted = append(mounted, placing[at+1])
		}
	}
	want := []string{
		"type=bind,source=" + boxapi.SwitchboardDir + ",target=" + boxapi.SwitchboardMount + ",readonly",
		"type=bind,source=" + coolifyDynamic + ",target=" + coolifyDynamic,
	}
	if !slices.Equal(mounted, want) {
		t.Errorf("the placement mounts %q, want %q", mounted, want)
	}
	if at := slices.Index(placing, boxapi.PlaceEnv+"="+coolifyDynamic); at < 1 || placing[at-1] != "--env" {
		t.Errorf("the placement runs as %q, want %s set to %s: it refuses to place anywhere else", placing, boxapi.PlaceEnv, coolifyDynamic)
	}
	for _, want := range []string{"--rm", "-i", "--read-only"} {
		if !slices.Contains(placing, want) {
			t.Errorf("the placement runs as %q, want %s", placing, want)
		}
	}
	if at := slices.Index(placing, "--network"); at < 0 || placing[at+1] != "none" {
		t.Errorf("the placement runs as %q, want it on no network", placing)
	}
	if slices.Contains(placing, "--name") {
		t.Errorf("the placement runs as %q, want it unnamed so two deploys placing at once never collide", placing)
	}
}

func TestAPlacementMountsADirectoryWhoseNameHasACommaAsThatDirectory(t *testing.T) {
	t.Parallel()

	placing := routing.Placement("/srv/a,b", "placed", "/srv/a,b/ocel.yml")
	if !slices.Contains(placing, `type=bind,"source=/srv/a,b","target=/srv/a,b"`) {
		t.Errorf("the placement runs as %q, want the directory quoted inside its --mount", placing)
	}
}

const coolifyFile = coolifyDynamic + "/ocel.yml"

func places(command string) bool { return strings.Contains(command, shell.Quote("place")+" ") }

func sums(command string) bool { return strings.Contains(command, shell.Quote("placed")+" ") }

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

func adoptedBox(t *testing.T, state boxapi.RoutingTable) *adoptedBench {
	t.Helper()

	adopted := &adoptedBench{claimBench: &claimBench{bench: newBench(nil), recorded: string(mustWrite(t, state))}}
	adopted.placed = string(mustPlace(t, state))
	absent := ""
	proxied := servesPair(adopted.bench, &adopted.recorded, &absent)
	adopted.answer = func(command string) (ssh.Result, bool) {
		switch {
		case sums(command):
			adopted.mu.Lock()
			defer adopted.mu.Unlock()
			if adopted.summing != nil {
				adopted.summing(adopted)
				adopted.summing = nil
			}
			if adopted.gone {
				return ssh.Result{}, true
			}
			return ssh.Result{Stdout: digested(adopted.placed) + "\n"}, true
		case writesProxy(command):
			adopted.mu.Lock()
			defer adopted.mu.Unlock()
			if expected := expectedDigest(command); expected != digested(adopted.recorded) {
				return ssh.Result{Code: routing.ExitMoved, Stderr: digested(adopted.recorded)}, true
			}
			fed := adopted.fed[len(adopted.fed)-1]
			adopted.recorded = tableOf(fed)
			if !places(command) {
				return ssh.Result{Stdout: digested(adopted.recorded)}, true
			}
			if refused := adopted.refusal(); refused != "" {
				return ssh.Result{Code: routing.ExitPlaceFailed, Stdout: digested(adopted.recorded), Stderr: refused}, true
			}
			adopted.placed, adopted.gone = configOf(fed), false
			return ssh.Result{Stdout: digested(adopted.recorded)}, true
		case places(command):
			adopted.mu.Lock()
			defer adopted.mu.Unlock()
			if expected := expectedDigest(command); expected != digested(adopted.recorded) {
				return ssh.Result{Code: routing.ExitMoved, Stderr: digested(adopted.recorded)}, true
			}
			if refused := adopted.refusal(); refused != "" {
				return ssh.Result{Code: routing.ExitPlaceFailed, Stderr: refused}, true
			}
			adopted.placed, adopted.gone = adopted.fed[len(adopted.fed)-1], false
			return ssh.Result{}, true
		case gates(command), cutsOver(command):
			return ssh.Result{}, true
		case idles(command):
			return everyIdle(command), true
		default:
			return proxied(command)
		}
	}
	return adopted
}

func (a *adoptedBench) host() *Host {
	h := a.fronted(routedByHand())
	h.useFront(userProxy{file: coolifyFile})
	return h
}

func mustPlace(t *testing.T, state boxapi.RoutingTable) []byte {
	t.Helper()
	rendered, err := routing.RenderProxyConfig(userProxy{file: coolifyFile}, state)
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}

func placedUnderLock(t *testing.T, command string) {
	t.Helper()
	locked := strings.Index(command, strings.TrimSuffix(routing.Locked("-x"), "\n"))
	compared := strings.Index(command, `if [ "$current" != `)
	placing := strings.Index(command, shell.Words(routing.PlacementFed(coolifyDynamic, "place", coolifyFile)))
	if locked < 0 || compared < locked || placing < compared {
		t.Errorf("the placement runs\n%s\nwant it through the switchboard, fed on stdin, after the table is compared under the routing lock: a deploy that wrote the table after this one can otherwise see its rendering overwritten by this one's", command)
	}
}

func TestAClaimOnABoxWhoseProxyKeepsItsFileElsewherePlacesItsRenderingUnderTheLockItWritesTheTableUnder(t *testing.T) {
	t.Parallel()

	adopted := adoptedBox(t, routed())
	if err := adopted.host().routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	commands := adopted.commands()
	wrote := slices.IndexFunc(commands, writesProxy)
	if wrote < 0 {
		t.Fatalf("the claim ran %q and never wrote the table", commands)
	}
	written := commands[wrote]
	if strings.Contains(written, ProxyConfig) {
		t.Errorf("the table write asks for %s, which a proxy that keeps its file elsewhere never reads: %s", ProxyConfig, written)
	}
	placedUnderLock(t, written)
	if moved, placing := strings.Index(written, `mv "$staged" `), strings.Index(written, shell.Words(routing.PlacementFed(coolifyDynamic, "place", coolifyFile))); moved < 0 || placing < moved {
		t.Errorf("the write runs\n%s\nwant the table moved into place before the rendering is placed: the table is what every later write and rollback reads", written)
	}
	if placed := adopted.count(places); placed != 1 {
		t.Errorf("the claim placed %d times, want once, in the write that holds the lock: %q", placed, commands)
	}
	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	if want := string(mustPlace(t, state)); adopted.placed != want {
		t.Errorf("the switchboard placed %q, want the rendering of the table the claim wrote, %q", adopted.placed, want)
	}
}

func TestAClaimOfAHostnameYourProxyAlreadyRoutesIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	t.Parallel()

	adopted := adoptedBox(t, routed())
	prior, placedBefore := adopted.recorded, adopted.placed
	h := adopted.host()
	h.useFront(userProxy{file: coolifyFile, routes: map[string]string{claimed: "dapp-lekbai-router-websecure-1"}})
	err := h.routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil || !strings.Contains(err.Error(), "dapp-lekbai-router-websecure-1") {
		t.Fatalf("ClaimHosts() = %v, want the claim refused naming the router that routes it", err)
	}
	if adopted.count(writesProxy) != 0 || adopted.count(places) != 0 {
		t.Errorf("a refused claim ran %q, want nothing written: ocel's router would outrank the one you have", adopted.commands())
	}
	if adopted.recorded != prior || adopted.placed != placedBefore {
		t.Errorf("a refused claim left the table %q and the placed file %q, want both untouched", adopted.recorded, adopted.placed)
	}
}

func TestYourProxyIsAskedOnlyAboutTheHostnamesAWriteAdds(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	adopted := adoptedBox(t, state)
	var asked [][]string
	h := adopted.host()
	h.useFront(userProxy{file: coolifyFile, asked: &asked})
	if err := h.routing.ClaimHosts(context.Background(), state.Claims); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if err := h.routing.InstallPreviewEntry(context.Background(), previewBase); err != nil {
		t.Fatalf("InstallPreviewEntry() = %v", err)
	}
	want := [][]string{{edge.ProbeHostname(edge.PreviewWildcard(previewBase))}}
	if !slices.EqualFunc(asked, want, slices.Equal) {
		t.Errorf("your proxy was asked about %q, want only %q: a hostname the box already serves was vetted when it was claimed", asked, want)
	}
}

func TestARenderingYourProxyRefusesIsNeverPlacedAndNothingIsWritten(t *testing.T) {
	t.Parallel()

	adopted := adoptedBox(t, routed())
	prior, placedBefore := adopted.recorded, adopted.placed
	h := adopted.host()
	h.useFront(userProxy{file: coolifyFile, invalid: "Error: /dev/stdin:2: unrecognized directive: bogus"})
	err := h.routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil || !strings.Contains(err.Error(), "unrecognized directive: bogus") {
		t.Fatalf("ClaimHosts() = %v, want the claim refused with what your proxy said of the rendering", err)
	}
	if adopted.count(writesProxy) != 0 || adopted.count(places) != 0 {
		t.Errorf("a claim whose rendering your proxy refuses ran %q, want nothing written: a file your proxy cannot read can drop every site it imports", adopted.commands())
	}
	if adopted.recorded != prior || adopted.placed != placedBefore {
		t.Errorf("a refused rendering left the table %q and the placed file %q, want both untouched", adopted.recorded, adopted.placed)
	}
}

func TestADriftedFileWhoseRenderingYourProxyRefusesIsNotPlacedAgain(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	adopted := adoptedBox(t, state)
	adopted.gone = true
	h := adopted.host()
	h.useFront(userProxy{file: coolifyFile, invalid: "Error: /dev/stdin:1: unexpected token"})
	if err := h.routing.ClaimHosts(context.Background(), state.Claims); err == nil || !strings.Contains(err.Error(), "unexpected token") {
		t.Errorf("ClaimHosts() = %v, want it refused with what your proxy said of the rendering", err)
	}
	if adopted.count(places) != 0 || !adopted.gone {
		t.Errorf("a rendering your proxy refuses was placed: %q", adopted.commands())
	}
}

func TestAReloadYourProxyRefusesPutsThePreviousFileBackAndSaysWhatYourProxySaid(t *testing.T) {
	t.Parallel()

	adopted := adoptedBox(t, routed())
	prior, placedBefore := adopted.recorded, adopted.placed
	said := `Error: sending configuration to instance: caddy responded with error: HTTP 400: {"error":"loading config: ambiguous site definition: shop.example.com"}`
	h := adopted.host()
	h.useFront(userProxy{file: coolifyFile, unloaded: &[]string{said}})
	err := h.routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil || !strings.Contains(err.Error(), said) {
		t.Fatalf("ClaimHosts() = %v, want the claim refused carrying what your proxy said verbatim", err)
	}
	if adopted.placed != placedBefore {
		t.Errorf("the placed file reads %q after a reload your proxy refused, want the previous file put back, %q", adopted.placed, placedBefore)
	}
	if adopted.recorded != prior {
		t.Errorf("%s reads\n%s\nafter a reload your proxy refused, want the table before the claim\n%s", boxapi.RoutingTablePath, adopted.recorded, prior)
	}
}

func TestYourProxyIsToldWhatItServedBeforeAWriteSoItWaitsOnlyOnWhatTheWriteChanged(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	adopted := adoptedBox(t, state)
	var reloaded []proxy.Spec
	h := adopted.host()
	h.useFront(userProxy{file: coolifyFile, reloaded: &reloaded})
	if err := h.routing.InstallPreviewEntry(context.Background(), previewBase); err != nil {
		t.Fatalf("InstallPreviewEntry() = %v", err)
	}
	if len(reloaded) != 1 || !slices.Equal(reloaded[0].Hostnames, []string{claimed}) {
		t.Errorf("the reload was handed %+v, want once the spec of the table before the write, serving %s: a hostname the proxy already served as it was is not waited on again", reloaded, claimed)
	}
}

func TestYourProxyIsToldItServedNothingKnownWhenThePlacedFileWasRewritten(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	adopted := adoptedBox(t, state)
	adopted.placed = "routes something a host tool's editor wrote\n"
	var reloaded []proxy.Spec
	h := adopted.host()
	h.useFront(userProxy{file: coolifyFile, reloaded: &reloaded})
	if err := h.routing.InstallPreviewEntry(context.Background(), previewBase); err != nil {
		t.Fatalf("InstallPreviewEntry() = %v", err)
	}
	if len(reloaded) != 1 || len(reloaded[0].Hostnames) != 0 {
		t.Errorf("the reload was handed %+v, want once an empty spec: the file was not what ocel rendered, so every hostname in the new one is waited on", reloaded)
	}
}

func TestAPlacementTheSwitchboardRefusesPutsTheTableAndThePlacedFileBackAndSaysWhy(t *testing.T) {
	t.Parallel()

	adopted := adoptedBox(t, routed())
	prior, placedBefore := adopted.recorded, adopted.placed
	refused := "open /data/coolify/proxy/dynamic/.ocel.123.tmp: read-only file system"
	adopted.refused = []string{refused}
	err := adopted.host().routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil {
		t.Fatal("ClaimHosts() over a placement the switchboard refused = nil, want the claim refused")
	}
	if !strings.Contains(err.Error(), refused) {
		t.Errorf("the claim refused with %q, which never says why the placement failed", err)
	}
	if strings.Contains(err.Error(), "also failed") {
		t.Errorf("the claim refused with %q, want the table and the file restored", err)
	}
	if adopted.recorded != prior {
		t.Errorf("%s contains\n%s\nafter a placement that failed, want the table before the claim\n%s\nthe switchboard would route a hostname the proxy in front of it was never told of", boxapi.RoutingTablePath, adopted.recorded, prior)
	}
	if adopted.placed != placedBefore {
		t.Errorf("the placed file reads %q, want the rendering of the table put back, %q", adopted.placed, placedBefore)
	}
	if writes := adopted.count(writesProxy); writes != 2 {
		t.Errorf("the claim wrote the table %d times, want twice, once forward and once back: %q", writes, adopted.commands())
	}
}

func TestADeployThatFindsThePlacedFileGoneOrRewrittenPlacesItsRenderingAgain(t *testing.T) {
	t.Parallel()

	serving := boxapi.RoutingTable{Grace: routing.DrainWindow, Routes: []boxapi.Route{{RouteKey: keyed("web"), Upstream: retired}}}
	for what, drift := range map[string]func(*adoptedBench){
		"deleted from the proxy's ui":     func(a *adoptedBench) { a.gone = true },
		"re-serialised by the proxy's ui": func(a *adoptedBench) { a.placed = "routes: {}\n" },
		"emptied by hand":                 func(a *adoptedBench) { a.placed = "" },
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			adopted := adoptedBox(t, serving)
			drift(adopted)
			if err := adopted.host().containers.RollOut(context.Background(), aRollout(), progress.Discard()); err != nil {
				t.Fatalf("Release() = %v", err)
			}
			state, err := routing.ReadRoutingTable([]byte(adopted.recorded))
			if err != nil {
				t.Fatal(err)
			}
			if want := string(mustPlace(t, state)); adopted.placed != want || adopted.gone {
				t.Errorf("a deploy onto a placed file %s leaves it %q (gone %v), want the rendering of the table it wrote, %q", what, adopted.placed, adopted.gone, want)
			}
		})
	}
}

func TestAPlacedFileThatDriftedUnderATableThatDidNotIsPlacedUnderTheLockWithoutRewritingTheTable(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	adopted := adoptedBox(t, state)
	adopted.gone = true
	if err := adopted.host().routing.ClaimHosts(context.Background(), state.Claims); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if want := string(mustPlace(t, state)); adopted.placed != want || adopted.gone {
		t.Errorf("a claim already recorded over a placed file that is gone leaves it %q (gone %v), want %q", adopted.placed, adopted.gone, want)
	}
	if adopted.count(writesProxy) != 0 {
		t.Errorf("a placed file that drifted under a table that did not rewrote the table: %v", adopted.commands())
	}
	commands := adopted.commands()
	placing := commands[slices.IndexFunc(commands, places)]
	placedUnderLock(t, placing)
	if expected := expectedDigest(placing); expected != digested(adopted.recorded) {
		t.Errorf("the placement is compared against table digest %s, want %s, the table it renders", expected, digested(adopted.recorded))
	}
}

func TestAPlacedFileThatDriftedUnderATableAnotherDeployMovesIsPlacedFromTheTableThatDeployLeft(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	adopted := adoptedBox(t, state)
	adopted.gone = true
	other := state
	other.Claims = append(slices.Clone(state.Claims), boxapi.Claim{Hostname: "blog.example.com", Owner: "ocel--blog--production", Pointer: pointed})
	adopted.summing = func(a *adoptedBench) { a.recorded = string(mustWrite(t, other)) }
	if err := adopted.host().routing.ClaimHosts(context.Background(), state.Claims); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if want := string(mustPlace(t, other)); adopted.placed != want || adopted.gone {
		t.Errorf("a placement over a table another deploy moved leaves the file %q (gone %v), want the rendering of the table that deploy left, %q: a placement skipped because the table moved is a hostname the proxy is never told of", adopted.placed, adopted.gone, want)
	}
}

func TestAPlacedFileThatDriftedAndTheSwitchboardRefusesToPlaceFailsTheClaimAndSaysWhy(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	adopted := adoptedBox(t, state)
	adopted.gone = true
	refused := "open /data/coolify/proxy/dynamic/.ocel.123.tmp: no space left on device"
	adopted.refused = []string{refused}
	err := adopted.host().routing.ClaimHosts(context.Background(), state.Claims)
	if err == nil || !strings.Contains(err.Error(), refused) {
		t.Errorf("ClaimHosts() over a placement the switchboard refused = %v, want it refused saying why", err)
	}
	if !adopted.gone {
		t.Errorf("the placed file reads %q after a placement that failed, want it still gone", adopted.placed)
	}
}

func TestAPlacedFileThatMatchesWhatTheTableRendersIsLeftAlone(t *testing.T) {
	t.Parallel()

	adopted := adoptedBox(t, boxapi.RoutingTable{Grace: routing.DrainWindow, Routes: []boxapi.Route{{RouteKey: keyed("web"), Upstream: retired}}})
	if err := adopted.host().containers.RollOut(context.Background(), aRollout(), progress.Discard()); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if adopted.count(writesProxy) == 0 {
		t.Fatal("the release never wrote the table, so this test proves nothing about a write that leaves the rendering as it was")
	}
	if placed := adopted.count(places); placed != 0 {
		t.Errorf("a release onto a placed file that already contains what the table renders placed it %d times, want never: every placement is a rewrite the proxy watching the directory reloads", placed)
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
		"case \"$*\" in *unplace-origins*) exit 0;; esac\n"+
		"printf '%s\\n' \"$*\" > "+shell.Quote(box.asked)+"\n"+
		"cat > "+shell.Quote(box.fed)+"\n"+
		"if [ -f "+shell.Quote(box.refusing)+" ]; then echo 'no space left on device' >&2; exit 2; fi\n")
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
	command := exec.Command("/bin/sh", "-c", strings.NewReplacer(boxapi.RoutingTablePath, b.table, routing.Lock, b.dir).Replace(script))
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
	return strings.TrimSpace(fileContents(t, b.asked))
}

func TestTheWriteFeedsTheSwitchboardTheRenderingOnlyOnceTheTableItRendersIsInPlace(t *testing.T) {
	t.Parallel()

	box := shellPlacing(t)
	before := fileContents(t, box.table)
	after := string(mustWrite(t, previewing()))
	fed := routing.PairFed(routing.Pair{Table: []byte(after), Config: []byte("routes shop.example.com\n")})
	asking := strings.Join(routing.PlacementFed(coolifyDynamic, "place", coolifyFile)[1:], " ")

	if code, _, _ := box.run(t, routing.StagedWrite(routing.Digest(digested("a table another deploy has since replaced")), coolifyFile, nil), fed); code != routing.ExitMoved {
		t.Errorf("a write over a table that moved = %d, want %d", code, routing.ExitMoved)
	}
	if asked := box.asks(t); asked != "" {
		t.Errorf("a write over a table that moved still asked docker %q, leaving a rendering of a table the box no longer has", asked)
	}
	if got := fileContents(t, box.table); got != before {
		t.Errorf("a write over a table that moved left it\n%s\nwant it untouched", got)
	}

	code, out, errs := box.run(t, routing.StagedWrite(routing.Digest(digested(before)), coolifyFile, nil), fed)
	if code != 0 {
		t.Fatalf("a write over the table it read = %d: %s", code, errs)
	}
	if got := fileContents(t, box.table); got != after {
		t.Errorf("the table reads\n%s\nwant the one written", got)
	}
	if strings.TrimSpace(out) != digested(after) {
		t.Errorf("the write said %q, want the digest of the table it wrote", out)
	}
	if got := fileContents(t, box.fed); got != "routes shop.example.com\n" {
		t.Errorf("the switchboard was fed %q, want the rendering whole", got)
	}
	if asked := box.asks(t); asked != asking {
		t.Errorf("docker was asked %q, want %q", asked, asking)
	}
}

func TestAWriteWhoseRenderingTheSwitchboardRefusesSaysSoAndNamesTheTableItLeft(t *testing.T) {
	t.Parallel()

	box := shellPlacing(t)
	before := fileContents(t, box.table)
	after := string(mustWrite(t, previewing()))
	box.refuse(t)

	code, out, errs := box.run(t, routing.StagedWrite(routing.Digest(digested(before)), coolifyFile, nil),
		routing.PairFed(routing.Pair{Table: []byte(after), Config: []byte("routes shop.example.com\n")}))
	if code != routing.ExitPlaceFailed {
		t.Errorf("a write whose placement the switchboard refused = %d, want %d so the deploy puts the table back", code, routing.ExitPlaceFailed)
	}
	if !strings.Contains(errs, "no space left on device") {
		t.Errorf("a write whose placement the switchboard refused said %q, which never says why", errs)
	}
	if strings.TrimSpace(out) != digested(fileContents(t, box.table)) {
		t.Errorf("a write whose placement the switchboard refused said %q, want the digest of the table it left, which the revert compares against", out)
	}
}

func TestAPlacementAloneFeedsTheSwitchboardOnlyWhileTheTableIsTheOneItRenders(t *testing.T) {
	t.Parallel()

	box := shellPlacing(t)
	current := fileContents(t, box.table)

	if code, _, _ := box.run(t, routing.Replacement(routing.Digest(digested("a table another deploy has since replaced")), coolifyFile, nil), "routes shop.example.com\n"); code != routing.ExitMoved {
		t.Errorf("a placement over a table that moved = %d, want %d: the deploy reads the table again and places what it renders", code, routing.ExitMoved)
	}
	if asked := box.asks(t); asked != "" {
		t.Errorf("a placement over a table that moved still asked docker %q, leaving a rendering of a table the box no longer has", asked)
	}

	if code, _, errs := box.run(t, routing.Replacement(routing.Digest(digested(current)), coolifyFile, nil), "routes shop.example.com\n"); code != 0 {
		t.Fatalf("a placement over the table it renders = %d: %s", code, errs)
	}
	if got := fileContents(t, box.fed); got != "routes shop.example.com\n" {
		t.Errorf("the switchboard was fed %q, want the rendering whole", got)
	}
	if asked, want := box.asks(t), strings.Join(routing.PlacementFed(coolifyDynamic, "place", coolifyFile)[1:], " "); asked != want {
		t.Errorf("docker was asked %q, want %q", asked, want)
	}

	box.refuse(t)
	if code, _, errs := box.run(t, routing.Replacement(routing.Digest(digested(current)), coolifyFile, nil), "routes shop.example.com\n"); code != routing.ExitPlaceFailed || !strings.Contains(errs, "no space left on device") {
		t.Errorf("a placement the switchboard refused = %d, %q, want %d saying why", code, errs, routing.ExitPlaceFailed)
	}
}

func TestEveryRoutingFailureNamesTheFilesTheProxyActuallyKeeps(t *testing.T) {
	t.Parallel()

	claim := []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	failing := func(adopted *adoptedBench, result ssh.Result) {
		proxied := adopted.answer
		adopted.answer = func(command string) (ssh.Result, bool) {
			if writesProxy(command) {
				return result, true
			}
			return proxied(command)
		}
	}
	for what, run := range map[string]func(t *testing.T) ([]string, []string, error){
		"a write that fails beside a proxy routed by hand": func(t *testing.T) ([]string, []string, error) {
			adopted := adoptedBox(t, routed())
			failing(adopted, ssh.Result{Code: 1, Stderr: "mv: no space left on device"})
			return []string{boxapi.RoutingTablePath}, []string{ProxyConfig}, adopted.fronted(routedByHand()).routing.ClaimHosts(context.Background(), claim)
		},
		"a write onto an unseeded box beside a proxy routed by hand": func(t *testing.T) ([]string, []string, error) {
			adopted := adoptedBox(t, routed())
			failing(adopted, ssh.Result{Code: routing.ExitUnseeded})
			return []string{boxapi.RoutingTablePath}, []string{ProxyConfig}, adopted.fronted(routedByHand()).routing.ClaimHosts(context.Background(), claim)
		},
		"a release whose write fails beside a proxy routed by hand": func(t *testing.T) ([]string, []string, error) {
			adopted := adoptedBox(t, boxapi.RoutingTable{Grace: routing.DrainWindow, Routes: []boxapi.Route{{RouteKey: keyed("web"), Upstream: retired}}})
			failing(adopted, ssh.Result{Code: 1, Stderr: "mv: no space left on device"})
			return []string{boxapi.RoutingTablePath}, []string{ProxyConfig}, adopted.fronted(routedByHand()).containers.RollOut(context.Background(), aRollout(), progress.Discard())
		},
		"a release whose write fails beside a proxy that keeps its file elsewhere": func(t *testing.T) ([]string, []string, error) {
			adopted := adoptedBox(t, boxapi.RoutingTable{Grace: routing.DrainWindow, Routes: []boxapi.Route{{RouteKey: keyed("web"), Upstream: retired}}})
			failing(adopted, ssh.Result{Code: 1, Stderr: "mv: no space left on device"})
			return []string{boxapi.RoutingTablePath, coolifyFile}, []string{ProxyConfig}, adopted.host().containers.RollOut(context.Background(), aRollout(), progress.Discard())
		},
		"a placement whose revert fails too": func(t *testing.T) ([]string, []string, error) {
			adopted := adoptedBox(t, routed())
			adopted.refused = []string{"no space left on device", "no space left on device"}
			return []string{boxapi.RoutingTablePath, coolifyFile}, []string{ProxyConfig}, adopted.host().routing.ClaimHosts(context.Background(), claim)
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
