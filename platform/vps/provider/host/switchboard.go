package host

import (
	"context"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	SwitchboardContainer = boxapi.Switchboard
	SwitchboardDir       = boxapi.LibDir + "/switchboard"
	SwitchboardBinary    = SwitchboardDir + "/" + boxapi.Switchboard
	switchboardMount     = "/ocel/switchboard"
	SwitchboardMounted   = switchboardMount + "/" + boxapi.Switchboard
	switchboardPort      = "8080"
	SwitchboardUpstream  = "unix/" + boxapi.FrontSocket
)

var SwitchboardPermission = proxy.Permission{Dial: "unix/" + boxapi.AdmitSocket, Path: boxapi.AdmitPath}

var switchboardCapabilities = []string{"DAC_OVERRIDE"}

func switchboardBinary(arch string) []byte { return embedded(boxapi.Switchboard, arch) }

func switchboardBox(binary []byte, front Front) boxContainer {
	return boxContainer{
		memory: switchboardMemoryBytes,
		name:   SwitchboardContainer,
		image:  StaticImage,
		command: append([]string{SwitchboardMounted, "serve",
			"--listen", ":" + switchboardPort,
			"--front", boxapi.FrontSocket,
			"--admit", boxapi.AdmitSocket,
			"--table", boxapi.RoutingTablePath,
			"--tunnel-listen", TunnelNetwork + ":" + boxapi.TunnelListenPort,
		}, front.listening()...),
		ports:    front.published(),
		networks: append(front.joined(), userNetwork{name: TunnelNetwork, createdByOcel: true}),
		config:   contentSum(binary),
		binds: []string{
			SwitchboardDir + ":" + switchboardMount + ":ro",
			boxapi.RoutingDir + ":" + boxapi.RoutingDir + ":ro",
			ConnectorRun + ":" + ConnectorRun + ":ro",
			boxapi.SwitchboardControlDir + ":" + boxapi.SwitchboardControlDir,
			boxapi.FrontDir + ":" + boxapi.FrontDir,
		},
		caps:    switchboardCapabilities,
		files:   []string{boxapi.RoutingTablePath, SwitchboardBinary},
		ready:   []string{SwitchboardMounted, "upstreams"},
		unready: "answered nothing over its control socket in " + boxapi.SwitchboardControlDir,
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
		"--env", boxapi.PlaceEnv+"="+dir,
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
		script = append(script, "[ "+test+" "+shell.Quote(path)+" ] || printf 'missing=%s\\n' "+shell.Quote(path))
	}
	for _, source := range board.sources() {
		missing("-d", source)
	}
	for _, file := range board.files {
		missing("-f", file)
	}
	binary := shell.Quote(SwitchboardBinary)
	script = append(script, "if [ -f "+binary+" ]; then printf 'sum=%s\\n' \"$(sha256sum "+binary+" | cut -d' ' -f1)\"; fi")
	return strings.Join(script, "\n")
}

func (s boxContainer) restoring(attempts int, locked string) string {
	return "set -e\n" +
		imagePulled(s.image, containerPulls, shell.PullAttemptSeconds, shell.PullBudgetSeconds) +
		locked +
		"if [ \"$(docker inspect --type container --format " + shell.Quote("{{.State.Running}}") + " " + shell.Quote(s.name) + " 2>/dev/null)\" != true ]; then\n" +
		"docker rm --force " + shell.Quote(s.name) + " >/dev/null 2>&1 || true\n" +
		s.networksPresent() +
		networkCommand() + "\n" +
		bindsPresent(s.files) +
		s.started() +
		"fi\n" +
		"flock -u 9\n" +
		s.rising(attempts)
}

func (h *Host) restoreSwitchboard(ctx context.Context, elevation ssh.Acting) error {
	board := switchboardBox(nil, h.proxyOption)
	said, err := h.Run(ctx, elevation, "read what "+board.name+" is started from", presenceRead(board), nil)
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
			board.name, h.Named(), strings.Join(missing, ", "), environment.TierProduction)
	}
	if board.config == "" {
		return unread("the switchboard binary's sha256", strings.TrimSpace(said))
	}
	board.restored = true
	result, err := h.Stream(ctx, elevation, board.restoring(containerRising, routingLocked("-x")), nil)
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return refusal.Refuse(refusal.CodeNotReady,
			"no %s container on %s, and starting it again failed: %s\n"+
				"Run `ocel bootstrap %s`",
			board.name, h.Named(), ssh.Spoken(result), environment.TierProduction)
	}
	return nil
}

func restoredCommand() string {
	return "docker inspect --type container --format " +
		shell.Quote(fmt.Sprintf(`{{index .Config.Labels %q}} {{.Created}}`, restoredLabel)) + " " +
		shell.Quote(SwitchboardContainer) + " 2>/dev/null || true"
}

func (h *Host) restoredAt(ctx context.Context, elevation ssh.Acting) (string, bool) {
	by, at, _ := strings.Cut(strings.TrimSpace(h.Evidence(ctx, elevation, restoredCommand())), " ")
	return at, by == restoredBy
}
