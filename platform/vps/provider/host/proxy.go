package host

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	KindNetwork     = "docker:network"
	KindVolume      = "docker:volume"
	KindContainer   = "docker:container"
	KindProxyConfig = "ocel:proxy-config"
)

const ProxyImage = "docker.io/library/caddy@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d"

const (
	ProxyNetwork   = "ocel"
	ProxyVolume    = "ocel-proxy-data"
	ProxyContainer = "ocel-proxy"
	proxyRestart   = "unless-stopped"
	proxyPort      = "80"
	proxyLabel     = "ocel.config"
)

const (
	ProxyHelper = helperRoot + "/" + proxyHelperName
	proxyRoot   = stateRoot + "/proxy"
	ProxyConfig = proxyRoot + "/caddy.json"
)

const (
	proxyHelperName  = "ocel-proxyctl"
	proxyConfigMount = "/etc/caddy/ocel.json"
	ProxyHelperMount = "/ocel/" + proxyHelperName
	proxyDataMount   = "/data"
	ProxyAdminSocket = "/run/caddy-admin.sock"
)

const (
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

const proxyRising = 30

const (
	networkFact = "network=present"
	volumeFact  = "volume=present"
	networkHeld = "network=held"
)

//go:generate sh ./generate.sh

//go:embed proxy.json
var proxyBaseline []byte

//go:embed dist
var proxyHelpers embed.FS

const proxyFactTemplate = `image={{.Config.Image}}
restart={{.HostConfig.RestartPolicy.Name}}
networks={{range $n, $v := .NetworkSettings.Networks}}{{$n}} {{end}}
binds={{json .HostConfig.Binds}}
ports={{json .HostConfig.PortBindings}}
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
		proxyConfigItem(),
		networkItem(),
		volumeItem(),
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
	seed := fmt.Sprintf("install -m %04o -o %s -g %s /dev/stdin %s", item.Mode, item.Owner, item.Owner, quoted(item.Name))
	return "set -e\n" +
		"if [ -e " + quoted(item.Name) + " ]; then cat >/dev/null; else " + seed + "; fi\n" +
		fmt.Sprintf("chown %s:%s %s\n", item.Owner, item.Owner, quoted(item.Name)) +
		fmt.Sprintf("chmod %04o %s", item.Mode, quoted(item.Name))
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

func volumeItem() Item {
	return Item{
		Kind:    KindVolume,
		Name:    ProxyVolume,
		Owner:   rootOwner,
		Content: []byte(volumeFact + "\n"),
		Note:    "holds the proxy's autosaved config and the certificates it will one day issue, and is reachable from nowhere but the proxy",
	}
}

func containerItem() Item {
	return Item{
		Kind:    KindContainer,
		Name:    ProxyContainer,
		Owner:   rootOwner,
		Content: proxyFacts(),
		Slow:    true,
		Note:    "the proxy every request to port " + proxyPort + " on this host reaches, pulled as " + ProxyImage,
	}
}

func proxyFacts() []byte {
	return fmt.Appendf(nil, "image=%s\nrestart=%s\nnetworks=%s \nbinds=%s\nports=%s\nbaseline=%s\nstate=running\n",
		ProxyImage, proxyRestart, ProxyNetwork, marshalled(proxyBinds()), marshalled(proxyPorts()), contentSum(proxyBaseline))
}

func proxyBinds() []string {
	return []string{
		ProxyConfig + ":" + proxyConfigMount + ":ro",
		ProxyHelper + ":" + ProxyHelperMount + ":ro",
		ProxyVolume + ":" + proxyDataMount,
	}
}

func proxyPorts() map[string][]map[string]string {
	return map[string][]map[string]string{
		proxyPort + "/tcp": {{"HostIp": "", "HostPort": proxyPort}},
	}
}

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

func volumeCommand() string {
	return "docker volume inspect " + quoted(ProxyVolume) + " >/dev/null 2>&1 || " +
		"docker volume create " + quoted(ProxyVolume) + " >/dev/null"
}

func containerCommand() string { return containerWriting(proxyRising) }

func containerWriting(attempts int) string {
	argv := []string{"docker", "run", "--detach",
		"--name", ProxyContainer,
		"--restart", proxyRestart,
		"--network", ProxyNetwork,
		"--label", proxyLabel + "=" + contentSum(proxyBaseline),
		"--env", "XDG_CONFIG_HOME=" + proxyDataMount + "/config",
		"--publish", proxyPort + ":" + proxyPort,
	}
	for _, bind := range proxyBinds() {
		argv = append(argv, "--volume", bind)
	}
	argv = append(argv, ProxyImage, "caddy", "run", "--config", proxyConfigMount)
	return "set -e\n" +
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

func volumeProbe() string {
	return standingProbe(KindVolume, ProxyVolume,
		"docker volume inspect "+quoted(ProxyVolume)+" >/dev/null 2>&1", volumeFact)
}

func containerProbe() string {
	return "if command -v " + quoted(dockerEngine) + " >/dev/null 2>&1 && " +
		"facts=$(docker inspect --type container --format " + quoted(proxyFactTemplate) + " " + quoted(ProxyContainer) + " 2>/dev/null); then\n" +
		reports(quoted(KindContainer), quoted(ProxyContainer), "0", quoted(rootOwner),
			`"$(printf '%s\n' "$facts" | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func proxyRemovals() []removal {
	return []removal{
		taking(KindContainer, ProxyContainer, "the proxy ocel runs, and nothing else this engine carries"),
		taking(KindVolume, ProxyVolume, "the proxy's autosaved config and every certificate it issued, which no other machine holds"),
		taking(KindNetwork, ProxyNetwork,
			"the network ocel's deploys resolve across, which stays as long as anything this host runs is still attached to it"),
		taking(KindProxyConfig, ProxyConfig,
			"the routes every app deployed onto this host is reached through, which no deploy renders again"),
		taking(KindDir, proxyRoot, "what the proxy serves"),
	}
}
