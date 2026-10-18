package host

import (
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

const (
	KindNetwork      = "docker:network"
	KindContainer    = "docker:container"
	KindProxyConfig  = "ocel:proxy-config"
	KindRoutingTable = "ocel:routing-table"
	KindPlaced       = "ocel:placed-file"
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

//go:embed dist
var helpers embed.FS

func embedded(name, arch string) []byte {
	read, err := helpers.ReadFile("dist/" + name + "-" + arch)
	if err != nil {
		panic(err)
	}
	return read
}

func ProxyItems(arch string, front Front) []Item {
	binary := switchboardBinary(arch)
	board := switchboardBox(binary, front)
	if front.adopted() {
		return append([]Item{
			dir(boxapi.SwitchboardDir, 0o755, rootOwner, ""),
			{Kind: KindFile, Name: boxapi.SwitchboardBinary, Mode: 0o755, Owner: rootOwner, Content: binary,
				Note: "routes every hostname and switches releases"},
			dir(boxapi.RoutingDir, 0o750, stateOwner, ""),
			routingTable(nil),
			networkItem(),
			dir(boxapi.SwitchboardControlDir, 0o755, rootOwner, "the switchboard's control socket"),
			dir(boxapi.FrontDir, 0o700, rootOwner, ""),
			containerItem(board, front.switchboardNote()),
		}, front.reloadGrant()...)
	}
	return []Item{
		dir(boxapi.SwitchboardDir, 0o755, rootOwner, ""),
		{Kind: KindFile, Name: boxapi.SwitchboardBinary, Mode: 0o755, Owner: rootOwner, Content: binary,
			Note: "routes every hostname and switches releases"},
		dir(proxyRoot, 0o750, stateOwner, ""),
		dir(boxapi.PinsDir, 0o700, rootOwner, "your pinned certificates"),
		dir(boxapi.RoutingDir, 0o750, stateOwner, ""),
		proxyConfigItem(),
		routingTableItem(),
		dir(ProxyData, 0o700, rootOwner, "certificates and acme key"),
		networkItem(),
		dir(boxapi.SwitchboardControlDir, 0o755, rootOwner, "the switchboard's control socket"),
		dir(boxapi.FrontDir, 0o700, rootOwner, "where the front proxy reaches the switchboard"),
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

func proxyConfigItem() Item {
	return Item{
		Kind:      KindProxyConfig,
		Name:      ProxyConfig,
		script:    func(config Item) string { return seedingRouting(routingTable(&config), &config) },
		survey:    seededProbe,
		rewritten: true,
		Mode:      0o640,
		Owner:     stateOwner,
		Content:   seededRendering,
		Note:      "rendered from the routing table",
	}
}

func routingTableItem() Item {
	config := proxyConfigItem()
	return routingTable(&config)
}

func routingTable(config *Item) Item {
	return Item{
		Kind:      KindRoutingTable,
		Name:      boxapi.RoutingTablePath,
		Mode:      0o640,
		Owner:     stateOwner,
		Content:   seededRows,
		Note:      "seeded here, rewritten by deploys",
		script:    func(table Item) string { return seedingRouting(table, config) },
		survey:    seededProbe,
		rewritten: true,
	}
}

func seedingRouting(table Item, config *Item) string {
	seeded := []Item{table}
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
	seed := func(item Item) string {
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

func seededProbe(item Item) string {
	at := shell.Quote(item.Name)
	return "if [ -h " + at + " ]; then " +
		reports(shell.Quote(kindLink), at, "0", "''", `"$(readlink `+at+`)"`) + "\n" +
		"elif [ -f " + at + " ]; then " +
		reports(shell.Quote(item.Kind), at, `"$(stat -c %a `+at+`)"`, `"$(stat -c %U `+at+`)"`, "''") + "\nfi"
}

func networkItem() Item {
	return Item{
		Kind:    KindNetwork,
		Name:    ProxyNetwork,
		script:  func(Item) string { return networkCommand() },
		survey:  func(Item) string { return networkProbe() },
		Owner:   rootOwner,
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

func containerItem(s docker.Service, note string) Item {
	return Item{
		Kind:    KindContainer,
		Name:    s.Name,
		Owner:   rootOwner,
		Content: s.Facts(),
		Slow:    true,
		Note:    note,
		script:  func(Item) string { return s.Writing(docker.ContainerRising) },
		survey:  func(Item) string { return containerProbe(s) },
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
		reports(shell.Quote(kind), shell.Quote(name), "0", shell.Quote(rootOwner),
			`"$(printf '%s\n' `+shell.Quote(fact)+` | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func networkProbe() string {
	return presenceProbe(KindNetwork, ProxyNetwork,
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
		reports(shell.Quote(KindContainer), shell.Quote(s.Name), "0", shell.Quote(rootOwner),
			`"$(printf '%s\n' "$facts" | LC_ALL=C sort | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func containerMountsProbe(s docker.Service) string {
	asked := append([]string{"docker", "exec", s.Name}, s.Inodes...)
	var compared string
	for _, bind := range s.Binds {
		source, dest, _ := strings.Cut(bind, ":")
		dest, _, _ = strings.Cut(dest, ":")
		asked = append(asked, dest)
		compared += "if outside=$(stat -c %d:%i " + shell.Quote(source) + " 2>/dev/null) && " +
			"! printf '%s\\n' \"$inside\" | grep -Fqx -- " + shell.Quote(dest) + "\" $outside\"; then mounts=" + docker.MountsMoved + "; fi\n"
	}
	return "mounts=" + docker.MountsIntact + "\n" +
		"if inside=$(" + shell.Words(asked) + " 2>/dev/null); then\n" + compared +
		"else case $? in " + docker.ExecUnstarted + ") mounts=" + docker.MountsMoved + " ;; esac\n" +
		"fi\n" +
		"facts=\"$facts\n" + docker.MountsFact + "$mounts\"\n"
}

func (f Front) placedRemovals() []removal {
	at := routing.Destination(openFront(f, frontBox{}))
	if at == "" {
		return nil
	}
	placed := taking(KindPlaced, at, "ocel's routes in your proxy's directory")
	placed.origins = filepath.Dir(at)
	if f.Caddy != nil {
		placed.reload = f.caddyReloadCommand()
	}
	removals := []removal{placed}
	for _, grant := range f.reloadGrant() {
		removals = append(removals, taking(KindFile, grant.Name, ""))
	}
	return removals
}

func proxyRemovals() []removal {
	return []removal{
		taking(KindContainer, caddy.Container, "ocel's front proxy"),
		taking(KindContainer, tunnel.ContainerName, "the tunnel to your edge"),
		taking(KindContainer, boxapi.Switchboard, "ocel's switchboard"),
		taking(KindNetwork, tunnel.Network, "kept while anything is attached"),
		taking(KindDir, tunnel.Dir, ""),
		taking(KindDir, boxapi.SwitchboardControlDir, ""),
		taking(KindDir, boxapi.FrontDir, ""),
		taking(KindDir, ProxyData, "certificates and acme key"),
		taking(KindNetwork, ProxyNetwork, "kept while anything is attached"),
		taking(KindProxyConfig, ProxyConfig, ""),
		taking(KindRoutingTable, boxapi.RoutingTablePath, "every app's routes"),
		taking(KindDir, boxapi.RoutingDir, ""),
		taking(KindDir, proxyRoot, ""),
		sharing(boxapi.PinsDir, "only if empty"),
		taking(KindFile, FrontRecordPath, "which proxy fronts this box"),
	}
}
