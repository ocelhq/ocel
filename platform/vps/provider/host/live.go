package host

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	LiveBinary     = helperRoot + "/live"
	LiveSocketUnit = "ocel-live.socket"
	LiveService    = "ocel-live.service"
	liveSocketFile = "/etc/systemd/system/" + LiveSocketUnit
	liveUnitFile   = "/etc/systemd/system/" + LiveService
	LiveSocketDir  = live.SocketDir
	LiveSocket     = live.SocketPath
	liveDirTmpfs   = "rw,noexec,nosuid,size=8m"
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
		"Description=the agent that opens each app container's live values on this host",
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

func liveAgent(arch string) []byte { return embedded(liveBinaryName, arch) }

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

func ContainerRuntime(arch string) ([]byte, error) {
	named, err := Architecture(arch)
	if err != nil {
		return nil, err
	}
	return embedded(runtimeName, named), nil
}

func LiveItems(arch string) []Item {
	socket, service, agent := liveSocketUnit(), liveServiceUnit(), liveAgent(arch)
	return []Item{
		{Kind: KindFile, Name: LiveBinary, Mode: 0o755, Owner: rootOwner, Content: agent,
			Note: "serves apps their secret values"},
		{Kind: KindFile, Name: liveSocketFile, Mode: 0o644, Owner: rootOwner, Content: socket},
		{Kind: KindFile, Name: liveUnitFile, Mode: 0o644, Owner: rootOwner, Content: service},
		{Kind: KindUnit, Name: LiveSocketUnit, Owner: rootOwner, Content: unitWatchFacts(socket),
			Watch: []string{liveSocketFile},
			Slow:  true},
		{Kind: KindUnit, Name: LiveService, Owner: rootOwner, Content: unitWatchFacts(service, agent),
			Watch: []string{liveUnitFile, LiveBinary},
			Slow:  true},
	}
}

func liveRemovals() []removal {
	return []removal{
		taking(KindUnit, LiveService, ""),
		taking(KindUnit, LiveSocketUnit, ""),
		taking(KindFile, liveUnitFile, ""),
		taking(KindFile, liveSocketFile, ""),
		taking(KindFile, LiveBinary, ""),
	}
}

func unitRemoval(name string) string {
	unit := quoted(name)
	return "if systemctl cat " + unit + " >/dev/null 2>&1; then systemctl disable --now " + unit + " >/dev/null 2>&1 || true; fi"
}
