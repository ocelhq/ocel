package host

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type engineHolding struct {
	network bool
	volume  bool
	facts   string
}

func holding() engineHolding {
	return engineHolding{network: true, volume: true, facts: string(containerItem().Content)}
}

func dockered(t *testing.T, held engineHolding) map[string]string {
	t.Helper()
	dir := t.TempDir()
	facts := filepath.Join(dir, "facts")
	if err := os.WriteFile(facts, []byte(held.facts), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\ncase \"$1 $2\" in\n" +
		"'network inspect') exit " + answering(held.network) + " ;;\n" +
		"'volume inspect') exit " + answering(held.volume) + " ;;\n" +
		"esac\n" +
		"case \"$1\" in\n" +
		"inspect) [ -s " + quoted(facts) + " ] || exit 1; cat " + quoted(facts) + " ;;\n" +
		"*) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, dockerEngine), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"sha256sum", "cut", "cat"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(found, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sh", "-c", strings.Join([]string{networkProbe(), volumeProbe(), containerProbe()}, "\n"))
	cmd.Env = []string{"PATH=" + dir}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	rendered, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe the proxy: %v\n%s", err, stderr.String())
	}
	observed, _, err := readSurvey(string(rendered))
	if err != nil {
		t.Fatal(err)
	}
	return observed
}

func answering(stood bool) string {
	if stood {
		return "0"
	}
	return "1"
}

func proxyItem(kind string) Item {
	for _, item := range ProxyItems() {
		if item.Kind == kind {
			return item
		}
	}
	return Item{}
}

func baseline(t *testing.T) map[string]any {
	t.Helper()
	var read map[string]any
	if err := json.Unmarshal(proxyBaseline, &read); err != nil {
		t.Fatalf("the config the proxy is started from is not json: %v", err)
	}
	return read
}

func nested(t *testing.T, read map[string]any, path ...string) any {
	t.Helper()
	var held any = read
	for at, key := range path {
		nested, mapped := held.(map[string]any)
		if !mapped {
			t.Fatalf("the proxy config holds no %s: %s is not an object", strings.Join(path, "."), strings.Join(path[:at], "."))
		}
		held = nested[key]
	}
	return held
}

func TestTheProbeAndTheWriteAgreeOnWhatAServingProxyIs(t *testing.T) {
	t.Parallel()

	observed := dockered(t, holding())
	for _, item := range ProxyItems() {
		if item.Kind == KindFile || item.Kind == KindDir {
			continue
		}
		if observed[item.ID()] != item.Digest() {
			t.Errorf("the probe read %q of %s on a host whose proxy serves, want %q: a proxy nothing can call current is one every run installs again",
				observed[item.ID()], item.ID(), item.Digest())
		}
	}
}

func TestAHostWithNoEngineCarriesNoProxyAtAll(t *testing.T) {
	t.Parallel()

	observed := dockered(t, engineHolding{})
	for _, item := range ProxyItems() {
		if item.Kind == KindFile || item.Kind == KindDir {
			continue
		}
		if _, stood := observed[item.ID()]; stood {
			t.Errorf("the probe read %s on a host that runs no containers at all", item.ID())
		}
	}
}

func TestAProxyThatIsGoneIsPlannedBackAndAStandingOneIsLeftAlone(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	whole := Reading{Class: class, Keys: keys, Observed: digests(Items(class, keys))}
	for _, change := range planned(whole) {
		if change.Kind == KindContainer && change.Action != providerkit.ActionKeep {
			t.Errorf("a re-run over a host whose proxy serves plans %q for it, and a bootstrap that reinstalls what stands is one nobody dares re-run", change.Action)
		}
	}

	torn := digests(Items(class, keys))
	delete(torn, containerItem().ID())
	gone := planFor(planned(Reading{Class: class, Keys: keys, Observed: torn}), containerItem().ID())
	if gone.Action != providerkit.ActionCreate {
		t.Errorf("a host whose proxy container was removed plans %q for it, want it written back: the proxy is state this host holds rather than a deploy's side effect", gone.Action)
	}
	if !gone.Slow {
		t.Error("pulling the proxy image is planned as quick work, and a plan that lies about its cost is one nobody waits through")
	}

	stopped := digests(Items(class, keys))
	stopped[containerItem().ID()] = digest(KindContainer, proxyContainer, 0, rootOwner,
		contentSum([]byte(strings.Replace(string(containerItem().Content), "state=running", "state=exited", 1))))
	idle := Reading{Class: class, Keys: keys, Observed: stopped}
	if idle.current(containerItem()) {
		t.Fatal("a proxy container that has exited reads as serving, and nothing would ever start it")
	}
	if woken := planFor(planned(idle), containerItem().ID()); woken.Action != providerkit.ActionUpdate {
		t.Errorf("a host whose proxy has exited plans %q for it, want it brought back to serving", woken.Action)
	}
}

func TestTheProxyIsPinnedByDigestAndNamedByNoTagAnywhere(t *testing.T) {
	t.Parallel()

	repo, hashed, split := strings.Cut(proxyImage, "@sha256:")
	if !split || len(hashed) != 64 || strings.Trim(hashed, "0123456789abcdef") != "" {
		t.Fatalf("the proxy is pulled as %q, want a repository and a sha256 digest: a tag is a name its owner can repoint", proxyImage)
	}
	if strings.Contains(repo, ":") {
		t.Errorf("the proxy is pulled as %q, and the tag in it is what the digest was written to replace", proxyImage)
	}
	for what, written := range map[string]string{
		"the run command":  containerCommand(),
		"the item's facts": string(containerItem().Content),
		"the plan's note":  containerItem().Note,
	} {
		if strings.Count(written, proxyImage) == 0 {
			t.Errorf("%s never names %s", what, proxyImage)
		}
		if strings.Contains(strings.ReplaceAll(written, proxyImage, ""), "caddy:") {
			t.Errorf("%s carries a tag reference beside the digest:\n%s", what, written)
		}
	}
}

func TestTheProxyIsRestartedUnlessSomebodyStopsItAndSitsOnTheOneSharedNetwork(t *testing.T) {
	t.Parallel()

	command := containerCommand()
	for _, flag := range []string{
		quoted("--restart") + " " + quoted(proxyRestart),
		quoted("--network") + " " + quoted(proxyNetwork),
	} {
		if !strings.Contains(command, flag) {
			t.Errorf("the proxy is run without %s:\n%s", flag, command)
		}
	}
	facts := string(containerItem().Content)
	for _, fact := range []string{"restart=" + proxyRestart, "networks=" + proxyNetwork + " ", "state=running"} {
		if !strings.Contains(facts, fact) {
			t.Errorf("the proxy is surveyed without %q, so a host that lost it would never be told:\n%s", fact, facts)
		}
	}
	if network := proxyItem(KindNetwork); network.Name != proxyNetwork {
		t.Errorf("the network every target resolves across is %q, want %q written as state of its own", network.Name, proxyNetwork)
	}
}

func TestTheControlPlaneBindsAUnixSocketAndNothingPublishesAPortForIt(t *testing.T) {
	t.Parallel()

	if listen := nested(t, baseline(t), "admin", "listen"); listen != "unix/"+ProxyAdminSocket {
		t.Fatalf("the admin endpoint listens on %v, want the unix socket at %s: an endpoint with no authentication is one that must bind nothing a peer can dial",
			listen, ProxyAdminSocket)
	}
	if strings.Contains(string(proxyBaseline), "2019") {
		t.Errorf("the proxy config names caddy's default admin port, and the whole of this pick is that nothing listens on it:\n%s", proxyBaseline)
	}
	command := containerCommand()
	if strings.Count(command, "--publish") != 1 || !strings.Contains(command, quoted("--publish")+" "+quoted(proxyPort+":"+proxyPort)) {
		t.Errorf("the proxy is run with something other than the one published port %s:\n%s", proxyPort, command)
	}
	ports := map[string][]map[string]string{}
	if err := json.Unmarshal([]byte(marshalled(proxyPorts())), &ports); err != nil {
		t.Fatal(err)
	}
	for port := range ports {
		if port != proxyPort+"/tcp" {
			t.Errorf("the proxy publishes %s, and the only port it may publish is the one requests arrive on", port)
		}
	}
	if strings.Contains(command, ProxyAdminSocket) {
		t.Errorf("the admin socket is named on the host side of the run command, and a socket that leaves the container is one anything on the box can dial:\n%s", command)
	}
}

func TestTheGracePeriodIsStatedRatherThanLeftEternal(t *testing.T) {
	t.Parallel()

	grace, written := nested(t, baseline(t), "apps", "http", "grace_period").(string)
	if !written || grace == "" {
		t.Fatal("the proxy leaves grace_period at its default, which caddy documents as eternal: one hung request would hold a retired server open forever")
	}
}

func TestTheAccessLogRecordsThePathAndNeverTheQueryStringBesideIt(t *testing.T) {
	t.Parallel()

	read := baseline(t)
	logger, named := nested(t, read, "apps", "http", "servers", "ocel", "logs", "default_logger_name").(string)
	if !named || logger == "" {
		t.Fatal("the server writes its access log through no logger of ocel's, so what lands in it is caddy's default rather than a decision")
	}
	encoder := nested(t, read, "logging", "logs", logger, "encoder")
	if format := nested(t, encoder.(map[string]any), "format"); format != "filter" {
		t.Fatalf("the access log is encoded as %v, want a filter: a secret in a query string reaches the log the moment nothing strips it", format)
	}
	uri, filtered := nested(t, encoder.(map[string]any), "fields", "request>uri").(map[string]any)
	if !filtered {
		t.Fatal("nothing filters the request uri, so an oauth callback's code and a pre-signed url's signature land in the proxy's log")
	}
	if uri["filter"] != "regexp" || uri["regexp"] == "" || uri["value"] == "" {
		t.Errorf("the request uri is filtered by %v, want a pattern that replaces the query string with something carrying none of it", uri)
	}
	if strings.Contains(string(proxyBaseline), "log_credentials") {
		t.Errorf("the proxy config touches log_credentials, and caddy redacts the authorization and cookie headers only while nothing does:\n%s", proxyBaseline)
	}
}

func TestWhatTheProxyPersistsGoesNoWiderThanTheProxy(t *testing.T) {
	t.Parallel()

	var data string
	for _, bind := range proxyBinds() {
		source, rest, _ := strings.Cut(bind, ":")
		mount, options, _ := strings.Cut(rest, ":")
		if mount == proxyDataMount {
			data = source
			continue
		}
		if !strings.HasPrefix(source, "/") {
			t.Errorf("%s is bound from %q, which names nothing on this host", mount, source)
		}
		if options != "ro" {
			t.Errorf("%s is bound %q, and everything ocel hands the proxy is ocel's to write and the proxy's to read", mount, options)
		}
	}
	if data == "" {
		t.Fatalf("nothing holds %s, so caddy's autosaved config and the certificates it issues live in a layer a recreate takes with it", proxyDataMount)
	}
	if strings.Contains(data, "/") {
		t.Errorf("%s is bound from %q: caddy's data records the whole route topology and will one day hold private keys, and a host path puts it where anything on the box can read it",
			proxyDataMount, data)
	}
	if data != proxyItem(KindVolume).Name {
		t.Errorf("%s is held by %q and the volume ocel writes is %q, so a destroy leaves what the proxy persisted behind", proxyDataMount, data, proxyItem(KindVolume).Name)
	}
	if !strings.Contains(containerCommand(), quoted("XDG_CONFIG_HOME="+proxyDataMount+"/config")) {
		t.Errorf("caddy's config directory is left where the image puts it, and the autosaved config recording every route lives outside the volume that holds the rest:\n%s", containerCommand())
	}
}

func TestTheHelperIsAFileTheProxyMayReadAndNothingThereMayWrite(t *testing.T) {
	t.Parallel()

	helper := proxyItem(KindFile)
	if helper.Name != ProxyHelper {
		t.Fatalf("the first file the proxy carries is %s, want the helper at %s", helper.Name, ProxyHelper)
	}
	if helper.Owner != rootOwner {
		t.Errorf("%s is owned by %s, want root: the only login that reaches it is the one docker exec already runs as", ProxyHelper, helper.Owner)
	}
	if helper.Mode&0o007 != 0 {
		t.Errorf("%s is written %04o, and a workload that ever runs as anything but root in that container could execute it", ProxyHelper, helper.Mode)
	}
	if helper.Mode&0o100 == 0 {
		t.Errorf("%s is written %04o and nothing may execute it", ProxyHelper, helper.Mode)
	}
	if !slices.Contains(proxyBinds(), ProxyHelper+":"+proxyHelperMount+":ro") {
		t.Errorf("%s is bound into the proxy as something other than a read-only file: %v", ProxyHelper, proxyBinds())
	}
	for _, serving := range []string{"file_server", proxyHelperMount, "\"root\""} {
		if strings.Contains(string(proxyBaseline), serving) {
			t.Errorf("the proxy config names %q, and the helper must sit outside anything the proxy would ever serve or execute from:\n%s", serving, proxyBaseline)
		}
	}
}

func TestTheContainerIsWrittenAgainWhenTheConfigItWasStartedFromMoves(t *testing.T) {
	t.Parallel()

	stamped := contentSum(proxyBaseline)
	if !strings.Contains(containerCommand(), quoted(proxyLabel+"="+stamped)) {
		t.Errorf("the proxy is run carrying no record of the config behind it, so a changed baseline would never reach the running proxy:\n%s", containerCommand())
	}
	if !strings.Contains(string(containerItem().Content), "config="+stamped) {
		t.Errorf("the proxy is surveyed without the config it was started from:\n%s", containerItem().Content)
	}
}

func TestHealNeverWritesOverTheConfigTheProxyIsServingFrom(t *testing.T) {
	t.Parallel()

	for _, item := range ProxyItems() {
		if deployOwned(item) {
			t.Errorf("heal may write %s, and the one unattended path with nobody watching would replace the routes every deployed app is reached through", item.ID())
		}
	}
}

func TestAnUnattendedApplyWritesOcelsOwnProxyBackWithoutAsking(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	observed := digests(Items(class, keys))
	observed[containerItem().ID()] = digest(KindContainer, proxyContainer, 0, rootOwner, contentSum([]byte("state=exited\n")))
	observed[networkItem().ID()] = digest(KindNetwork, proxyNetwork, 0, rootOwner, contentSum([]byte("moved\n")))

	read := Reading{Class: class, Keys: keys, Observed: observed}
	if err := refuseReplacements(read, Items(class, keys)); err != nil {
		t.Errorf("an unattended apply over a host whose proxy has moved = %v, want it written back: the proxy carries ocel's own name and nothing of the user's", err)
	}
}

func TestDestroyTakesOcelsProxyAndLeavesEveryContainerTheHostRuns(t *testing.T) {
	t.Parallel()

	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	keys := []byte(aKey + "\n")
	standing := Reading{Class: production, Keys: keys, Observed: digests(Items(production, keys))}
	beside := Reading{Class: preview, Keys: keys, Observed: digests(Items(preview, keys))}
	proxied := []string{proxyContainer, proxyVolume, proxyNetwork, proxyRoot, ProxyHelper}

	for _, taken := range removing(standing, beside) {
		if slices.Contains(proxied, taken.path) && taken.action == providerkit.ActionDelete {
			t.Errorf("destroying one class takes %s, and the sibling class still standing on this host deploys through it", taken.path)
		}
	}

	last := removing(standing, Reading{Class: preview, Observed: map[string]string{}})
	for _, path := range proxied {
		gone := removalOf(last, path)
		if gone.action != providerkit.ActionDelete {
			t.Errorf("destroying the last class plans %s as %q, want it taken: what ocel wrote is what ocel takes back", path, gone.action)
		}
	}
	if reason := removalOf(last, proxyContainer).reason; reason == "" {
		t.Error("the proxy is taken with no reason, and the typed confirmation must name what goes before a user types")
	}
	if reason := removalOf(last, proxyVolume).reason; reason == "" {
		t.Error("the proxy's volume is taken with no reason, and every certificate it holds goes with it")
	}
	container := slices.IndexFunc(last, func(r removal) bool { return r.path == proxyContainer })
	for _, after := range []string{proxyVolume, proxyNetwork, proxyRoot} {
		if at := slices.IndexFunc(last, func(r removal) bool { return r.path == after }); at < container {
			t.Errorf("%s is taken at %d and the container using it at %d, and nothing takes what a running container holds", after, at, container)
		}
	}
	if kept := removalOf(last, dockerEngine); kept.action != providerkit.ActionKeep {
		t.Errorf("destroying the last class plans the engine as %q, want it kept with every container it runs", kept.action)
	}
}

func TestRemovingTheProxyNamesOcelsOwnContainerAndNeverAsksTheEngineWhatElseItRuns(t *testing.T) {
	t.Parallel()

	for _, taken := range proxyRemovals() {
		command := removal{kind: taken.kind, path: taken.path}.command()
		if taken.kind == KindDir {
			continue
		}
		if !strings.HasSuffix(strings.TrimSuffix(command, " || true"), quoted(taken.path)) {
			t.Errorf("%s is taken by %q, want a command naming ocel's own %s and nothing else", taken.path, command, taken.kind)
		}
		for _, sweeping := range []string{"ps", "--all", "-a", "prune", "--filter"} {
			if strings.Contains(command, " "+sweeping) {
				t.Errorf("%s is taken by %q, which asks the engine what else it runs: a destroy reaches one name it wrote", taken.path, command)
			}
		}
	}
	if command := (removal{kind: KindNetwork, path: proxyNetwork}).command(); !strings.HasSuffix(command, "|| true") {
		t.Errorf("the network is taken by %q, and a destroy fails on a host still running something attached to it", command)
	}
}

func TestNothingButWhatOcelWroteIsEverOcelsToTake(t *testing.T) {
	t.Parallel()

	stood := machine(map[providerkit.Class][]Item{providerkit.ClassProduction: bootstrapped(t, providerkit.ClassProduction)})
	for _, kind := range []string{KindEngine, KindUnit} {
		err := stood.host().remove(context.Background(), removal{kind: kind, path: dockerEngine, action: providerkit.ActionDelete})
		if err == nil {
			t.Fatalf("removing %s landed, and what this host runs stays when ocel goes", kind)
		}
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("removing %s said %q, want it refused by name", kind, err)
		}
	}
	for _, command := range stood.commands() {
		if strings.HasPrefix(command, "docker ") {
			t.Errorf("refusing a removal ocel never wrote still ran %q on the host", command)
		}
	}
}

func TestTheHelperIsShellAHostCanRunAndRefusesAConfigThatWouldMoveTheAdminEndpoint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	helper := filepath.Join(dir, "proxyctl")
	if err := os.WriteFile(helper, proxyctlScript, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/sh", "-n", helper).CombinedOutput(); err != nil {
		t.Fatalf("the helper is not shell this host can read: %v\n%s", err, out)
	}

	away := filepath.Join(dir, "away.json")
	if err := os.WriteFile(away, []byte(`{"apps":{"http":{"servers":{}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", helper, "flip", away).CombinedOutput()
	if err == nil {
		t.Fatalf("the helper flipped a config declaring no admin endpoint, and caddy moves the admin endpoint before it validates the rest: the socket goes and a tcp listener takes its place\n%s", out)
	}
	if !strings.Contains(string(out), ProxyAdminSocket) {
		t.Errorf("the helper refused with %q, want it to name the socket the config must keep", out)
	}
}
