package host

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	KindNetwork      = "docker:network"
	KindContainer    = "docker:container"
	KindProxyConfig  = "ocel:proxy-config"
	KindRoutingTable = "ocel:routing-table"
)

const ProxyImage = "public.ecr.aws/docker/library/caddy@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d"

const (
	ProxyNetwork      = "ocel"
	ProxyContainer    = "ocel-proxy"
	proxyRestart      = "unless-stopped"
	proxyPort         = "80"
	proxyTLSPort      = "443"
	proxyLabel        = "ocel.config"
	proxyCommandLabel = "ocel.command"
)

const (
	ProxyHelper = helperRoot + "/" + proxyHelperName
	proxyRoot   = live.ProxyDir
	ProxyConfig = live.ProxyConfig
	ProxyData   = proxyRoot + "/data"
	ProxyPins   = classRoot + "/certs"
)

const (
	proxyHelperName  = "ocel-proxyctl"
	proxyConfigName  = "caddy.json"
	proxyConfigDir   = "/etc/caddy/ocel"
	ProxyConfigMount = proxyConfigDir + "/" + proxyConfigName
	ProxyHelperMount = "/ocel/" + proxyHelperName
	proxyDataMount   = "/data"
	proxyPinsMount   = "/etc/caddy/pins"
	ProxyAdminSocket = "/run/caddy-admin.sock"
)

const (
	AdminPort       = "2019"
	AdminPortNumber = 2019
	RenewalPort     = proxyPort
)

const (
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

const (
	proxyRising = 30
	proxyPulls  = 5
)

var proxyCapabilities = []string{"NET_BIND_SERVICE", "DAC_OVERRIDE", "DAC_READ_SEARCH"}

const (
	networkFact   = "network=present"
	networkHeld   = "network=held"
	networkJoined = "joined"
	networkLeft   = "left"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/vps-host

//go:embed proxy.json
var proxyBaseline []byte

//go:embed dist
var proxyHelpers embed.FS

const ProxyFactTemplate = `image={{.Config.Image}}
command={{index .Config.Labels "` + proxyCommandLabel + `"}}
restart={{.HostConfig.RestartPolicy.Name}}
network={{if index .NetworkSettings.Networks "` + ProxyNetwork + `"}}` + networkJoined + `{{else}}` + networkLeft + `{{end}}
{{range .HostConfig.Binds}}bind={{.}}
{{end}}ports={{json .HostConfig.PortBindings}}
baseline={{index .Config.Labels "` + proxyLabel + `"}}
state={{.State.Status}}`

func Architecture(reported string) (string, error) {
	switch reported {
	case "x86_64", ArchAMD64:
		return ArchAMD64, nil
	case "aarch64", ArchARM64:
		return ArchARM64, nil
	default:
		return "", providerkit.Refuse(providerkit.CodeDenied,
			"this host's architecture %q is not %s or %s",
			reported, ArchAMD64, ArchARM64)
	}
}

func proxyHelper(arch string) []byte {
	read, err := proxyHelpers.ReadFile("dist/" + proxyHelperName + "-" + arch)
	if err != nil {
		panic(err)
	}
	return read
}

func ProxyItems(arch string) []Item {
	return []Item{
		{Kind: KindFile, Name: ProxyHelper, Mode: 0o750, Owner: rootOwner, Content: proxyHelper(arch),
			Note: "proxy control"},
		dir(proxyRoot, 0o750, stateOwner, ""),
		dir(ProxyPins, 0o700, rootOwner, "your pinned certificates"),
		proxyConfigItem(),
		routingTableItem(),
		dir(ProxyData, 0o700, rootOwner, "certificates and acme key"),
		networkItem(),
		containerItem(),
	}
}

var seededTable = RoutingTable{Grace: DrainWindow}

var seededRows, seededRendering = seeded(seededTable)

func seeded(table RoutingTable) ([]byte, []byte) {
	rows, unwritten := WriteRoutingTable(table)
	rendered, unrendered := RenderProxyConfig(table)
	if err := errors.Join(unwritten, unrendered); err != nil {
		panic(err)
	}
	return rows, rendered
}

func proxyConfigItem() Item {
	return Item{
		Kind:    KindProxyConfig,
		Name:    ProxyConfig,
		Mode:    0o640,
		Owner:   stateOwner,
		Content: seededRendering,
		Note:    "rendered from the routing table",
	}
}

func routingTableItem() Item {
	return Item{
		Kind:    KindRoutingTable,
		Name:    live.RoutingTable,
		Mode:    0o640,
		Owner:   stateOwner,
		Content: seededRows,
		Note:    "seeded here, rewritten by deploys",
	}
}

func rewrittenByDeploys(item Item) bool {
	return item.Kind == KindProxyConfig || item.Kind == KindRoutingTable
}

func seedingRouting(table, config Item) string {
	var script strings.Builder
	script.WriteString("set -e\n")
	for _, item := range []Item{table, config} {
		at := quoted(item.Name)
		script.WriteString("if [ -e " + at + " ] && [ ! -f " + at + " ]; then " + notAFile(item.Name) + "; fi\n")
	}
	install := func(item Item) string {
		return fmt.Sprintf("printf '%%s' %s | install -m %04o -o %s -g %s /dev/stdin %s\n",
			quoted(string(item.Content)), item.Mode, item.Owner, item.Owner, quoted(item.Name))
	}
	script.WriteString("if [ ! -f " + quoted(table.Name) + " ]; then\n" + install(table) + install(config) +
		"elif [ ! -f " + quoted(config.Name) + " ]; then\n" + install(config) + "fi\n")
	for _, item := range []Item{table, config} {
		fmt.Fprintf(&script, "chown %s:%s %s\nchmod %04o %s\n", item.Owner, item.Owner, quoted(item.Name), item.Mode, quoted(item.Name))
	}
	return strings.TrimSuffix(script.String(), "\n")
}

func notAFile(name string) string {
	return "printf '%s\\n' " + quoted(name+" is not a regular file") + " >&2; exit 1"
}

func proxyFiles() []string { return []string{ProxyConfig, ProxyHelper} }

func bindsStanding(files []string) string {
	var written string
	for _, name := range files {
		written += "if [ ! -f " + quoted(name) + " ]; then " + notAFile(name) + "; fi\n"
	}
	return written
}

func seededProbe(item Item) string {
	at := quoted(item.Name)
	return "if [ -h " + at + " ]; then " +
		reports(quoted(kindLink), at, "0", "''", `"$(readlink `+at+`)"`) + "\n" +
		"elif [ -f " + at + " ]; then " +
		reports(quoted(item.Kind), at, `"$(stat -c %a `+at+`)"`, `"$(stat -c %U `+at+`)"`, "''") + "\nfi"
}

func networkItem() Item {
	return Item{
		Kind:    KindNetwork,
		Name:    ProxyNetwork,
		Owner:   rootOwner,
		Content: []byte(networkFact + "\n"),
	}
}

func containerItem() Item {
	return Item{
		Kind:    KindContainer,
		Name:    ProxyContainer,
		Owner:   rootOwner,
		Content: proxyFacts(),
		Slow:    true,
		Note:    "serves :" + proxyPort + " and :" + proxyTLSPort,
	}
}

func proxyFacts() []byte { return proxyFactsOver(proxyBinds()) }

func proxyFactsOver(binds []string) []byte {
	stated := []string{
		"image=" + ProxyImage,
		"command=" + strings.Join(proxyCommand(), " "),
		"restart=" + proxyRestart,
		"network=" + networkJoined,
		"ports=" + marshalled(proxyPorts()),
		"baseline=" + contentSum(proxyBaseline),
		"state=running",
	}
	for _, bind := range binds {
		stated = append(stated, "bind="+bind)
	}
	slices.Sort(stated)
	return []byte(strings.Join(stated, "\n") + "\n")
}

func proxyBinds() []string {
	return []string{
		proxyRoot + ":" + proxyConfigDir + ":ro",
		ProxyHelper + ":" + ProxyHelperMount + ":ro",
		ProxyPins + ":" + proxyPinsMount + ":ro",
		ProxyData + ":" + proxyDataMount,
		ConnectorRun + ":" + ConnectorRun + ":ro",
	}
}

func proxyPorts() map[string][]map[string]string {
	published := map[string][]map[string]string{}
	for _, port := range proxyServing() {
		published[port+"/tcp"] = []map[string]string{{"HostIp": "", "HostPort": port}}
	}
	return published
}

func proxyServing() []string { return []string{proxyPort, proxyTLSPort} }

func ProxyServing() []string { return proxyServing() }

func marshalled(value any) string {
	written, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(written)
}

func networkCommand() string {
	return "docker network inspect " + quoted(ProxyNetwork) + " >/dev/null 2>&1 || " +
		"docker network create " + quoted(ProxyNetwork) + " >/dev/null"
}

func containerCommand() string { return containerWriting(proxyRising, proxyFiles()) }

func proxyRun() []string {
	argv := []string{"docker", "run", "--detach",
		"--name", ProxyContainer,
		"--restart", proxyRestart,
		"--network", ProxyNetwork,
		"--label", proxyLabel + "=" + contentSum(proxyBaseline),
		"--label", proxyCommandLabel + "=" + strings.Join(proxyCommand(), " "),
		"--env", "XDG_CONFIG_HOME=" + proxyDataMount + "/config",
	}
	argv = append(argv, logging()...)
	argv = append(argv, confined(proxyCapabilities, true)...)
	for _, port := range proxyServing() {
		argv = append(argv, "--publish", port+":"+port)
	}
	for _, bind := range proxyBinds() {
		argv = append(argv, "--volume", bind)
	}
	return append(append(argv, ProxyImage), proxyCommand()...)
}

func proxyCommand() []string { return []string{ProxyHelperMount, "serve", ProxyConfigMount} }

func containerWriting(attempts int, files []string) string {
	argv := proxyRun()
	return "set -e\n" +
		bindsStanding(files) +
		imageHeld(ProxyImage, proxyPulls) +
		"docker rm --force " + quoted(ProxyContainer) + " >/dev/null 2>&1 || true\n" +
		words(argv) + " >/dev/null\n" +
		proxyRejoining() +
		containerRising(attempts)
}

func proxyRejoining() string {
	return "for net in $(docker network ls --quiet --filter " + quoted("label="+LabelClass) + "); do\n" +
		"docker network connect \"$net\" " + quoted(ProxyContainer) + " >/dev/null\n" +
		"done\n"
}

func imageHeld(coordinate string, attempts int) string {
	image := quoted(coordinate)
	return "at=0\n" + pullHold.start() +
		"until docker image inspect " + image + " >/dev/null 2>&1 || docker pull " + image + " >/dev/null; do\n" +
		"at=$((at + 1))\n" +
		"if [ \"$at\" -ge " + fmt.Sprint(attempts) + " ]; then\n" +
		"printf '%s\\n' " + quoted(fmt.Sprintf("%s was not pulled in %d attempts", coordinate, attempts)) + " >&2\n" +
		"exit 1\n" +
		"fi\n" +
		pullHold.again() +
		"done\n"
}

func containerRising(attempts int) string {
	name := quoted(ProxyContainer)
	inspect := "docker inspect --type container --format "
	answering := "docker exec " + name + " " + ProxyHelperMount + " config / >/dev/null 2>&1"
	return "at=0\n" +
		"while :; do\n" +
		"if [ \"$(" + inspect + quoted("{{.State.Status}}") + " " + name + " 2>/dev/null)\" = running ] && " + answering + "; then exit 0; fi\n" +
		"at=$((at + 1))\n" +
		"[ \"$at\" -lt " + fmt.Sprint(attempts) + " ] || break\n" +
		"sleep 1\n" +
		"done\n" +
		"printf '%s\\n' " + quoted(fmt.Sprintf(
		"%s was created and did not answer over its admin socket within %ds", ProxyContainer, attempts)) + " >&2\n" +
		inspect + quoted("status={{.State.Status}} exit={{.State.ExitCode}} error={{.State.Error}}") +
		" " + name + " >&2 2>&1 || true\n" +
		"docker logs --tail 2 " + name + " >&2 2>&1 || true\n" +
		"exit 1"
}

func standingProbe(kind, name, ask, fact string) string {
	return "if command -v " + quoted(dockerEngine) + " >/dev/null 2>&1 && " + ask + "; then\n" +
		reports(quoted(kind), quoted(name), "0", quoted(rootOwner),
			`"$(printf '%s\n' `+quoted(fact)+` | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func networkProbe() string {
	return standingProbe(KindNetwork, ProxyNetwork,
		"docker network inspect "+quoted(ProxyNetwork)+" >/dev/null 2>&1", networkFact)
}

func containerProbe() string {
	return "if command -v " + quoted(dockerEngine) + " >/dev/null 2>&1 && " +
		"facts=$(docker inspect --type container --format " + quoted(ProxyFactTemplate) + " " + quoted(ProxyContainer) + " 2>/dev/null); then\n" +
		reports(quoted(KindContainer), quoted(ProxyContainer), "0", quoted(rootOwner),
			`"$(printf '%s\n' "$facts" | LC_ALL=C sort | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func proxyRemovals() []removal {
	return []removal{
		taking(KindContainer, ProxyContainer, "ocel's proxy"),
		taking(KindDir, ProxyData, "certificates and acme key"),
		taking(KindNetwork, ProxyNetwork, "kept while anything is attached"),
		taking(KindProxyConfig, ProxyConfig, ""),
		taking(KindRoutingTable, live.RoutingTable, "every app's routes"),
		taking(KindDir, proxyRoot, ""),
		sharing(ProxyPins, "only if empty"),
	}
}
