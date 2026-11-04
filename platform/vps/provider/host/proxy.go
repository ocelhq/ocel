package host

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

const (
	ProxyNetwork       = boxapi.ProxyNetwork
	proxyDataConfigEnv = "XDG_CONFIG_HOME=" + caddy.DataMount + "/config"
)

const (
	proxyRoot   = boxapi.ProxyDir
	ProxyConfig = boxapi.ProxyConfig
	ProxyData   = proxyRoot + "/data"
)

var proxyCapabilities = []string{"NET_BIND_SERVICE", "DAC_OVERRIDE"}

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/vps-host

func ProxyItems(assets Assets, arch string, fronting front.Proxy) []machine.Item {
	binary := switchboardBinary(assets, arch)
	board := switchboardBox(binary, fronting)
	if fronting.Adopted() {
		return append([]machine.Item{
			machine.Dir(boxapi.SwitchboardDir, 0o755, machine.RootOwner, ""),
			{Kind: machine.KindFile, Name: boxapi.SwitchboardBinary, Mode: 0o755, Owner: machine.RootOwner, Content: binary,
				Note: "routes every hostname and switches releases"},
			machine.Dir(boxapi.RoutingDir, 0o750, stateOwner, ""),
			routingTable(nil),
			networkItem(),
			machine.Dir(boxapi.SwitchboardControlDir, 0o755, machine.RootOwner, "the switchboard's control socket"),
			machine.Dir(boxapi.FrontDir, 0o700, machine.RootOwner, ""),
			containerItem(board, switchboardNote(fronting)),
		}, fronting.Front().ReloadGrant()...)
	}
	return []machine.Item{
		machine.Dir(boxapi.SwitchboardDir, 0o755, machine.RootOwner, ""),
		{Kind: machine.KindFile, Name: boxapi.SwitchboardBinary, Mode: 0o755, Owner: machine.RootOwner, Content: binary,
			Note: "routes every hostname and switches releases"},
		machine.Dir(proxyRoot, 0o750, stateOwner, ""),
		machine.Dir(boxapi.PinsDir, 0o700, machine.RootOwner, "your pinned certificates"),
		pinsSudoersItem(),
		machine.Dir(boxapi.RoutingDir, 0o750, stateOwner, ""),
		proxyConfigItem(),
		routingTableItem(),
		machine.Dir(ProxyData, 0o700, machine.RootOwner, "certificates and acme key"),
		networkItem(),
		machine.Dir(boxapi.SwitchboardControlDir, 0o755, machine.RootOwner, "the switchboard's control socket"),
		machine.Dir(boxapi.FrontDir, 0o700, machine.RootOwner, "where the front proxy reaches the switchboard"),
		containerItem(board, "routes :"+routing.SwitchboardPort+" on the "+ProxyNetwork+" network"),
		containerItem(frontProxy(), "serves :"+caddy.HTTPPort+" and :"+caddy.HTTPSPort),
	}
}

var seededTable = boxapi.RoutingTable{Grace: routing.DrainWindow}

var seededRows, seededRendering = seeded(seededTable)

func seeded(table boxapi.RoutingTable) ([]byte, []byte) {
	rows, unwritten := routing.WriteRoutingTable(table)
	rendered, unrendered := routing.RenderProxyConfig(caddy.Builtin{}, table)
	if err := errors.Join(unwritten, unrendered); err != nil {
		panic(err)
	}
	return rows, rendered
}

func proxyConfigItem() machine.Item {
	return machine.Item{
		Kind:      machine.KindProxyConfig,
		Name:      ProxyConfig,
		Script:    func(config machine.Item) string { return seedingRouting(routingTable(&config), &config) },
		Survey:    seededProbe,
		Rewritten: true,
		Mode:      0o640,
		Owner:     stateOwner,
		Content:   seededRendering,
		Note:      "rendered from the routing table",
	}
}

func routingTableItem() machine.Item {
	config := proxyConfigItem()
	return routingTable(&config)
}

func routingTable(config *machine.Item) machine.Item {
	return machine.Item{
		Kind:      machine.KindRoutingTable,
		Name:      boxapi.RoutingTablePath,
		Mode:      0o640,
		Owner:     stateOwner,
		Content:   seededRows,
		Note:      "seeded here, rewritten by deploys",
		Script:    func(table machine.Item) string { return seedingRouting(table, config) },
		Survey:    seededProbe,
		Rewritten: true,
	}
}

func seedingRouting(table machine.Item, config *machine.Item) string {
	seeded := []machine.Item{table}
	if config != nil {
		seeded = append(seeded, *config)
	}
	var script strings.Builder
	script.WriteString("set -e\n")
	for _, item := range seeded {
		at := shell.Quote(item.Name)
		script.WriteString("if [ -e " + at + " ] && [ ! -f " + at + " ]; then " + docker.NotAFile(item.Name) + "; fi\n")
	}
	script.WriteString(routing.Locked("-x") + "staged=''\ntrap 'rm -f \"$staged\"' EXIT\n")
	seed := func(item machine.Item) string {
		at := shell.Quote(item.Name)
		return "staged=$(mktemp " + shell.Quote(item.Name+".XXXXXX") + ")\n" +
			"printf '%s' " + shell.Quote(string(item.Content)) + " > \"$staged\"\n" +
			fmt.Sprintf("chown %s:%s \"$staged\"\nchmod %04o \"$staged\"\n", item.Owner, item.Owner, item.Mode) +
			"mv \"$staged\" " + at + "\n"
	}
	if config == nil {
		script.WriteString("if [ ! -f " + shell.Quote(table.Name) + " ]; then\n" + seed(table) + "fi\n")
	} else {
		config := *config
		script.WriteString("if [ ! -f " + shell.Quote(table.Name) + " ]; then\n" + seed(table) + seed(config) +
			"elif [ ! -f " + shell.Quote(config.Name) + " ]; then\n" + seed(config) + "fi\n")
	}
	for _, item := range seeded {
		fmt.Fprintf(&script, "chown %s:%s %s\nchmod %04o %s\n", item.Owner, item.Owner, shell.Quote(item.Name), item.Mode, shell.Quote(item.Name))
	}
	return strings.TrimSuffix(script.String(), "\n")
}

func seededProbe(item machine.Item) string {
	at := shell.Quote(item.Name)
	return "if [ -h " + at + " ]; then " +
		machine.Reports(shell.Quote(machine.KindLink), at, "0", "''", `"$(readlink `+at+`)"`) + "\n" +
		"elif [ -f " + at + " ]; then " +
		machine.Reports(shell.Quote(item.Kind), at, `"$(stat -c %a `+at+`)"`, `"$(stat -c %U `+at+`)"`, "''") + "\nfi"
}

func networkItem() machine.Item {
	return machine.Item{
		Kind:    machine.KindNetwork,
		Name:    ProxyNetwork,
		Script:  func(machine.Item) string { return networkCommand() },
		Survey:  func(machine.Item) string { return networkProbe() },
		Owner:   machine.RootOwner,
		Content: []byte(docker.NetworkFact + "\n"),
	}
}

const (
	proxyMemoryBytes       = 512 << 20
	switchboardMemoryBytes = 256 << 20
)

func frontProxy() docker.Service {
	return docker.Service{
		Memory:  proxyMemoryBytes,
		Name:    caddy.Container,
		Image:   caddy.Image(),
		Command: caddy.Command(),
		Binds: []string{
			proxyRoot + ":" + caddy.ConfigDir + ":ro",
			boxapi.PinsDir + ":" + caddy.PinsMount + ":ro",
			ProxyData + ":" + caddy.DataMount,
			boxapi.FrontDir + ":" + boxapi.FrontDir + ":ro",
			boxapi.SwitchboardDir + ":" + boxapi.SwitchboardMount + ":ro",
		},
		Ports:    servingPublished(),
		Env:      []string{proxyDataConfigEnv},
		Caps:     proxyCapabilities,
		FileCaps: true,
		Files:    []string{ProxyConfig},
		Ready:    caddy.Ready(boxapi.SwitchboardMounted),
		Inodes:   []string{"stat", "-c", "%n %d:%i"},
		Unready:  "did not answer with its http app loaded over its admin socket " + caddy.AdminSocket,
		Migrates: true,
	}
}

func containerItem(s docker.Service, note string) machine.Item {
	return machine.Item{
		Kind:    machine.KindContainer,
		Name:    s.Name,
		Owner:   machine.RootOwner,
		Content: s.Facts(),
		Slow:    true,
		Note:    note,
		Script:  func(machine.Item) string { return s.Writing(docker.ContainerRising) },
		Survey:  func(machine.Item) string { return containerProbe(s) },
	}
}

func proxyServing() []string { return []string{caddy.HTTPPort, caddy.HTTPSPort} }

func servingPublished() []docker.Publish {
	var publishes []docker.Publish
	for _, port := range proxyServing() {
		publishes = append(publishes, docker.Publish{Port: port, Target: port})
	}
	return publishes
}

func networkCommand() string { return docker.EnsuringNetwork(ProxyNetwork) }

func presenceProbe(kind, name, ask, fact string) string {
	return "if command -v " + shell.Quote(dockerEngine) + " >/dev/null 2>&1 && " + ask + "; then\n" +
		machine.Reports(shell.Quote(kind), shell.Quote(name), "0", shell.Quote(machine.RootOwner),
			`"$(printf '%s\n' `+shell.Quote(fact)+` | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func networkProbe() string {
	return presenceProbe(machine.KindNetwork, ProxyNetwork,
		"docker network inspect "+shell.Quote(ProxyNetwork)+" >/dev/null 2>&1", docker.NetworkFact)
}

func containerProbe(s docker.Service) string {
	template, normalized := docker.ContainerFactTemplate, ""
	for _, joined := range s.Networks {
		template += "\n" + docker.JoinedFact(joined) + `{{if index .NetworkSettings.Networks "` + joined.Name + `"}}` + docker.NetworkJoined + `{{else}}` + docker.NetworkLeft + `{{end}}`
	}
	if s.Migrates {
		template += "\n" + docker.MigrateFact + `{{if eq (index .HostConfig.Sysctls "` + docker.MigrateSysctl + `") "1"}}` + docker.MigrateSet + `{{else}}` + docker.MigrateUnset + `{{end}}`
		normalized = "if [ ! -e " + shell.Quote(docker.MigrateKnob) + " ]; then facts=\"${facts%" + docker.MigrateFact + "*}" + docker.MigrateFact + docker.MigrateSet + "\"; fi\n"
	}
	return "if command -v " + shell.Quote(dockerEngine) + " >/dev/null 2>&1 && " +
		"facts=$(docker inspect --type container --format " + shell.Quote(template) + " " + shell.Quote(s.Name) + " 2>/dev/null); then\n" +
		normalized +
		containerMountsProbe(s) +
		machine.Reports(shell.Quote(machine.KindContainer), shell.Quote(s.Name), "0", shell.Quote(machine.RootOwner),
			`"$(printf '%s\n' "$facts" | LC_ALL=C sort | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func containerMountsProbe(s docker.Service) string {
	asked := append([]string{"docker", "exec", s.Name}, s.MountInodesCommand()...)
	var compared string
	for _, bind := range s.Binds {
		source, dest, _ := strings.Cut(bind, ":")
		dest, _, _ = strings.Cut(dest, ":")
		compared += "if outside=$(stat -c %d:%i " + shell.Quote(source) + " 2>/dev/null) && " +
			"! printf '%s\\n' \"$inside\" | grep -Fqx -- " + shell.Quote(dest) + "\" $outside\"; then mounts=" + docker.MountsMoved + "; fi\n"
	}
	return "mounts=" + docker.MountsIntact + "\n" +
		"if inside=$(" + shell.Words(asked) + " 2>/dev/null); then\n" + compared +
		"else case $? in " + docker.ExecUnstarted + ") mounts=" + docker.MountsMoved + " ;; esac\n" +
		"fi\n" +
		"facts=\"$facts\n" + docker.MountsFact + "$mounts\"\n"
}

func placedRemovals(f front.Proxy) []machine.Removal {
	at := routing.Destination(f.Front().Open(frontBox{}))
	if at == "" {
		return nil
	}
	placed := machine.Taking(machine.KindPlaced, at, "ocel's routes in your proxy's directory")
	placed.Origins = filepath.Dir(at)
	placed.Reload = f.Front().ReloadCommand()
	removals := []machine.Removal{placed}
	for _, grant := range f.Front().ReloadGrant() {
		removals = append(removals, machine.Taking(machine.KindFile, grant.Name, ""))
	}
	return removals
}

func proxyRemovals() []machine.Removal {
	return []machine.Removal{
		machine.Taking(machine.KindContainer, caddy.Container, "ocel's front proxy"),
		machine.Taking(machine.KindContainer, tunnel.ContainerName, "the tunnel to your edge"),
		machine.Taking(machine.KindContainer, boxapi.Switchboard, "ocel's switchboard"),
		machine.Taking(machine.KindNetwork, tunnel.Network, "kept while anything is attached"),
		machine.Taking(machine.KindDir, tunnel.Dir, ""),
		machine.Taking(machine.KindDir, boxapi.SwitchboardControlDir, ""),
		machine.Taking(machine.KindDir, boxapi.FrontDir, ""),
		machine.Taking(machine.KindDir, ProxyData, "certificates and acme key"),
		machine.Taking(machine.KindNetwork, ProxyNetwork, "kept while anything is attached"),
		machine.Taking(machine.KindProxyConfig, ProxyConfig, ""),
		machine.Taking(machine.KindRoutingTable, boxapi.RoutingTablePath, "every app's routes"),
		machine.Taking(machine.KindDir, boxapi.RoutingDir, ""),
		machine.Taking(machine.KindDir, proxyRoot, ""),
		machine.Taking(machine.KindFile, sudoersPins, ""),
		machine.Sharing(boxapi.PinsDir, "only if empty"),
		machine.Taking(machine.KindFile, FrontRecordPath, "which proxy fronts this box"),
	}
}
