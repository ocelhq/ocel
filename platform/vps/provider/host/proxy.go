package host

import (
	"cmp"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
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
	containerRestart  = "unless-stopped"
	configLabel       = "ocel.config"
	containerCmdLabel = "ocel.command"
)

const (
	proxyRoot   = boxapi.ProxyDir
	ProxyConfig = boxapi.ProxyConfig
	ProxyData   = proxyRoot + "/data"
)

const (
	containerRising = 30
	containerPulls  = 5
)

const (
	migrateSysctl = "net.ipv4.tcp_migrate_req"
	migrateKnob   = "/proc/sys/net/ipv4/tcp_migrate_req"
	migrateFact   = "migrate="
	migrateSet    = "set"
	migrateUnset  = "unset"
	mountsFact    = "mounts="
	mountsIntact  = "intact"
	mountsMoved   = "moved"
	execUnstarted = "126|127"
)

var proxyCapabilities = []string{"NET_BIND_SERVICE", "DAC_OVERRIDE"}

const (
	networkFact   = "network=present"
	networkInUse  = "network=in-use"
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
			dir(SwitchboardDir, 0o755, rootOwner, ""),
			{Kind: KindFile, Name: SwitchboardBinary, Mode: 0o755, Owner: rootOwner, Content: binary,
				Note: "routes every hostname and switches releases"},
			dir(boxapi.RoutingDir, 0o750, stateOwner, ""),
			routingTable(nil),
			networkItem(),
			dir(boxapi.SwitchboardControlDir, 0o755, rootOwner, "the switchboard's control socket"),
			dir(boxapi.FrontDir, 0o700, rootOwner, ""),
			board.item(front.switchboardNote()),
		}, front.reloadGrant()...)
	}
	return []Item{
		dir(SwitchboardDir, 0o755, rootOwner, ""),
		{Kind: KindFile, Name: SwitchboardBinary, Mode: 0o755, Owner: rootOwner, Content: binary,
			Note: "routes every hostname and switches releases"},
		dir(proxyRoot, 0o750, stateOwner, ""),
		dir(caddy.PinsDir, 0o700, rootOwner, "your pinned certificates"),
		dir(boxapi.RoutingDir, 0o750, stateOwner, ""),
		proxyConfigItem(),
		routingTableItem(),
		dir(ProxyData, 0o700, rootOwner, "certificates and acme key"),
		networkItem(),
		dir(boxapi.SwitchboardControlDir, 0o755, rootOwner, "the switchboard's control socket"),
		dir(boxapi.FrontDir, 0o700, rootOwner, "where the front proxy reaches the switchboard"),
		board.item("routes :" + switchboardPort + " on the " + ProxyNetwork + " network"),
		frontProxy().item("serves :" + caddy.HTTPPort + " and :" + caddy.HTTPSPort),
	}
}

var seededTable = RoutingTable{Grace: DrainWindow}

var seededRows, seededRendering = seeded(seededTable)

func seeded(table RoutingTable) ([]byte, []byte) {
	rows, unwritten := WriteRoutingTable(table)
	rendered, unrendered := RenderProxyConfig(caddy.Builtin{}, table)
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
	config := proxyConfigItem()
	return routingTable(&config)
}

func routingTable(config *Item) Item {
	return Item{
		Kind:     KindRoutingTable,
		Name:     boxapi.RoutingTable,
		Mode:     0o640,
		Owner:    stateOwner,
		Content:  seededRows,
		Note:     "seeded here, rewritten by deploys",
		rendered: config,
	}
}

func rewrittenByDeploys(item Item) bool {
	return item.Kind == KindProxyConfig || item.Kind == KindRoutingTable
}

func seedingRouting(table Item) string {
	seeded := []Item{table}
	if table.rendered != nil {
		seeded = append(seeded, *table.rendered)
	}
	var script strings.Builder
	script.WriteString("set -e\n")
	for _, item := range seeded {
		at := shell.Quote(item.Name)
		script.WriteString("if [ -e " + at + " ] && [ ! -f " + at + " ]; then " + notAFile(item.Name) + "; fi\n")
	}
	script.WriteString(routingLocked("-x") + "staged=''\ntrap 'rm -f \"$staged\"' EXIT\n")
	seed := func(item Item) string {
		at := shell.Quote(item.Name)
		return "staged=$(mktemp " + shell.Quote(item.Name+".XXXXXX") + ")\n" +
			"printf '%s' " + shell.Quote(string(item.Content)) + " > \"$staged\"\n" +
			fmt.Sprintf("chown %s:%s \"$staged\"\nchmod %04o \"$staged\"\n", item.Owner, item.Owner, item.Mode) +
			"mv \"$staged\" " + at + "\n"
	}
	if table.rendered == nil {
		script.WriteString("if [ ! -f " + shell.Quote(table.Name) + " ]; then\n" + seed(table) + "fi\n")
	} else {
		config := *table.rendered
		script.WriteString("if [ ! -f " + shell.Quote(table.Name) + " ]; then\n" + seed(table) + seed(config) +
			"elif [ ! -f " + shell.Quote(config.Name) + " ]; then\n" + seed(config) + "fi\n")
	}
	for _, item := range seeded {
		fmt.Fprintf(&script, "chown %s:%s %s\nchmod %04o %s\n", item.Owner, item.Owner, shell.Quote(item.Name), item.Mode, shell.Quote(item.Name))
	}
	return strings.TrimSuffix(script.String(), "\n")
}

func notAFile(name string) string {
	return "printf '%s\\n' " + shell.Quote(name+" is not a regular file") + " >&2; exit 1"
}

func bindsPresent(files []string) string {
	var written string
	for _, name := range files {
		written += "if [ ! -f " + shell.Quote(name) + " ]; then " + notAFile(name) + "; fi\n"
	}
	return written
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
		Owner:   rootOwner,
		Content: []byte(networkFact + "\n"),
	}
}

type publish struct {
	addr   string
	port   string
	target string
}

func (p publish) String() string {
	if p.addr == "" {
		return p.port
	}
	return p.addr + ":" + p.port
}

type boxContainer struct {
	name     string
	image    string
	command  []string
	config   string
	binds    []string
	ports    []publish
	env      []string
	caps     []string
	fileCaps bool
	files    []string
	ready    []string
	unready  string
	inodes   []string
	joins    bool
	migrates bool
	restored bool
	network  string
	networks []userNetwork

	user string
}

func frontProxy() boxContainer {
	return boxContainer{
		name:    caddy.Container,
		image:   caddy.Image(),
		command: caddy.Command(),
		binds: []string{
			proxyRoot + ":" + caddy.ConfigDir + ":ro",
			caddy.PinsDir + ":" + caddy.PinsMount + ":ro",
			ProxyData + ":" + caddy.DataMount,
			boxapi.FrontDir + ":" + boxapi.FrontDir + ":ro",
			SwitchboardDir + ":" + switchboardMount + ":ro",
		},
		ports:    servingPublished(),
		env:      []string{proxyDataConfigEnv},
		caps:     proxyCapabilities,
		fileCaps: true,
		files:    []string{ProxyConfig},
		ready:    caddy.Ready(SwitchboardMounted),
		inodes:   []string{"stat", "-c", "%n %d:%i"},
		unready:  "did not answer with its http app loaded over its admin socket " + caddy.AdminSocket,
		migrates: true,
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
		box:     &s,
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
		mountsFact + mountsIntact,
	}
	if s.migrates {
		stated = append(stated, migrateFact+migrateSet)
	}
	for _, joined := range s.networks {
		stated = append(stated, joinedFact(joined)+networkJoined)
	}
	for _, bind := range binds {
		stated = append(stated, "bind="+bind)
	}
	slices.Sort(stated)
	return []byte(strings.Join(stated, "\n") + "\n")
}

func published(ports []publish) map[string][]map[string]string {
	bindings := map[string][]map[string]string{}
	for _, port := range ports {
		bindings[port.target+"/tcp"] = []map[string]string{{"HostIp": port.addr, "HostPort": port.port}}
	}
	return bindings
}

func proxyServing() []string { return []string{caddy.HTTPPort, caddy.HTTPSPort} }

func servingPublished() []publish {
	var publishes []publish
	for _, port := range proxyServing() {
		publishes = append(publishes, publish{port: port, target: port})
	}
	return publishes
}

func marshalled(value any) string {
	written, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(written)
}

func networkCommand() string { return networkEnsured(ProxyNetwork) }

func networkEnsured(name string) string {
	return "docker network inspect " + shell.Quote(name) + " >/dev/null 2>&1 || " +
		"docker network create " + shell.Quote(name) + " >/dev/null"
}

func (s boxContainer) run(sysctls ...string) []string {
	return s.composeArgv([]string{"docker", "run", "--detach"}, sysctls)
}

func (s boxContainer) create() []string { return s.composeArgv([]string{"docker", "create"}, nil) }

func (s boxContainer) composeArgv(leading, sysctls []string) []string {
	argv := append(slices.Clone(leading),
		"--name", s.name,
		"--restart", containerRestart,
		"--network", cmp.Or(s.network, ProxyNetwork),
	)
	for _, joined := range s.networks {
		argv = append(argv, "--network", joined.name)
	}
	argv = append(argv, "--label", containerCmdLabel+"="+strings.Join(s.command, " "))
	if s.config != "" {
		argv = append(argv, "--label", configLabel+"="+s.config)
	}
	if s.restored {
		argv = append(argv, "--label", restoredLabel+"="+restoredBy)
	}
	if s.user != "" {
		argv = append(argv, "--user", s.user)
	}
	for _, env := range s.env {
		argv = append(argv, "--env", env)
	}
	argv = append(argv, logging()...)
	argv = append(argv, confined(s.caps, s.fileCaps)...)
	for _, port := range s.ports {
		argv = append(argv, "--publish", port.String()+":"+port.target)
	}
	for _, bind := range s.binds {
		argv = append(argv, "--volume", bind)
	}
	for _, sysctl := range sysctls {
		argv = append(argv, "--sysctl", sysctl)
	}
	return append(append(argv, s.image), s.command...)
}

func (s boxContainer) writing(attempts int) string {
	return s.prepared() + s.replaced() + s.rising(attempts)
}

func (s boxContainer) writingUnderLock(attempts int, locked string, guard []string) string {
	return s.prepared() + locked + strings.Join(guard, "\n") + "\n" + s.replaced() + "flock -u 9\n" + s.rising(attempts)
}

func (s boxContainer) prepared() string {
	return "set -e\n" +
		s.networksPresent() +
		bindsPresent(s.files) +
		imagePulled(s.image, containerPulls, shell.PullAttemptSeconds, shell.PullBudgetSeconds)
}

func (s boxContainer) replaced() string {
	return "docker stop " + shell.Quote(s.name) + " >/dev/null 2>&1 || true\n" +
		"docker rm --force " + shell.Quote(s.name) + " >/dev/null 2>&1 || true\n" +
		s.started()
}

func (s boxContainer) started() string {
	if s.joins {
		return shell.Words(s.create()) + " >/dev/null\n" + rejoining(s.name) + "docker start " + shell.Quote(s.name) + " >/dev/null\n"
	}
	run := shell.Words(s.run()) + " >/dev/null\n"
	if !s.migrates {
		return run
	}
	return "if [ -e " + shell.Quote(migrateKnob) + " ]; then\n" +
		shell.Words(s.run(migrateSysctl+"=1")) + " >/dev/null\n" +
		"else\n" + run + "fi\n"
}

func (s boxContainer) networksPresent() string {
	var checks string
	for _, joined := range s.networks {
		if joined.createdByOcel {
			checks += networkEnsured(joined.name) + "\n"
			continue
		}
		missing := fmt.Sprintf("option %q names the docker network %s, which this box does not have: start the proxy that creates it, or create it with docker network create %s",
			joined.option, joined.name, joined.name)
		checks += "if ! docker network inspect " + shell.Quote(joined.name) + " >/dev/null 2>&1; then\n" +
			"printf '%s\\n' " + shell.Quote(missing) + " >&2\n" +
			"exit 1\n" +
			"fi\n"
	}
	return checks
}

func joinedFact(joined userNetwork) string { return "network:" + joined.name + "=" }

func rejoining(name string) string {
	return "for net in $(docker network ls --quiet --filter " + shell.Quote("label="+LabelTier) + "); do\n" +
		"docker network connect \"$net\" " + shell.Quote(name) + " >/dev/null\n" +
		"done\n"
}

func imagePulled(imageRef string, attempts, attemptSeconds, budgetSeconds int) string {
	image := shell.Quote(imageRef)
	spent := "{ printf '%s\\n' " + shell.Quote(fmt.Sprintf("%s was not pulled within %s", imageRef, shell.Seconds(budgetSeconds))) + " >&2; exit 1; }"
	return "at=0\n" + scriptPullBackoff.Start() +
		"pull_deadline=$(($(date +%s) + " + fmt.Sprint(budgetSeconds) + "))\n" +
		"pull_left() { left=$((pull_deadline - $(date +%s))); [ \"$left\" -gt " + fmt.Sprint(attemptSeconds) + " ] && left=" + fmt.Sprint(attemptSeconds) + "; [ \"$left\" -gt 0 ]; }\n" +
		"until " + shell.Within(shell.ProbeSeconds, "docker image inspect "+image) + " >/dev/null 2>&1 || " +
		"{ pull_left || " + spent + "; timeout -k " + fmt.Sprint(shell.KillGraceSeconds) + " \"$left\" docker pull " + image + " >/dev/null; }; do\n" +
		"at=$((at + 1))\n" +
		"if [ \"$at\" -ge " + fmt.Sprint(attempts) + " ]; then\n" +
		"printf '%s\\n' " + shell.Quote(fmt.Sprintf("%s was not pulled in %d attempts of at most %s each", imageRef, attempts, shell.Seconds(attemptSeconds))) + " >&2\n" +
		"exit 1\n" +
		"fi\n" +
		scriptPullBackoff.Again() +
		"pull_left || " + spent + "\n" +
		"done\n"
}

func (s boxContainer) readiness() []string {
	return append([]string{"docker", "exec", s.name}, s.ready...)
}

func (s boxContainer) rising(attempts int) string {
	name := shell.Quote(s.name)
	inspect := shell.Within(shell.ProbeSeconds, "docker inspect --type container --format ")
	answering := shell.Within(shell.ProbeSeconds, shell.Words(s.readiness())) + " >/dev/null 2>&1"
	return "at=0\n" +
		"while :; do\n" +
		"if [ \"$(" + inspect + shell.Quote("{{.State.Status}}") + " " + name + " 2>/dev/null)\" = running ] && " + answering + "; then exit 0; fi\n" +
		"at=$((at + 1))\n" +
		"[ \"$at\" -lt " + fmt.Sprint(attempts) + " ] || break\n" +
		"sleep 1\n" +
		"done\n" +
		"printf '%s\\n' " + shell.Quote(fmt.Sprintf("%s was created and %s within %ds", s.name, s.unready, attempts)) + " >&2\n" +
		inspect + shell.Quote("status={{.State.Status}} exit={{.State.ExitCode}} error={{.State.Error}}") +
		" " + name + " >&2 2>&1 || true\n" +
		shell.Within(shell.ProbeSeconds, "docker logs --tail 2 "+name) + " >&2 2>&1 || true\n" +
		"exit 1"
}

func presenceProbe(kind, name, ask, fact string) string {
	return "if command -v " + shell.Quote(dockerEngine) + " >/dev/null 2>&1 && " + ask + "; then\n" +
		reports(shell.Quote(kind), shell.Quote(name), "0", shell.Quote(rootOwner),
			`"$(printf '%s\n' `+shell.Quote(fact)+` | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func networkProbe() string {
	return presenceProbe(KindNetwork, ProxyNetwork,
		"docker network inspect "+shell.Quote(ProxyNetwork)+" >/dev/null 2>&1", networkFact)
}

func (s boxContainer) probe() string {
	template, normalized := ContainerFactTemplate, ""
	for _, joined := range s.networks {
		template += "\n" + joinedFact(joined) + `{{if index .NetworkSettings.Networks "` + joined.name + `"}}` + networkJoined + `{{else}}` + networkLeft + `{{end}}`
	}
	if s.migrates {
		template += "\n" + migrateFact + `{{if eq (index .HostConfig.Sysctls "` + migrateSysctl + `") "1"}}` + migrateSet + `{{else}}` + migrateUnset + `{{end}}`
		normalized = "if [ ! -e " + shell.Quote(migrateKnob) + " ]; then facts=\"${facts%" + migrateFact + "*}" + migrateFact + migrateSet + "\"; fi\n"
	}
	return "if command -v " + shell.Quote(dockerEngine) + " >/dev/null 2>&1 && " +
		"facts=$(docker inspect --type container --format " + shell.Quote(template) + " " + shell.Quote(s.name) + " 2>/dev/null); then\n" +
		normalized +
		s.mountsProbe() +
		reports(shell.Quote(KindContainer), shell.Quote(s.name), "0", shell.Quote(rootOwner),
			`"$(printf '%s\n' "$facts" | LC_ALL=C sort | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func (s boxContainer) mountsProbe() string {
	asked := append([]string{"docker", "exec", s.name}, s.inodes...)
	var compared string
	for _, bind := range s.binds {
		source, dest, _ := strings.Cut(bind, ":")
		dest, _, _ = strings.Cut(dest, ":")
		asked = append(asked, dest)
		compared += "if outside=$(stat -c %d:%i " + shell.Quote(source) + " 2>/dev/null) && " +
			"! printf '%s\\n' \"$inside\" | grep -Fqx -- " + shell.Quote(dest) + "\" $outside\"; then mounts=" + mountsMoved + "; fi\n"
	}
	return "mounts=" + mountsIntact + "\n" +
		"if inside=$(" + shell.Words(asked) + " 2>/dev/null); then\n" + compared +
		"else case $? in " + execUnstarted + ") mounts=" + mountsMoved + " ;; esac\n" +
		"fi\n" +
		"facts=\"$facts\n" + mountsFact + "$mounts\"\n"
}

func (f Front) placedRemovals() []removal {
	at := destination(openFront(f, frontBox{}))
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
		taking(KindContainer, TunnelContainer, "the tunnel to your edge"),
		taking(KindContainer, SwitchboardContainer, "ocel's switchboard"),
		taking(KindNetwork, TunnelNetwork, "kept while anything is attached"),
		taking(KindDir, TunnelDir, ""),
		taking(KindDir, boxapi.SwitchboardControlDir, ""),
		taking(KindDir, boxapi.FrontDir, ""),
		taking(KindDir, ProxyData, "certificates and acme key"),
		taking(KindNetwork, ProxyNetwork, "kept while anything is attached"),
		taking(KindProxyConfig, ProxyConfig, ""),
		taking(KindRoutingTable, boxapi.RoutingTable, "every app's routes"),
		taking(KindDir, boxapi.RoutingDir, ""),
		taking(KindDir, proxyRoot, ""),
		sharing(caddy.PinsDir, "only if empty"),
		taking(KindFile, FrontRecordPath, "which proxy fronts this box"),
	}
}
