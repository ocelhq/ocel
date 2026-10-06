package host

import (
	"context"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	SwitchboardContainer = switchboard.Name
	SwitchboardDir       = boxstore.Dir + "/switchboard"
	SwitchboardBinary    = SwitchboardDir + "/" + switchboard.Name
	switchboardMount     = "/ocel/switchboard"
	SwitchboardMounted   = switchboardMount + "/" + switchboard.Name
	switchboardPort      = "8080"
	SwitchboardUpstream  = "unix/" + switchboard.FrontSocket
)

var SwitchboardPermission = proxy.Permission{Dial: "unix/" + switchboard.AdmitSocket, Path: switchboard.AdmitPath}

var switchboardCapabilities = []string{"DAC_OVERRIDE", "DAC_READ_SEARCH"}

func switchboardBinary(arch string) []byte { return embedded(switchboard.Name, arch) }

func switchboardBox(binary []byte, front Front) boxContainer {
	return boxContainer{
		name:  SwitchboardContainer,
		image: StaticImage,
		command: append([]string{SwitchboardMounted, "serve",
			"--listen", ":" + switchboardPort,
			"--front", switchboard.FrontSocket,
			"--admit", switchboard.AdmitSocket,
			"--table", live.RoutingTable,
			"--tunnel-listen", TunnelNetwork + ":" + switchboard.TunnelListenPort,
		}, front.listening()...),
		ports:    front.published(),
		networks: append(front.joined(), userNetwork{name: TunnelNetwork, createdByOcel: true}),
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
	}
}

func placementCommand(dir string, argv ...string) []string { return placementRun(false, dir, argv) }

func placementFed(dir string, argv ...string) []string { return placementRun(true, dir, argv) }

func placementRun(fed bool, dir string, argv []string) []string {
	run := []string{"docker", "run"}
	if fed {
		run = append(run, "-i")
	}
	run = append(run, "--rm", "--network", "none", "--read-only")
	run = append(run, confined(switchboardCapabilities, false)...)
	run = append(run,
		"--mount", boundMount(SwitchboardDir, switchboardMount, "readonly"),
		"--mount", boundMount(dir, dir),
		"--env", switchboard.PlaceEnv+"="+dir,
		StaticImage, SwitchboardMounted)
	return append(run, argv...)
}

func boundMount(source, target string, options ...string) string {
	var written strings.Builder
	fields := csv.NewWriter(&written)
	if err := fields.Write(append([]string{"type=bind", "source=" + source, "target=" + target}, options...)); err != nil {
		panic(err)
	}
	fields.Flush()
	return strings.TrimSuffix(written.String(), "\n")
}

func switchboardCommand(argv ...string) []string {
	return append([]string{"docker", "exec", SwitchboardContainer, SwitchboardMounted}, argv...)
}

const (
	restoredLabel = "ocel.restored"
	restoredBy    = "deploy"
)

func (s boxContainer) sources() []string {
	paths := make([]string, 0, len(s.binds))
	for _, bind := range s.binds {
		source, _, _ := strings.Cut(bind, ":")
		paths = append(paths, source)
	}
	return paths
}

func presenceRead(board boxContainer) string {
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
		imagePulled(s.image, containerPulls, pullAttemptSeconds, pullBudgetSeconds) +
		routingLocked("-x") +
		"if [ \"$(docker inspect --type container --format " + quoted("{{.State.Running}}") + " " + quoted(s.name) + " 2>/dev/null)\" != true ]; then\n" +
		"docker rm --force " + quoted(s.name) + " >/dev/null 2>&1 || true\n" +
		s.networksPresent() +
		networkCommand() + "\n" +
		bindsPresent(s.files) +
		s.started() +
		"fi\n" +
		"flock -u 9\n" +
		s.rising(attempts)
}

func (h *Host) restoreSwitchboard(ctx context.Context, elevation string) error {
	board := switchboardBox(nil, h.proxyOption)
	said, err := h.ran(ctx, "read what "+board.name+" is started from", presenceRead(board), nil, elevation)
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
		return refusal.Refuse(refusal.CodeNotReady,
			"no %s container on %s, and it cannot be started again without %s\n"+
				"Run `ocel bootstrap %s`",
			board.name, h.named(), strings.Join(missing, ", "), environment.TierProduction)
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
		return refusal.Refuse(refusal.CodeNotReady,
			"no %s container on %s, and starting it again failed: %s\n"+
				"Run `ocel bootstrap %s`",
			board.name, h.named(), spoken(result), environment.TierProduction)
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
