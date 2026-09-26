package host

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const SwitchboardImage = "gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2"

const (
	SwitchboardContainer = switchboard.Name
	SwitchboardDir       = helperRoot + "/switchboard"
	SwitchboardBinary    = SwitchboardDir + "/" + switchboard.Name
	switchboardMount     = "/ocel/switchboard"
	SwitchboardMounted   = switchboardMount + "/" + switchboard.Name
	switchboardPort      = "8080"
	SwitchboardUpstream  = "unix/" + switchboard.FrontSocket
)

var SwitchboardPermission = proxy.Permission{Dial: "unix/" + switchboard.AdmitSocket, Path: switchboard.AdmitPath}

var switchboardCapabilities = []string{"DAC_OVERRIDE", "DAC_READ_SEARCH"}

func switchboardBinary(arch string) []byte { return embedded(switchboard.Name, arch) }

func switchboardStanding(binary []byte, front Front) boxContainer {
	var relaying []string
	if front.adopted() {
		relaying = []string{"--relay-network", ProxyNetwork}
	}
	for _, joined := range front.joined() {
		relaying = append(relaying, "--relay-network", joined.name)
	}
	return boundToPlace(boxContainer{
		name:  SwitchboardContainer,
		image: SwitchboardImage,
		command: append([]string{SwitchboardMounted, "serve",
			"--listen", ":" + switchboardPort,
			"--front", switchboard.FrontSocket,
			"--admit", switchboard.AdmitSocket,
			"--table", live.RoutingTable,
		}, relaying...),
		ports:    front.published(),
		networks: front.joined(),
		config:   contentSum(binary),
		binds: []string{
			SwitchboardDir + ":" + switchboardMount + ":ro",
			live.RoutingDir + ":" + live.RoutingDir + ":ro",
			ConnectorRun + ":" + ConnectorRun + ":ro",
			switchboard.ControlDir + ":" + switchboard.ControlDir,
			switchboard.FrontDir + ":" + switchboard.FrontDir,
		},
		caps:    switchboardCapabilities,
		files:   []string{live.RoutingTable, SwitchboardBinary},
		ready:   []string{SwitchboardMounted, "upstreams"},
		unready: "answered nothing over its control socket in " + switchboard.ControlDir,
		inodes:  []string{SwitchboardMounted, "inodes"},
		joins:   true,
	}, destination(openFront(front, frontBox{})))
}

func boundToPlace(board boxContainer, at string) boxContainer {
	if at == "" {
		return board
	}
	dir := filepath.Dir(at)
	board.binds = append(board.binds, dir+":"+dir)
	board.env = append(board.env, switchboard.PlaceEnv+"="+dir)
	return board
}

func switchboardCommand(argv ...string) []string {
	return append([]string{"docker", "exec", SwitchboardContainer, SwitchboardMounted}, argv...)
}

func switchboardFed(argv ...string) []string {
	return append([]string{"docker", "exec", "-i", SwitchboardContainer, SwitchboardMounted}, argv...)
}

const (
	restoredLabel = "ocel.restored"
	restoredBy    = "deploy"
)

func (s boxContainer) sources() []string {
	held := make([]string, 0, len(s.binds))
	for _, bind := range s.binds {
		source, _, _ := strings.Cut(bind, ":")
		held = append(held, source)
	}
	return held
}

func standingRead(board boxContainer) string {
	var script []string
	missing := func(test, path string) {
		script = append(script, "[ "+test+" "+quoted(path)+" ] || printf 'missing=%s\\n' "+quoted(path))
	}
	for _, source := range board.sources() {
		missing("-d", source)
	}
	for _, file := range board.files {
		missing("-f", file)
	}
	binary := quoted(SwitchboardBinary)
	script = append(script, "if [ -f "+binary+" ]; then printf 'sum=%s\\n' \"$(sha256sum "+binary+" | cut -d' ' -f1)\"; fi")
	return strings.Join(script, "\n")
}

func (s boxContainer) restoring(attempts int) string {
	return "set -e\n" +
		routingLocked("-x") +
		"if ! docker inspect --type container --format " + quoted("{{.Id}}") + " " + quoted(s.name) + " >/dev/null 2>&1; then\n" +
		networkCommand() + "\n" +
		bindsStanding(s.files) +
		imageHeld(s.image, containerPulls) +
		s.started() +
		rejoining(s.name) +
		"fi\n" +
		"flock -u 9\n" +
		s.rising(attempts)
}

func (h *Host) restoreSwitchboard(ctx context.Context, elevation string) error {
	board := switchboardStanding(nil, h.proxyOption)
	said, err := h.ran(ctx, "read what "+board.name+" is stood from", standingRead(board), nil, elevation)
	if err != nil {
		return err
	}
	var missing []string
	for line := range strings.Lines(said) {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "missing":
			missing = append(missing, value)
		case "sum":
			board.config = value
		}
	}
	if len(missing) > 0 {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"no %s container on %s, and it cannot be stood again without %s\n"+
				"Run `ocel bootstrap %s`",
			board.name, h.named(), strings.Join(missing, ", "), providerkit.ClassProduction)
	}
	if board.config == "" {
		return unread("the switchboard binary's sha256", strings.TrimSpace(said))
	}
	board.restored = true
	result, err := h.stream(ctx, board.restoring(containerRising), nil, elevation)
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"no %s container on %s, and standing it again failed: %s\n"+
				"Run `ocel bootstrap %s`",
			board.name, h.named(), spoken(result), providerkit.ClassProduction)
	}
	return nil
}

func restoredCommand() string {
	return "docker inspect --type container --format " +
		quoted(fmt.Sprintf(`{{index .Config.Labels %q}} {{.Created}}`, restoredLabel)) + " " +
		quoted(SwitchboardContainer) + " 2>/dev/null || true"
}

func (h *Host) restoredAt(ctx context.Context, elevation string) (string, bool) {
	by, at, _ := strings.Cut(strings.TrimSpace(h.said(ctx, restoredCommand(), elevation)), " ")
	return at, by == restoredBy
}
