package host

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	KindNetwork     = "docker:network"
	KindContainer   = "docker:container"
	KindProxyConfig = "ocel:proxy-config"
)

const ProxyImage = "docker.io/library/caddy@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d"

const (
	ProxyNetwork   = "ocel"
	ProxyContainer = "ocel-proxy"
	proxyRestart   = "unless-stopped"
	proxyPort      = "80"
	proxyTLSPort   = "443"
	proxyLabel     = "ocel.config"
)

const (
	ProxyHelper = helperRoot + "/" + proxyHelperName
	proxyRoot   = stateRoot + "/proxy"
	ProxyConfig = proxyRoot + "/" + proxyConfigName
	ProxyData   = proxyRoot + "/data"
	ProxyPins   = classRoot + "/certs"
)

const (
	proxyHelperName  = "ocel-proxyctl"
	proxyConfigName  = "caddy.json"
	proxyConfigDir   = "/etc/caddy/ocel"
	proxyConfigMount = proxyConfigDir + "/" + proxyConfigName
	ProxyHelperMount = "/ocel/" + proxyHelperName
	proxyDataMount   = "/data"
	proxyPinsMount   = "/etc/caddy/pins"
	ProxyAdminSocket = "/run/caddy-admin.sock"
)

const (
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

const proxyRising = 30

const (
	networkFact = "network=present"
	networkHeld = "network=held"
)

//go:generate sh ./generate.sh

//go:embed proxy.json
var proxyBaseline []byte

//go:embed dist
var proxyHelpers embed.FS

const ProxyFactTemplate = `image={{.Config.Image}}
restart={{.HostConfig.RestartPolicy.Name}}
networks={{range $n, $v := .NetworkSettings.Networks}}{{$n}} {{end}}
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
			"this host reports its architecture as %q, and ocel builds the proxy's flip helper for %s and %s alone",
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
			Note: "gates a target, flips the proxy and reads what is still in flight"},
		dir(proxyRoot, 0o750, stateOwner, "what the proxy serves"),
		dir(ProxyPins, 0o755, rootOwner, "the one directory a certificate pair pinned by an operator is loaded from, which a destroy leaves where it found it"),
		proxyConfigItem(),
		dir(ProxyData, 0o700, rootOwner, "the proxy's certificates, their private keys and the acme account key that issues for every one of them"),
		networkItem(),
		containerItem(),
	}
}

func proxyConfigItem() Item {
	return Item{
		Kind:    KindProxyConfig,
		Name:    ProxyConfig,
		Mode:    0o640,
		Owner:   stateOwner,
		Content: proxyBaseline,
		Note:    "the proxy's whole configuration, seeded once here and rewritten by every deploy",
	}
}

func proxyConfigCommand(item Item) string {
	at := quoted(item.Name)
	seed := fmt.Sprintf("install -m %04o -o %s -g %s /dev/stdin %s", item.Mode, item.Owner, item.Owner, at)
	return "set -e\n" +
		"if [ -f " + at + " ]; then cat >/dev/null\n" +
		"elif [ -e " + at + " ]; then cat >/dev/null; " + notAFile(item.Name) + "\n" +
		"else " + seed + "; fi\n" +
		fmt.Sprintf("chown %s:%s %s\n", item.Owner, item.Owner, at) +
		fmt.Sprintf("chmod %04o %s", item.Mode, at)
}

func notAFile(name string) string {
	return "printf '%s\\n' " + quoted(name+" stands as something other than a regular file, and the proxy reads"+
		" the whole of what it serves from it. Take whatever is there and re-run") + " >&2; exit 1"
}

func proxyFiles() []string { return []string{ProxyConfig, ProxyHelper} }

func bindsStanding(files []string) string {
	var written string
	for _, name := range files {
		written += "if [ ! -f " + quoted(name) + " ]; then " + notAFile(name) + "; fi\n"
	}
	return written
}

func proxyConfigProbe(item Item) string {
	at := quoted(item.Name)
	return "if [ -h " + at + " ]; then " +
		reports(quoted(kindLink), at, "0", "''", `"$(readlink `+at+`)"`) + "\n" +
		"elif [ -f " + at + " ]; then " +
		reports(quoted(KindProxyConfig), at, `"$(stat -c %a `+at+`)"`, `"$(stat -c %U `+at+`)"`, "''") + "\nfi"
}

func networkItem() Item {
	return Item{
		Kind:    KindNetwork,
		Name:    ProxyNetwork,
		Owner:   rootOwner,
		Content: []byte(networkFact + "\n"),
		Note:    "the one network every deploy target resolves across",
	}
}

func containerItem() Item {
	return Item{
		Kind:    KindContainer,
		Name:    ProxyContainer,
		Owner:   rootOwner,
		Content: proxyFacts(),
		Slow:    true,
		Note: "the proxy every request to ports " + proxyPort + " and " + proxyTLSPort +
			" on this host reaches, pulled as " + ProxyImage,
	}
}

func proxyFacts() []byte { return proxyFactsOver(proxyBinds()) }

func proxyFactsOver(binds []string) []byte {
	stated := []string{
		"image=" + ProxyImage,
		"restart=" + proxyRestart,
		"networks=" + ProxyNetwork + " ",
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

func containerWriting(attempts int, files []string) string {
	argv := []string{"docker", "run", "--detach",
		"--name", ProxyContainer,
		"--restart", proxyRestart,
		"--network", ProxyNetwork,
		"--label", proxyLabel + "=" + contentSum(proxyBaseline),
		"--env", "XDG_CONFIG_HOME=" + proxyDataMount + "/config",
	}
	for _, port := range proxyServing() {
		argv = append(argv, "--publish", port+":"+port)
	}
	for _, bind := range proxyBinds() {
		argv = append(argv, "--volume", bind)
	}
	argv = append(argv, ProxyImage, "caddy", "run", "--config", proxyConfigMount)
	return "set -e\n" +
		bindsStanding(files) +
		"docker rm --force " + quoted(ProxyContainer) + " >/dev/null 2>&1 || true\n" +
		words(argv) + " >/dev/null\n" +
		containerRising(attempts)
}

func containerRising(attempts int) string {
	name := quoted(ProxyContainer)
	inspect := "docker inspect --type container --format "
	return "at=0\n" +
		"while :; do\n" +
		"if [ \"$(" + inspect + quoted("{{.State.Status}}") + " " + name + " 2>/dev/null)\" = running ]; then exit 0; fi\n" +
		"at=$((at + 1))\n" +
		"[ \"$at\" -lt " + fmt.Sprint(attempts) + " ] || break\n" +
		"sleep 1\n" +
		"done\n" +
		"printf '%s\\n' " + quoted(fmt.Sprintf(
		"%s was created and did not report running within %ds", ProxyContainer, attempts)) + " >&2\n" +
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
		taking(KindContainer, ProxyContainer, "the proxy ocel runs, and nothing else this engine carries"),
		taking(KindDir, ProxyData, "the proxy's certificates, their private keys and the acme account key that issues for every hostname on it, which no other machine holds"),
		taking(KindNetwork, ProxyNetwork,
			"the network ocel's deploys resolve across, which stays as long as anything this host runs is still attached to it"),
		taking(KindProxyConfig, ProxyConfig,
			"the routes every app deployed onto this host is reached through, which no deploy renders again"),
		taking(KindDir, proxyRoot, "what the proxy serves"),
		sharing(ProxyPins, "the one directory a certificate pair pinned by an operator is loaded from, reclaimed only if it is empty: any pair you placed there stays, because ocel never placed one"),
	}
}
