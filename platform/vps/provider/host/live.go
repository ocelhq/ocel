package host

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	LiveBinary     = boxapi.LiveBinary
	LiveSocketUnit = "ocel-live.socket"
	LiveService    = "ocel-live.service"
	liveSocketFile = "/etc/systemd/system/" + LiveSocketUnit
	liveUnitFile   = "/etc/systemd/system/" + LiveService
	LiveSocket     = boxapi.SocketPath
)

const (
	liveBinaryName  = "ocel-live"
	runtimeName     = "ocel-runtime"
	liveSocketMode  = "0666"
	liveSocketDirs  = "0755"
	liveRestartWait = "2s"
)

func liveSocketUnit() []byte {
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=the socket every app container on this host asks for its live values",
		"Before=" + dockerUnit,
		"",
		"[Socket]",
		"ListenStream=" + LiveSocket,
		"SocketMode=" + liveSocketMode,
		"DirectoryMode=" + liveSocketDirs,
		"",
		"[Install]",
		"WantedBy=sockets.target",
		"",
	}, "\n"))
}

func liveServiceUnit() []byte {
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=the agent that opens each app container's live values and runs its topics and tasks on this host",
		"Requires=" + LiveSocketUnit,
		"After=" + LiveSocketUnit,
		"",
		"[Service]",
		"ExecStart=" + LiveBinary,
		"Restart=on-failure",
		"RestartSec=" + liveRestartWait,
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ProtectHome=yes",
		"PrivateTmp=yes",
		"CapabilityBoundingSet=CAP_DAC_READ_SEARCH",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n"))
}

func liveAgent(architecture string) []byte { return embedded(liveBinaryName, architecture) }

func ContainerArch(app, declared, runs string) (string, error) {
	if declared == "" {
		return runs, nil
	}
	if asked, _ := arch.GoArch(declared); asked != runs {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"app %s declares arch %q, and this host runs %s\nDrop the arch, or deploy to a %s host",
			app, declared, runs, declared)
	}
	return runs, nil
}

func ContainerRuntime(architecture string) ([]byte, error) {
	named, err := ssh.Architecture(architecture)
	if err != nil {
		return nil, err
	}
	return embedded(runtimeName, named), nil
}

func (h *Host) IsAgentCurrent(ctx context.Context) (bool, error) {
	return h.isCurrent(ctx, "survey the box agent", func(arch string) machine.Item { return newAgentItem(liveAgent(arch)) })
}

func (h *Host) isCurrent(ctx context.Context, what string, newItem func(arch string) machine.Item) (bool, error) {
	arch, err := h.Arch(ctx)
	if err != nil {
		return false, err
	}
	item := newItem(arch)
	rendered, err := h.Run(ctx, ssh.RootWhenAble, what, machine.SurveyScript([]machine.Item{item}), nil)
	if err != nil {
		return false, err
	}
	observed, _, err := machine.ReadSurvey(rendered)
	if err != nil {
		return false, err
	}
	return observed[item.ID()] == item.Digest(), nil
}

func newAgentItem(agent []byte) machine.Item {
	return machine.Item{Kind: machine.KindFile, Name: LiveBinary, Mode: 0o755, Owner: machine.RootOwner, Content: agent,
		Note: "serves apps their secret values and runs their topics and tasks"}
}

func LiveItems(architecture string) []machine.Item {
	socket, service, agent := liveSocketUnit(), liveServiceUnit(), liveAgent(architecture)
	return []machine.Item{
		{Kind: machine.KindFile, Name: liveSocketFile, Mode: 0o644, Owner: machine.RootOwner, Content: socket},
		{Kind: machine.KindFile, Name: liveUnitFile, Mode: 0o644, Owner: machine.RootOwner, Content: service},
		machine.SystemdUnit(machine.Item{Name: LiveSocketUnit, Owner: machine.RootOwner, Content: machine.UnitWatchFacts(socket),
			Watch: []string{liveSocketFile},
			Slow:  true}),
		machine.SystemdUnit(machine.Item{Name: LiveService, Owner: machine.RootOwner, Content: machine.UnitWatchFacts(service, agent),
			Watch: []string{liveUnitFile, LiveBinary},
			Slow:  true}),
	}
}

func liveRemovals() []machine.Removal {
	return []machine.Removal{
		machine.Taking(machine.KindUnit, LiveService, ""),
		machine.Taking(machine.KindUnit, LiveSocketUnit, ""),
		machine.Taking(machine.KindFile, liveUnitFile, ""),
		machine.Taking(machine.KindFile, liveSocketFile, ""),
		machine.Taking(machine.KindFile, LiveBinary, ""),
	}
}
