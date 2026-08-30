package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
)

const upstreamsPath = "/reverse_proxy/upstreams"

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
	for _, tool := range []string{"sha256sum", "cut", "cat", "sort"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(found, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sh", "-c", strings.Join([]string{networkProbe(), containerProbe()}, "\n"))
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
	for _, item := range ProxyItems(ArchAMD64) {
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
	for _, item := range ProxyItems(ArchAMD64) {
		if item.Kind == KindFile || item.Kind == KindDir || item.Kind == KindProxyConfig {
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
	for _, item := range ProxyItems(ArchAMD64) {
		if item.Kind == KindFile || item.Kind == KindDir || item.Kind == KindProxyConfig {
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
	whole := Reading{Arch: ArchAMD64, Class: class, Keys: keys, Observed: digests(Items(class, keys, ArchAMD64))}
	for _, change := range planned(whole) {
		if change.Kind == KindContainer && change.Action != providerkit.ActionKeep {
			t.Errorf("a re-run over a host whose proxy serves plans %q for it, and a bootstrap that reinstalls what stands is one nobody dares re-run", change.Action)
		}
	}

	torn := digests(Items(class, keys, ArchAMD64))
	delete(torn, containerItem().ID())
	gone := planFor(planned(Reading{Arch: ArchAMD64, Class: class, Keys: keys, Observed: torn}), containerItem().ID())
	if gone.Action != providerkit.ActionCreate {
		t.Errorf("a host whose proxy container was removed plans %q for it, want it written back: the proxy is state this host holds rather than a deploy's side effect", gone.Action)
	}
	if !gone.Slow {
		t.Error("pulling the proxy image is planned as quick work, and a plan that lies about its cost is one nobody waits through")
	}

	stopped := digests(Items(class, keys, ArchAMD64))
	stopped[containerItem().ID()] = digest(KindContainer, ProxyContainer, 0, rootOwner,
		contentSum([]byte(strings.Replace(string(containerItem().Content), "state=running", "state=exited", 1))))
	idle := Reading{Arch: ArchAMD64, Class: class, Keys: keys, Observed: stopped}
	if idle.current(containerItem()) {
		t.Fatal("a proxy container that has exited reads as serving, and nothing would ever start it")
	}
	if woken := planFor(planned(idle), containerItem().ID()); woken.Action != providerkit.ActionUpdate {
		t.Errorf("a host whose proxy has exited plans %q for it, want it brought back to serving", woken.Action)
	}
}

func TestTheProxyIsPinnedByDigestAndNamedByNoTagAnywhere(t *testing.T) {
	t.Parallel()

	repo, hashed, split := strings.Cut(ProxyImage, "@sha256:")
	if !split || len(hashed) != 64 || strings.Trim(hashed, "0123456789abcdef") != "" {
		t.Fatalf("the proxy is pulled as %q, want a repository and a sha256 digest: a tag is a name its owner can repoint", ProxyImage)
	}
	if strings.Contains(repo, ":") {
		t.Errorf("the proxy is pulled as %q, and the tag in it is what the digest was written to replace", ProxyImage)
	}
	for what, written := range map[string]string{
		"the run command":  containerCommand(),
		"the item's facts": string(containerItem().Content),
		"the plan's note":  containerItem().Note,
	} {
		if strings.Count(written, ProxyImage) == 0 {
			t.Errorf("%s never names %s", what, ProxyImage)
		}
		if strings.Contains(strings.ReplaceAll(written, ProxyImage, ""), "caddy:") {
			t.Errorf("%s carries a tag reference beside the digest:\n%s", what, written)
		}
	}
}

func TestTheProxyIsRestartedUnlessSomebodyStopsItAndSitsOnTheOneSharedNetwork(t *testing.T) {
	t.Parallel()

	command := containerCommand()
	for _, flag := range []string{
		quoted("--restart") + " " + quoted(proxyRestart),
		quoted("--network") + " " + quoted(ProxyNetwork),
	} {
		if !strings.Contains(command, flag) {
			t.Errorf("the proxy is run without %s:\n%s", flag, command)
		}
	}
	facts := string(containerItem().Content)
	for _, fact := range []string{"restart=" + proxyRestart, "networks=" + ProxyNetwork + " ", "state=running"} {
		if !strings.Contains(facts, fact) {
			t.Errorf("the proxy is surveyed without %q, so a host that lost it would never be told:\n%s", fact, facts)
		}
	}
	if network := proxyItem(KindNetwork); network.Name != ProxyNetwork {
		t.Errorf("the network every target resolves across is %q, want %q written as state of its own", network.Name, ProxyNetwork)
	}
}

func TestTheControlPlaneBindsAUnixSocketAndNothingPublishesAPortForIt(t *testing.T) {
	t.Parallel()

	if listen := nested(t, baseline(t), "admin", "listen"); listen != caddyadmin.Listen(ProxyAdminSocket) {
		t.Fatalf("the admin endpoint listens on %v, want %s: an endpoint with no authentication is one that must bind nothing a peer can dial, and the mode it is created under is the whole of what stands between it and everything else in the container",
			listen, caddyadmin.Listen(ProxyAdminSocket))
	}
	if !strings.HasSuffix(caddyadmin.Listen(ProxyAdminSocket), "|"+caddyadmin.SocketMode) {
		t.Errorf("the admin endpoint is declared as %s, which names no mode and leaves the socket at whatever the proxy defaults to",
			caddyadmin.Listen(ProxyAdminSocket))
	}
	if strings.Contains(string(proxyBaseline), "2019") {
		t.Errorf("the proxy config names caddy's default admin port, and the whole of this pick is that nothing listens on it:\n%s", proxyBaseline)
	}
	command := containerCommand()
	if strings.Count(command, "--publish") != len(proxyServing()) {
		t.Errorf("the proxy is run with something other than the ports requests arrive on:\n%s", command)
	}
	for _, port := range proxyServing() {
		if !strings.Contains(command, quoted("--publish")+" "+quoted(port+":"+port)) {
			t.Errorf("the proxy does not publish %s, and a request that never reaches it is one the box cannot answer:\n%s", port, command)
		}
	}
	ports := map[string][]map[string]string{}
	if err := json.Unmarshal([]byte(marshalled(proxyPorts())), &ports); err != nil {
		t.Fatal(err)
	}
	for port := range ports {
		if !slices.Contains(proxyServing(), strings.TrimSuffix(port, "/tcp")) {
			t.Errorf("the proxy publishes %s, and the only ports it may publish are the ones requests arrive on", port)
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
	if data != ProxyData {
		t.Errorf("%s is bound from %q, want %s: a docker named volume appears in no removal plan, so a destroy would report a clean teardown and leave every private key and the acme account key on this box",
			proxyDataMount, data, ProxyData)
	}
	held := itemAt(t, ProxyItems(ArchAMD64), KindDir, ProxyData)
	if held.Owner != rootOwner || held.Mode != 0o700 {
		t.Errorf("%s is written as %s at %04o, want root at 0700: it holds every certificate's private key and the acme account key that issues for every other hostname on the account",
			ProxyData, held.Owner, held.Mode)
	}
	if !strings.HasPrefix(ProxyData, proxyRoot+"/") {
		t.Errorf("%s sits outside %s, and only what the bootstrap owns is named in the removal plan a destroy runs", ProxyData, proxyRoot)
	}
	if !strings.Contains(containerCommand(), quoted("XDG_CONFIG_HOME="+proxyDataMount+"/config")) {
		t.Errorf("caddy's config directory is left where the image puts it, and the autosaved config recording every route lives outside the directory that holds the rest:\n%s", containerCommand())
	}
}

func itemAt(t *testing.T, items []Item, kind, name string) Item {
	t.Helper()
	at := slices.IndexFunc(items, func(i Item) bool { return i.Kind == kind && i.Name == name })
	if at < 0 {
		t.Fatalf("the proxy's items carry no %s %s", kind, name)
	}
	return items[at]
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
	if !slices.Contains(proxyBinds(), ProxyHelper+":"+ProxyHelperMount+":ro") {
		t.Errorf("%s is bound into the proxy as something other than a read-only file: %v", ProxyHelper, proxyBinds())
	}
	for _, serving := range []string{"file_server", ProxyHelperMount, "\"root\""} {
		if strings.Contains(string(proxyBaseline), serving) {
			t.Errorf("the proxy config names %q, and the helper must sit outside anything the proxy would ever serve or execute from:\n%s", serving, proxyBaseline)
		}
	}
}

func TestTheContainerIsWrittenAgainWhenTheBaselineBehindItMoves(t *testing.T) {
	t.Parallel()

	stamped := contentSum(proxyBaseline)
	if !strings.Contains(containerCommand(), quoted(proxyLabel+"="+stamped)) {
		t.Errorf("the proxy is run carrying no record of the baseline behind it, so a box already carrying one would never be given the next:\n%s", containerCommand())
	}
	if !strings.Contains(string(containerItem().Content), "baseline="+stamped) {
		t.Errorf("the proxy is surveyed without the baseline it was written under:\n%s", containerItem().Content)
	}
}

func starting(t *testing.T, command string) string {
	t.Helper()

	for _, line := range strings.Split(command, "\n") {
		if strings.HasPrefix(line, quoted("docker")+" "+quoted("run")+" ") {
			return line
		}
	}
	t.Fatalf("nothing in the write stands the proxy up:\n%s", command)
	return ""
}

func TestTheFileTheProxyIsStartedFromIsTheWholeOfWhatItServes(t *testing.T) {
	t.Parallel()

	command := containerCommand()
	run, config, split := strings.Cut(strings.TrimSuffix(starting(t, command), " >/dev/null"), quoted("caddy")+" "+quoted("run")+" ")
	if !split {
		t.Fatalf("nothing in the run command starts caddy:\n%s", command)
	}
	if config != quoted("--config")+" "+quoted(proxyConfigMount) {
		t.Errorf("caddy is started with %q, want nothing beyond --config: --resume is documented to use the last autosaved configuration, overriding --config, so the file every deploy replaces would be read and thrown away",
			config)
	}
	if strings.Contains(run, "--resume") {
		t.Errorf("the proxy is run with --resume:\n%s", command)
	}
	if !strings.Contains(command, quoted(proxyRoot+":"+proxyConfigDir+":ro")) {
		t.Errorf("the proxy is handed something other than %s, the directory ocel writes %s in:\n%s\na deploy replaces that file by staging beside it and renaming, and a bind of the file itself pins the container to the inode it started on, so every flip after the first reloads whatever the box was seeded with",
			proxyRoot, ProxyConfig, command)
	}
	if proxyConfigMount != proxyConfigDir+strings.TrimPrefix(ProxyConfig, proxyRoot) {
		t.Errorf("the proxy is started from %s, which is not where %s lands under the directory it is handed", proxyConfigMount, ProxyConfig)
	}
}

func TestWhatTheDeployLoopWritesOverTheProxysConfigIsNeverCalledDrift(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	deployed := digests(Items(class, keys, ArchAMD64))
	routed := proxyConfigItem()
	routed.Content = []byte("every route this box serves")
	if routed.Digest() != proxyConfigItem().Digest() {
		t.Fatal("the proxy's config digests differently once a deploy has written routes into it, and every deploy would then read as drift")
	}

	read := Reading{Class: class, Keys: keys, Arch: ArchAMD64, Observed: deployed}
	if !read.current(routed) {
		t.Fatal("a box whose deploys have written routes into the proxy's config reads as drifted, and the item is keyed on content the deploy loop is built to replace")
	}
	if planned := planFor(planned(read), proxyConfigItem().ID()); planned.Action != providerkit.ActionKeep {
		t.Errorf("a re-run over a box carrying deployed routes plans %q for %s, and a bootstrap that reseeds it takes every app on the box down", planned.Action, ProxyConfig)
	}
	if sum := proxyConfigItem().sum(); sum != "" {
		t.Errorf("the proxy's config is digested over %q, and every deploy invalidates it", sum)
	}
	if strings.Contains(proxyConfigProbe(proxyConfigItem()), "sha256sum") {
		t.Errorf("the survey hashes what the proxy serves:\n%s", proxyConfigProbe(proxyConfigItem()))
	}
	seeding := proxyConfigCommand(proxyConfigItem())
	if !strings.Contains(seeding, "if [ -f "+quoted(ProxyConfig)+" ]") {
		t.Errorf("the write of %s replaces what stands there rather than seeding what does not:\n%s", ProxyConfig, seeding)
	}
	if !strings.Contains(seeding, "chown "+stateOwner+":"+stateOwner) || !strings.Contains(seeding, "chmod 0640") {
		t.Errorf("the write of %s leaves its mode and owner as it found them:\n%s", ProxyConfig, seeding)
	}
}

func TestOneProxyConfigOfTheDeploysOwnDoesNotRefuseTheHealOfEveryOtherItem(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	items := Items(class, keys, ArchAMD64)
	observed := digests(items)
	observed[proxyConfigItem().ID()] = digest(KindProxyConfig, ProxyConfig, 0o600, rootOwner, "")
	observed[KindDir+" "+RecordsDir(class)] = digest(KindDir, RecordsDir(class), 0o700, stateOwner, "")
	read := Reading{Class: class, Present: true, Keys: keys, Arch: ArchAMD64, Observed: observed,
		Stamp: Stamp{State: StateComplete, Digests: digests(items)}}

	work, left, err := healable(read)
	if err != nil {
		t.Fatalf("heal over a box whose proxy config stands as the deploy left it = %v, want every other item still healed", err)
	}
	if len(work) != 1 || work[0].Name != RecordsDir(class) {
		t.Errorf("healable() = %v, want only the record tier", ids(work))
	}
	if !slices.Contains(left, proxyConfigItem().ID()) {
		t.Errorf("heal left %v, and a box told nothing about the one item it declined to write is one nobody can read the exit code of", left)
	}
	if err := refuseReplacements(read, work); err != nil {
		t.Errorf("an unattended apply over the same box = %v, want the proxy's own config never counted as something a user must consent to overwrite", err)
	}
}

func TestAMissingProxyIsLeftToABootstrapAndSaidSoRatherThanPassedOver(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	items := Items(class, keys, ArchAMD64)
	observed := digests(items)
	for _, gone := range []Item{containerItem(), proxyConfigItem()} {
		delete(observed, gone.ID())
	}
	read := Reading{Class: class, Present: true, Keys: keys, Arch: ArchAMD64, Observed: observed,
		Stamp: Stamp{State: StateComplete, Digests: digests(items)}}

	work, left, err := healable(read)
	if err != nil {
		t.Fatalf("heal over a box whose proxy is gone = %v", err)
	}
	if len(work) != 0 {
		t.Errorf("heal writes %v, and the unattended path with nobody watching does not install a proxy", ids(work))
	}
	for _, said := range []string{containerItem().ID(), proxyConfigItem().ID()} {
		if !slices.Contains(left, said) {
			t.Errorf("heal left %v and never names %s, so a box with no proxy at all exits zero saying nothing", left, said)
		}
	}
}

func TestANetworkAnotherWorkloadHoldsIsReportedKeptRatherThanRemoved(t *testing.T) {
	t.Parallel()

	command := (removal{kind: KindNetwork, path: ProxyNetwork}).command()
	for held, want := range map[string]string{"0": networkHeld, "1": ""} {
		stub := t.TempDir()
		script := "#!/bin/sh\n" +
			"case \"$1 $2\" in\n" +
			"'network rm') exit 1 ;;\n" +
			"'network inspect') exit " + held + " ;;\n" +
			"esac\nexit 1\n"
		if err := os.WriteFile(filepath.Join(stub, dockerEngine), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		run := exec.Command("/bin/sh", "-c", command)
		run.Env = []string{"PATH=" + stub}
		rendered, err := run.Output()
		if err != nil {
			t.Fatalf("taking the network = %v", err)
		}
		if strings.TrimSpace(string(rendered)) != want {
			t.Errorf("a network the engine still holds after the removal answers %q, want %q: a removal that always exits zero reports every kept network as removed",
				strings.TrimSpace(string(rendered)), want)
		}
	}
}

func TestHealNeverWritesOverTheConfigTheProxyIsServingFrom(t *testing.T) {
	t.Parallel()

	for _, item := range ProxyItems(ArchAMD64) {
		if deployOwned(item) {
			t.Errorf("heal may write %s, and the one unattended path with nobody watching would replace the routes every deployed app is reached through", item.ID())
		}
	}
}

func TestAnUnattendedApplyWritesOcelsOwnProxyBackWithoutAsking(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	observed := digests(Items(class, keys, ArchAMD64))
	observed[containerItem().ID()] = digest(KindContainer, ProxyContainer, 0, rootOwner, contentSum([]byte("state=exited\n")))
	observed[networkItem().ID()] = digest(KindNetwork, ProxyNetwork, 0, rootOwner, contentSum([]byte("moved\n")))

	read := Reading{Arch: ArchAMD64, Class: class, Keys: keys, Observed: observed}
	if err := refuseReplacements(read, Items(class, keys, ArchAMD64)); err != nil {
		t.Errorf("an unattended apply over a host whose proxy has moved = %v, want it written back: the proxy carries ocel's own name and nothing of the user's", err)
	}
}

func TestDestroyTakesOcelsProxyAndLeavesEveryContainerTheHostRuns(t *testing.T) {
	t.Parallel()

	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	keys := []byte(aKey + "\n")
	standing := Reading{Arch: ArchAMD64, Class: production, Keys: keys, Observed: digests(Items(production, keys, ArchAMD64))}
	beside := Reading{Arch: ArchAMD64, Class: preview, Keys: keys, Observed: digests(Items(preview, keys, ArchAMD64))}
	proxied := []string{ProxyContainer, ProxyData, ProxyNetwork, proxyRoot, ProxyHelper, ProxyConfig}

	for _, taken := range removing(standing, beside) {
		if slices.Contains(proxied, taken.path) && taken.action == providerkit.ActionDelete {
			t.Errorf("destroying one class takes %s, and the sibling class still standing on this host deploys through it", taken.path)
		}
	}

	last := removing(standing, Reading{Arch: ArchAMD64, Class: preview, Observed: map[string]string{}})
	for _, path := range proxied {
		gone := removalOf(last, path)
		if gone.action != providerkit.ActionDelete {
			t.Errorf("destroying the last class plans %s as %q, want it taken: what ocel wrote is what ocel takes back", path, gone.action)
		}
	}
	if reason := removalOf(last, ProxyContainer).reason; reason == "" {
		t.Error("the proxy is taken with no reason, and the typed confirmation must name what goes before a user types")
	}
	if reason := removalOf(last, ProxyData).reason; reason == "" {
		t.Error("the proxy's data directory is taken with no reason, and every private key and the acme account key it holds go with it")
	}
	container := slices.IndexFunc(last, func(r removal) bool { return r.path == ProxyContainer })
	for _, after := range []string{ProxyData, ProxyNetwork, proxyRoot} {
		if at := slices.IndexFunc(last, func(r removal) bool { return r.path == after }); at < container {
			t.Errorf("%s is taken at %d and the container using it at %d, and nothing takes what a running container holds", after, at, container)
		}
	}
	if kept := removalOf(last, dockerEngine); kept.action != providerkit.ActionKeep {
		t.Errorf("destroying the last class plans the engine as %q, want it kept with every container it runs", kept.action)
	}
}

func TestThePinRootIsPlannedAsReclaimedOnlyIfEmptyRatherThanAsABareDelete(t *testing.T) {
	t.Parallel()

	pins := removalOf(proxyRemovals(), ProxyPins)
	if pins.action != providerkit.ActionDelete {
		t.Fatalf("the pin root is planned as %q, and this test states nothing about the row a destroy prints for it", pins.action)
	}
	if pins.reason == "" {
		t.Fatal("the typed confirmation offers to delete the directory holding an operator's key material and says nothing about what happens to it")
	}
	for _, said := range []string{"empty", "ocel never placed one"} {
		if !strings.Contains(pins.reason, said) {
			t.Errorf("the pin root is offered as %q, which never says %q: what the destroy runs reclaims it only when nothing is pinned under it", pins.reason, said)
		}
	}
}

func TestRemovingTheProxyNamesOcelsOwnContainerAndNeverAsksTheEngineWhatElseItRuns(t *testing.T) {
	t.Parallel()

	for _, taken := range proxyRemovals() {
		command := removal{kind: taken.kind, path: taken.path}.command()
		if taken.kind == KindDir {
			continue
		}
		if !strings.Contains(command, quoted(taken.path)) {
			t.Errorf("%s is taken by %q, want a command naming ocel's own %s and nothing else", taken.path, command, taken.kind)
		}
		for _, sweeping := range []string{"ps", "--all", "-a", "prune", "--filter"} {
			if strings.Contains(command, " "+sweeping) {
				t.Errorf("%s is taken by %q, which asks the engine what else it runs: a destroy reaches one name it wrote", taken.path, command)
			}
		}
	}
	command := (removal{kind: KindNetwork, path: ProxyNetwork}).command()
	if !strings.HasPrefix(command, "if ! docker network rm ") {
		t.Errorf("the network is taken by %q, and a destroy fails on a host still running something attached to it", command)
	}
	if !strings.Contains(command, quoted(networkHeld)) {
		t.Errorf("the network is taken by %q, which says nothing about whether it went", command)
	}
}

func TestNothingButWhatOcelWroteIsEverOcelsToTake(t *testing.T) {
	t.Parallel()

	stood := machine(map[providerkit.Class][]Item{providerkit.ClassProduction: bootstrapped(t, providerkit.ClassProduction)})
	for _, kind := range []string{KindEngine, KindUnit} {
		_, err := stood.host().remove(context.Background(), removal{kind: kind, path: dockerEngine, action: providerkit.ActionDelete})
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

func TestTheHelperIsAStaticBinaryOcelBuildsForEveryArchitectureABoxMayRun(t *testing.T) {
	t.Parallel()

	for reported, want := range map[string]string{
		"x86_64": ArchAMD64, ArchAMD64: ArchAMD64, "aarch64": ArchARM64, ArchARM64: ArchARM64,
	} {
		read, err := Architecture(reported)
		if err != nil || read != want {
			t.Errorf("a host reporting %q is served the %q helper (%v), want %q", reported, read, err, want)
		}
	}
	if _, err := Architecture("riscv64"); err == nil {
		t.Error("a host ocel builds no helper for is bootstrapped anyway, and the file the release loop execs would be for another machine")
	}

	shipped := map[string][]byte{}
	for _, arch := range []string{ArchAMD64, ArchARM64} {
		built := proxyHelper(arch)
		shipped[arch] = built
		if !bytes.HasPrefix(built, []byte("\x7fELF")) {
			t.Fatalf("the %s helper is not an elf executable, and the proxy image lends it nothing to interpret it with", arch)
		}
		if bytes.Contains(built, []byte("libc.so")) || bytes.Contains(built, []byte("ld-linux")) {
			t.Errorf("the %s helper names a dynamic loader, and the image it runs in owes it none", arch)
		}
		if !bytes.Contains(built, []byte(upstreamsPath)) {
			t.Errorf("the %s helper carries no read of %s, and the drain is the one signal telling a retired release apart from a live one",
				arch, upstreamsPath)
		}
		if !bytes.Contains(built, []byte(ProxyAdminSocket)) {
			t.Errorf("the %s helper names no admin socket, so what it speaks over is whatever the caller happens to pass", arch)
		}
	}
	if bytes.Equal(shipped[ArchAMD64], shipped[ArchARM64]) {
		t.Error("both architectures are shipped the same bytes, so one of the two boxes runs a binary built for the other")
	}
	if bytes.Equal(ProxyItems(ArchAMD64)[0].Content, ProxyItems(ArchARM64)[0].Content) {
		t.Error("the helper item carries the same bytes whatever the box reports, and the architecture is then not what selects it")
	}
}

func TestTheHelperCarriesNoDependenceOnWhatTheProxyImageHappensToShip(t *testing.T) {
	t.Parallel()

	command := containerCommand()
	for _, borrowed := range []string{"curl", "wget", "busybox"} {
		if strings.Contains(command, borrowed) {
			t.Errorf("the proxy is run naming %q, and a gate whose precondition is that the image happens to carry a program is not a contract:\n%s", borrowed, command)
		}
	}
	if got := proxyItem(KindFile).Content; !bytes.Equal(got, proxyHelper(ArchAMD64)) && !bytes.Equal(got, proxyHelper(ArchARM64)) {
		t.Error("the file bootstrap ships to the box is not one of the helpers ocel builds")
	}
}

func TestABoxOcelBuildsNoHelperForIsStillABoxOcelCanDestroy(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})
	stood.facts.Arch = "riscv64"

	if _, err := Bootstrap(stood.host()).PlanRemoval(context.Background(), class); err != nil {
		t.Fatalf("PlanRemoval() over a host ocel builds no flip helper for = %v, want what ocel wrote still taken back: the paths it wrote are the same whatever the box runs", err)
	}
	if _, err := stood.host().Read(context.Background(), class); err == nil {
		t.Error("a host reporting an architecture ocel builds no helper for is bootstrapped anyway, and the file the release loop execs would be for another machine")
	}
}

func TestWhatTheDeployLoginHoldsIsTheSameWhicheverArchitectureTheBoxRuns(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	if !slices.Equal(grants(class, ArchAMD64), grants(class, ArchARM64)) {
		t.Error("`ocel permissions deploy` prints one thing on an amd64 box and another on an arm64 one, and what a login holds does not depend on what the box runs")
	}
}

func engineStub(t *testing.T, upAt int) string {
	t.Helper()

	dir := t.TempDir()
	asks := quoted(filepath.Join(dir, "asked"))
	stub := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"rm|run) exit 0 ;;\n" +
		"logs) echo 'run: loading initial config' ; echo 'caddy said why it stopped' ; exit 0 ;;\n" +
		"inspect)\n" +
		"  echo call >> " + asks + "\n" +
		"  case \"$*\" in *ExitCode*) echo 'status=exited exit=7 error=nothing caddy could parse' ; exit 0 ;; esac\n" +
		"  if [ " + fmt.Sprint(upAt) + " -gt 0 ] && [ \"$(wc -l < " + asks + ")\" -ge " + fmt.Sprint(upAt) + " ]; then\n" +
		"    echo running\n  else\n    echo created\n  fi\n" +
		"  ;;\n" +
		"esac\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, dockerEngine), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func boundFiles(t *testing.T) []string {
	t.Helper()

	dir := t.TempDir()
	var files []string
	for _, name := range []string{"caddy.json", proxyHelperName} {
		at := filepath.Join(dir, name)
		if err := os.WriteFile(at, []byte("what the proxy is started against"), 0o640); err != nil {
			t.Fatal(err)
		}
		files = append(files, at)
	}
	return files
}

func asked(t *testing.T, dir string) int {
	t.Helper()

	rendered, err := os.ReadFile(filepath.Join(dir, "asked"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(rendered), "\n")
}

func writing(t *testing.T, dir, command string) (string, error) {
	t.Helper()

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

func TestTheContainerWriteWaitsForTheProxyItJustCreatedToComeUp(t *testing.T) {
	t.Parallel()

	dir := engineStub(t, 2)
	said, err := writing(t, dir, containerWriting(5, boundFiles(t)))
	if err != nil {
		t.Fatalf("the write of a proxy that reported created before it reported running = %v\n%s", err, said)
	}
	if at := asked(t, dir); at < 2 {
		t.Errorf("the write asked the engine %d times whether the proxy was up, and a container still reported created is one the write returned on rather than waited for", at)
	}
}

func TestAProxyThatNeverComesUpFailsTheWriteWithWhatTheEngineSaysAboutIt(t *testing.T) {
	t.Parallel()

	dir := engineStub(t, 0)
	said, err := writing(t, dir, containerWriting(2, boundFiles(t)))
	if err == nil {
		t.Fatalf("the write of a proxy that never came up landed, and the stamp then records a state the box does not hold:\n%s", said)
	}
	for _, evidence := range []string{ProxyContainer, "status=exited", "exit=7", "caddy said why it stopped"} {
		if !strings.Contains(said, evidence) {
			t.Errorf("the write said %q, and it never names %q: a proxy that crash-loops must diagnose itself here rather than surface as drift", said, evidence)
		}
	}
	if lines := strings.Count(strings.TrimRight(said, "\n"), "\n") + 1; lines > saidLines {
		t.Errorf("the write said %d lines and a refusal carries %d, so the evidence is cut before it is read:\n%s", lines, saidLines, said)
	}
}

func TestTheProxyIsWrittenAgainstTheBoxTheEngineWriteLeftBehind(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := settledOn(t, class)
	for at, item := range stood.stands[class] {
		if item.Kind == KindEngine {
			stood.stands[class][at].Content = []byte("engine=" + unservedFact + "\n")
		}
	}
	stood.after = func(b *bench, command string) {
		if !strings.Contains(command, dockerSource) {
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		held := b.stands[class]
		for at, item := range held {
			if item.Kind == KindContainer {
				held[at].Content = []byte("state=exited\n")
			}
		}
	}

	report := &said{}
	if err := Bootstrap(stood.host()).Apply(context.Background(),
		providerkit.BootstrapRequest{Class: class, Writer: "the-suite"}, report); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if at := report.at("wrote " + KindContainer + " " + ProxyContainer); at < 0 {
		t.Errorf("the apply installed the engine, the proxy went down under it, and the apply still called the container current:\n%s",
			strings.Join(report.lines, "\n"))
	}
}

func TestSomethingOtherThanTheProxysConfigStandingAtItsPathIsRefusedRatherThanChowned(t *testing.T) {
	t.Parallel()

	at := filepath.Join(t.TempDir(), "caddy.json")
	if err := os.Mkdir(at, 0o755); err != nil {
		t.Fatal(err)
	}
	item := proxyConfigItem()
	item.Name = at

	said, err := writing(t, t.TempDir(), proxyConfigCommand(item))
	if err == nil {
		t.Fatalf("the write over a directory where the config belongs landed, and the probe reads that path with -f: "+
			"the write would call it present forever, the survey would call it absent forever, and every apply would report success over a proxy that never serves:\n%s", said)
	}
	if !strings.Contains(said, at) {
		t.Errorf("the write said %q, want it to name %s as what stands there", said, at)
	}
	held, err := os.Stat(at)
	if err != nil {
		t.Fatal(err)
	}
	if held.Mode().Perm() != 0o755 {
		t.Errorf("the write left %s at %v, and it chmodded what it could not write rather than refusing over it", at, held.Mode().Perm())
	}
}

func TestTheProxyIsNeverStartedAgainstABindSourceDockerWouldInvent(t *testing.T) {
	t.Parallel()

	gone := filepath.Join(t.TempDir(), "caddy.json")
	dir := engineStub(t, 1)
	said, err := writing(t, dir, containerWriting(2, []string{gone}))
	if err == nil {
		t.Fatalf("the proxy was started against a bind source that does not stand, and docker answers a missing source by "+
			"creating a root-owned directory there, which caddy cannot read and no later apply repairs:\n%s", said)
	}
	if !strings.Contains(said, gone) {
		t.Errorf("the write said %q, want it to name %s", said, gone)
	}
	if at := asked(t, dir); at != 0 {
		t.Errorf("the write reached the engine %d times over a bind source that does not stand, want it refused before docker is asked for anything", at)
	}
}

func TestAProxyStandingAsWrittenButNotRunningIsPlannedBackAndNeverCalledSettled(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	items := Items(class, keys, ArchAMD64)
	minted := []byte("the key this box minted for itself")

	for _, between := range []string{"created", "restarting", "exited", "paused"} {
		observed := digests(items)
		idle := bytes.Replace(containerItem().Content, []byte("state=running"), []byte("state="+between), 1)
		if bytes.Equal(idle, containerItem().Content) {
			t.Fatalf("the container is surveyed without a state at all, so what %q proves here is nothing", between)
		}
		observed[containerItem().ID()] = digest(KindContainer, ProxyContainer, 0, rootOwner, contentSum(idle))

		read := Reading{
			Class: class, Present: true, Keys: keys, Arch: ArchAMD64, Observed: observed,
			Seal: Seal{Fingerprint: contentSum(minted)},
			Stamp: Stamp{
				Schema: providerkit.BootstrapSchema, State: StateComplete,
				Seal: Seal{Fingerprint: contentSum(minted)}, Digests: digests(items),
			},
		}

		if back := planFor(planned(read), containerItem().ID()); back.Action != providerkit.ActionUpdate {
			t.Errorf("a proxy whose every configuration fact is as ocel wrote it and whose state is %q plans %q, want it written back: a proxy nothing notices is one nothing repairs",
				between, back.Action)
		}
		if read.settled() {
			t.Errorf("a box whose proxy is %q reports itself settled, and Describe would call it current while a re-run plans an update over it",
				between)
		}
	}
}

func TestTheProxysFactsReadTheSameWhateverOrderTheEngineReportsItsMountsIn(t *testing.T) {
	t.Parallel()

	binds := proxyBinds()
	if len(binds) < 3 {
		t.Fatalf("the proxy carries %d mounts, and nothing about ordering can be proven over fewer than two", len(binds))
	}
	stated := proxyFacts()
	for _, order := range orderings(len(binds)) {
		shuffled := make([]string, 0, len(binds))
		for _, at := range order {
			shuffled = append(shuffled, binds[at])
		}
		if got := proxyFactsOver(shuffled); !bytes.Equal(got, stated) {
			t.Errorf("the engine reporting ocel's own mounts in the order %v reads as a proxy that drifted:\n%s\nwant\n%s",
				order, got, stated)
		}
	}

	for _, missing := range []([]string){binds[:2], append(slices.Clone(binds), "/somewhere/else:/etc/caddy/ocel.json:ro")} {
		if bytes.Equal(proxyFactsOver(missing), stated) {
			t.Errorf("a proxy carrying the mounts %v reads as one carrying %v, and a proxy someone re-ran with different mounts is drift ocel must catch",
				missing, binds)
		}
	}
}

func orderings(count int) [][]int {
	var found [][]int
	var walk func(taken, rest []int)
	walk = func(taken, rest []int) {
		if len(rest) == 0 {
			found = append(found, slices.Clone(taken))
			return
		}
		for at := range rest {
			walk(append(taken, rest[at]), slices.Concat(rest[:at], rest[at+1:]))
		}
	}
	all := make([]int, count)
	for at := range all {
		all[at] = at
	}
	walk(nil, all)
	return found
}

func TestEveryFactTheProbeReadsIsOneTheItemStates(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"image=", "restart=", "networks=", "bind=", "ports=", "baseline=", "state="} {
		if !strings.Contains(ProxyFactTemplate, key) {
			t.Errorf("the probe reads no %q off the box, and the item states one", key)
		}
		if !strings.Contains(string(containerItem().Content), key) {
			t.Errorf("the item states no %q, and the probe reads one off the box", key)
		}
	}
	if strings.Contains(ProxyFactTemplate, "json .HostConfig.Binds") {
		t.Error("the mounts are read off the box as one json array, and the engine does not promise the order of that array")
	}
	if !strings.Contains(containerProbe(), "LC_ALL=C sort") {
		t.Errorf("the probe hashes what the box says in the order the box happens to say it:\n%s", containerProbe())
	}
}
