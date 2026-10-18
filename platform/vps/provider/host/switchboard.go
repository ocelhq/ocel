package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

func switchboardBinary(arch string) []byte { return embedded(boxapi.Switchboard, arch) }

func switchboardBox(binary []byte, front Front) docker.Service {
	return docker.Service{
		Memory: switchboardMemoryBytes,
		Name:   boxapi.Switchboard,
		Image:  docker.StaticImage,
		Command: append([]string{boxapi.SwitchboardMounted, "serve",
			"--listen", ":" + routing.SwitchboardPort,
			"--front", boxapi.FrontSocket,
			"--admit", boxapi.AdmitSocket,
			"--table", boxapi.RoutingTablePath,
			"--tunnel-listen", tunnel.Network + ":" + boxapi.TunnelListenPort,
		}, front.listening()...),
		Ports:    front.published(),
		Networks: append(front.joined(), docker.Network{Name: tunnel.Network, CreatedByOcel: true}),
		Config:   contentSum(binary),
		Binds: []string{
			boxapi.SwitchboardDir + ":" + boxapi.SwitchboardMount + ":ro",
			boxapi.RoutingDir + ":" + boxapi.RoutingDir + ":ro",
			ConnectorRun + ":" + ConnectorRun + ":ro",
			boxapi.SwitchboardControlDir + ":" + boxapi.SwitchboardControlDir,
			boxapi.FrontDir + ":" + boxapi.FrontDir,
		},
		Caps:    routing.SwitchboardCapabilities,
		Files:   []string{boxapi.RoutingTablePath, boxapi.SwitchboardBinary},
		Ready:   []string{boxapi.SwitchboardMounted, "upstreams"},
		Unready: "answered nothing over its control socket in " + boxapi.SwitchboardControlDir,
		Inodes:  []string{boxapi.SwitchboardMounted, "inodes"},
		Joins:   true,
	}
}

func presenceRead(board docker.Service) string {
	var script []string
	missing := func(test, path string) {
		script = append(script, "[ "+test+" "+shell.Quote(path)+" ] || printf 'missing=%s\\n' "+shell.Quote(path))
	}
	for _, source := range board.Sources() {
		missing("-d", source)
	}
	for _, file := range board.Files {
		missing("-f", file)
	}
	binary := shell.Quote(boxapi.SwitchboardBinary)
	script = append(script, "if [ -f "+binary+" ]; then printf 'sum=%s\\n' \"$(sha256sum "+binary+" | cut -d' ' -f1)\"; fi")
	return strings.Join(script, "\n")
}

func (h *Host) restoreSwitchboard(ctx context.Context, elevation ssh.Acting) error {
	board := switchboardBox(nil, h.proxyOption)
	said, err := h.Run(ctx, elevation, "read what "+board.Name+" is started from", presenceRead(board), nil)
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
			board.Config = value
		}
	}
	if len(missing) > 0 {
		return refusal.Refuse(refusal.CodeNotReady,
			"no %s container on %s, and it cannot be started again without %s\n"+
				"Run `ocel bootstrap %s`",
			board.Name, h.Named(), strings.Join(missing, ", "), environment.TierProduction)
	}
	if board.Config == "" {
		return unread("the switchboard binary's sha256", strings.TrimSpace(said))
	}
	board.Restored = true
	result, err := h.Stream(ctx, elevation, board.Restoring(docker.ContainerRising, routing.Locked("-x")), nil)
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return refusal.Refuse(refusal.CodeNotReady,
			"no %s container on %s, and starting it again failed: %s\n"+
				"Run `ocel bootstrap %s`",
			board.Name, h.Named(), ssh.Spoken(result), environment.TierProduction)
	}
	return nil
}

func restoredCommand() string {
	return "docker inspect --type container --format " +
		shell.Quote(fmt.Sprintf(`{{index .Config.Labels %q}} {{.Created}}`, docker.RestoredLabel)) + " " +
		shell.Quote(boxapi.Switchboard) + " 2>/dev/null || true"
}

func (h *Host) restoredAt(ctx context.Context, elevation ssh.Acting) (string, bool) {
	by, at, _ := strings.Cut(strings.TrimSpace(h.Evidence(ctx, elevation, restoredCommand())), " ")
	return at, by == docker.RestoredBy
}
