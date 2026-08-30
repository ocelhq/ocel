package host

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

const (
	KindNetwork   = "docker:network"
	KindVolume    = "docker:volume"
	KindContainer = "docker:container"
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
	ProxyHelper = helperRoot + "/proxyctl"
	proxyRoot   = stateRoot + "/proxy"
	ProxyConfig = proxyRoot + "/caddy.json"
)

const (
	proxyConfigMount = "/etc/caddy/ocel.json"
	ProxyHelperMount = "/ocel/proxyctl"
	proxyDataMount   = "/data"
	ProxyAdminSocket = "/run/caddy-admin.sock"
)

const (
	networkFact = "network=present"
	volumeFact  = "volume=present"
)

//go:embed proxy.json
var proxyBaseline []byte

//go:embed proxyctl.sh
var proxyctlScript []byte

const proxyFactTemplate = `image={{.Config.Image}}
restart={{.HostConfig.RestartPolicy.Name}}
networks={{range $n, $v := .NetworkSettings.Networks}}{{$n}} {{end}}
binds={{json .HostConfig.Binds}}
ports={{json .HostConfig.PortBindings}}
config={{index .Config.Labels "` + proxyLabel + `"}}
state={{.State.Status}}`

func ProxyItems() []Item {
	return []Item{
		{Kind: KindFile, Name: ProxyHelper, Mode: 0o750, Owner: rootOwner, Content: proxyctlScript,
			Note: "gates a target, flips the proxy and reads what is still in flight"},
		dir(proxyRoot, 0o750, stateOwner, "what the proxy serves"),
		{Kind: KindFile, Name: ProxyConfig, Mode: 0o640, Owner: stateOwner, Content: proxyBaseline,
			Note: "the proxy's whole configuration, which every deploy replaces"},
		networkItem(),
		volumeItem(),
		containerItem(),
	}
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
	return fmt.Appendf(nil, "image=%s\nrestart=%s\nnetworks=%s \nbinds=%s\nports=%s\nconfig=%s\nstate=running\n",
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

func containerCommand() string {
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
	argv = append(argv, ProxyImage, "caddy", "run", "--config", proxyConfigMount, "--resume")
	return "set -e\n" +
		"docker rm --force " + quoted(ProxyContainer) + " >/dev/null 2>&1 || true\n" +
		words(argv) + " >/dev/null"
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
		taking(KindDir, proxyRoot, "what the proxy serves"),
	}
}
