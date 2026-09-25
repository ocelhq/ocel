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
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const (
	KindNetwork      = "docker:network"
	KindContainer    = "docker:container"
	KindProxyConfig  = "ocel:proxy-config"
	KindRoutingTable = "ocel:routing-table"
)

const (
	ProxyNetwork       = "ocel"
	containerRestart   = "unless-stopped"
	configLabel        = "ocel.config"
	containerCmdLabel  = "ocel.command"
	proxyDataConfigEnv = "XDG_CONFIG_HOME=" + caddy.DataMount + "/config"
)

const (
	proxyRoot   = live.ProxyDir
	ProxyConfig = live.ProxyConfig
	ProxyData   = proxyRoot + "/data"
	ProxyPins   = caddy.PinsDir
)

const RenewalPort = caddy.HTTPPort

const (
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

const (
	containerRising = 30
	containerPulls  = 5
)

const (
	migrateSysctl = "net.ipv4.tcp_migrate_req"
	migrateKnob   = "/proc/sys/net/ipv4/tcp_migrate_req"
	migrateFact   = "migrate="
	migrateHeld   = "held"
	migrateUnset  = "unset"
)

var proxyCapabilities = []string{"NET_BIND_SERVICE", "DAC_OVERRIDE", "DAC_READ_SEARCH"}

const (
	networkFact   = "network=present"
	networkHeld   = "network=held"
	networkJoined = "joined"
	networkLeft   = "left"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/vps-host

//go:embed dist
var helpers embed.FS

const ContainerFactTemplate = `image={{.Config.Image}}
command={{index .Config.Labels "` + containerCmdLabel + `"}}
restart={{.HostConfig.RestartPolicy.Name}}
network={{if index .NetworkSettings.Networks "` + ProxyNetwork + `"}}` + networkJoined + `{{else}}` + networkLeft + `{{end}}
{{range .HostConfig.Binds}}bind={{.}}
{{end}}ports={{json .HostConfig.PortBindings}}
config={{index .Config.Labels "` + configLabel + `"}}
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

func embedded(name, arch string) []byte {
	read, err := helpers.ReadFile("dist/" + name + "-" + arch)
	if err != nil {
		panic(err)
	}
	return read
}

func ProxyItems(arch string) []Item {
	binary := switchboardBinary(arch)
	return []Item{
		dir(SwitchboardDir, 0o755, rootOwner, ""),
		{Kind: KindFile, Name: SwitchboardBinary, Mode: 0o755, Owner: rootOwner, Content: binary,
			Note: "routes every hostname and switches releases"},
		dir(proxyRoot, 0o750, stateOwner, ""),
		dir(ProxyPins, 0o700, rootOwner, "your pinned certificates"),
		proxyConfigItem(),
		dir(live.RoutingDir, 0o750, stateOwner, ""),
		routingTableItem(),
		dir(ProxyData, 0o700, rootOwner, "certificates and acme key"),
		networkItem(),
		switchboardStanding(binary).item("routes :" + switchboardPort + " on the " + ProxyNetwork + " network"),
		frontProxy().item("serves :" + caddy.HTTPPort + " and :" + caddy.HTTPSPort),
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
	script.WriteString(routingLocked("-x") + "staged=''\ntrap 'rm -f \"$staged\"' EXIT\n")
	seed := func(item Item) string {
		at := quoted(item.Name)
		return "staged=$(mktemp " + quoted(item.Name+".XXXXXX") + ")\n" +
			"printf '%s' " + quoted(string(item.Content)) + " > \"$staged\"\n" +
			fmt.Sprintf("chown %s:%s \"$staged\"\nchmod %04o \"$staged\"\n", item.Owner, item.Owner, item.Mode) +
			"mv \"$staged\" " + at + "\n"
	}
	script.WriteString("if [ ! -f " + quoted(table.Name) + " ]; then\n" + seed(table) + seed(config) +
		"elif [ ! -f " + quoted(config.Name) + " ]; then\n" + seed(config) + "fi\n")
	for _, item := range []Item{table, config} {
		fmt.Fprintf(&script, "chown %s:%s %s\nchmod %04o %s\n", item.Owner, item.Owner, quoted(item.Name), item.Mode, quoted(item.Name))
	}
	return strings.TrimSuffix(script.String(), "\n")
}

func notAFile(name string) string {
	return "printf '%s\\n' " + quoted(name+" is not a regular file") + " >&2; exit 1"
}

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

type boxContainer struct {
	name      string
	image     string
	command   []string
	config    string
	binds     []string
	ports     []string
	env       []string
	caps      []string
	fileCaps  bool
	files     []string
	answering string
	answer    string
	joins     bool
	migrates  bool
}

func frontProxy() boxContainer {
	return boxContainer{
		name:    caddy.Container,
		image:   caddy.Image,
		command: caddy.Command(),
		binds: []string{
			proxyRoot + ":" + caddy.ConfigDir + ":ro",
			ProxyPins + ":" + caddy.PinsMount + ":ro",
			ProxyData + ":" + caddy.DataMount,
		},
		ports:     proxyServing(),
		env:       []string{proxyDataConfigEnv},
		caps:      proxyCapabilities,
		fileCaps:  true,
		files:     []string{ProxyConfig},
		answering: "test -S " + quoted(caddy.AdminSocket),
		answer:    "did not answer over its admin socket",
		migrates:  true,
	}
}

func (s boxContainer) item(note string) Item {
	return Item{
		Kind:    KindContainer,
		Name:    s.name,
		Owner:   rootOwner,
		Content: s.facts(),
		Slow:    true,
		Note:    note,
	}
}

func (s boxContainer) facts() []byte { return s.factsOver(s.binds) }

func (s boxContainer) factsOver(binds []string) []byte {
	stated := []string{
		"image=" + s.image,
		"command=" + strings.Join(s.command, " "),
		"restart=" + containerRestart,
		"network=" + networkJoined,
		"ports=" + marshalled(published(s.ports)),
		"config=" + s.config,
		"state=running",
	}
	if s.migrates {
		stated = append(stated, migrateFact+migrateHeld)
	}
	for _, bind := range binds {
		stated = append(stated, "bind="+bind)
	}
	slices.Sort(stated)
	return []byte(strings.Join(stated, "\n") + "\n")
}

func published(ports []string) map[string][]map[string]string {
	held := map[string][]map[string]string{}
	for _, port := range ports {
		held[port+"/tcp"] = []map[string]string{{"HostIp": "", "HostPort": port}}
	}
	return held
}

func proxyServing() []string { return []string{caddy.HTTPPort, caddy.HTTPSPort} }

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

func (s boxContainer) run(sysctls ...string) []string {
	argv := []string{"docker", "run", "--detach",
		"--name", s.name,
		"--restart", containerRestart,
		"--network", ProxyNetwork,
		"--label", containerCmdLabel + "=" + strings.Join(s.command, " "),
	}
	if s.config != "" {
		argv = append(argv, "--label", configLabel+"="+s.config)
	}
	for _, env := range s.env {
		argv = append(argv, "--env", env)
	}
	argv = append(argv, logging()...)
	argv = append(argv, confined(s.caps, s.fileCaps)...)
	for _, port := range s.ports {
		argv = append(argv, "--publish", port+":"+port)
	}
	for _, bind := range s.binds {
		argv = append(argv, "--volume", bind)
	}
	for _, sysctl := range sysctls {
		argv = append(argv, "--sysctl", sysctl)
	}
	return append(append(argv, s.image), s.command...)
}

func proxyRun() []string { return frontProxy().run() }

func (s boxContainer) writing(attempts int) string {
	written := "set -e\n" +
		bindsStanding(s.files) +
		imageHeld(s.image, containerPulls) +
		"docker rm --force " + quoted(s.name) + " >/dev/null 2>&1 || true\n" +
		s.started()
	if s.joins {
		written += rejoining(s.name)
	}
	return written + s.rising(attempts)
}

func (s boxContainer) started() string {
	run := words(s.run()) + " >/dev/null\n"
	if !s.migrates {
		return run
	}
	return "if [ -e " + quoted(migrateKnob) + " ]; then\n" +
		words(s.run(migrateSysctl+"=1")) + " >/dev/null\n" +
		"else\n" + run + "fi\n"
}

func proxyWriting(attempts int) string { return frontProxy().writing(attempts) }

func rejoining(name string) string {
	return "for net in $(docker network ls --quiet --filter " + quoted("label="+LabelClass) + "); do\n" +
		"docker network connect \"$net\" " + quoted(name) + " >/dev/null\n" +
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

func (s boxContainer) rising(attempts int) string {
	name := quoted(s.name)
	inspect := "docker inspect --type container --format "
	answering := "docker exec " + name + " " + s.answering + " >/dev/null 2>&1"
	return "at=0\n" +
		"while :; do\n" +
		"if [ \"$(" + inspect + quoted("{{.State.Status}}") + " " + name + " 2>/dev/null)\" = running ] && " + answering + "; then exit 0; fi\n" +
		"at=$((at + 1))\n" +
		"[ \"$at\" -lt " + fmt.Sprint(attempts) + " ] || break\n" +
		"sleep 1\n" +
		"done\n" +
		"printf '%s\\n' " + quoted(fmt.Sprintf("%s was created and %s within %ds", s.name, s.answer, attempts)) + " >&2\n" +
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

func (s boxContainer) probe() string {
	template, normalized := ContainerFactTemplate, ""
	if s.migrates {
		template += "\n" + migrateFact + `{{if eq (index .HostConfig.Sysctls "` + migrateSysctl + `") "1"}}` + migrateHeld + `{{else}}` + migrateUnset + `{{end}}`
		normalized = "if [ ! -e " + quoted(migrateKnob) + " ]; then facts=\"${facts%" + migrateFact + migrateUnset + "}" + migrateFact + migrateHeld + "\"; fi\n"
	}
	return "if command -v " + quoted(dockerEngine) + " >/dev/null 2>&1 && " +
		"facts=$(docker inspect --type container --format " + quoted(template) + " " + quoted(s.name) + " 2>/dev/null); then\n" +
		normalized +
		reports(quoted(KindContainer), quoted(s.name), "0", quoted(rootOwner),
			`"$(printf '%s\n' "$facts" | LC_ALL=C sort | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func standingOf(item Item) boxContainer {
	if item.Name == SwitchboardContainer {
		return switchboardOver(factOf(item.Content, "config"))
	}
	return frontProxy()
}

func proxyRemovals() []removal {
	return []removal{
		taking(KindContainer, caddy.Container, "ocel's front proxy"),
		taking(KindContainer, SwitchboardContainer, "ocel's switchboard"),
		taking(KindDir, ProxyData, "certificates and acme key"),
		taking(KindNetwork, ProxyNetwork, "kept while anything is attached"),
		taking(KindProxyConfig, ProxyConfig, ""),
		taking(KindRoutingTable, live.RoutingTable, "every app's routes"),
		taking(KindDir, live.RoutingDir, ""),
		taking(KindDir, proxyRoot, ""),
		sharing(ProxyPins, "only if empty"),
	}
}
