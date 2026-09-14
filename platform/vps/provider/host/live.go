package host

import (
	"strings"

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
	LiveDir        = live.ProjectionDir
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

func liveAgent(arch string) []byte {
	read, err := proxyHelpers.ReadFile("dist/" + liveBinaryName + "-" + arch)
	if err != nil {
		panic(err)
	}
	return read
}

func ContainerRuntime(arch string) ([]byte, error) {
	named, err := Architecture(arch)
	if err != nil {
		return nil, err
	}
	read, err := proxyHelpers.ReadFile("dist/" + runtimeName + "-" + named)
	if err != nil {
		panic(err)
	}
	return read, nil
}

func LiveItems(arch string) []Item {
	socket, service, agent := liveSocketUnit(), liveServiceUnit(), liveAgent(arch)
	return []Item{
		{Kind: KindFile, Name: LiveBinary, Mode: 0o755, Owner: rootOwner, Content: agent,
			Note: "answers each app container with the values its own deploy declared, opened under the class key as root"},
		{Kind: KindFile, Name: liveSocketFile, Mode: 0o644, Owner: rootOwner, Content: socket,
			Note: "binds " + LiveSocket + " before the engine starts, so a container that restarts on a boot finds it"},
		{Kind: KindFile, Name: liveUnitFile, Mode: 0o644, Owner: rootOwner, Content: service,
			Note: "runs the agent as root on the socket systemd hands it, with the filesystem read-only to it"},
		{Kind: KindUnit, Name: LiveSocketUnit, Owner: rootOwner, Content: unitWatchFacts(socket),
			Watch: []string{liveSocketFile},
			Slow:  true, Note: "listening now and at every boot, ahead of the engine"},
		{Kind: KindUnit, Name: LiveService, Owner: rootOwner, Content: unitWatchFacts(service, agent),
			Watch: []string{liveUnitFile, LiveBinary},
			Slow:  true, Note: "started now and at every boot, and restarted whenever the unit or the agent changes"},
	}
}

func liveRemovals() []removal {
	return []removal{
		taking(KindUnit, LiveService, "the agent that opened each app container's live values; a container still running keeps the last values it read and reads no more"),
		taking(KindUnit, LiveSocketUnit, "the socket every app container asked for its values"),
		taking(KindFile, liveUnitFile, ""),
		taking(KindFile, liveSocketFile, ""),
		taking(KindFile, LiveBinary, ""),
	}
}

func unitRemoval(name string) string {
	unit := quoted(name)
	return "if systemctl cat " + unit + " >/dev/null 2>&1; then systemctl disable --now " + unit + " >/dev/null 2>&1 || true; fi"
}
