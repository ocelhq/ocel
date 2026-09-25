package host

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
	envSyncTemplate     = "ocel-envsync@.service"
	envSyncTemplateFile = "/etc/systemd/system/" + envSyncTemplate
	envSyncCommand      = "envsync"
	envSyncRestartWait  = "10s"
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

func ContainerArch(app, declared, runs string) (string, error) {
	if declared == "" {
		return runs, nil
	}
	if asked, _ := providerkit.GoArch(declared); asked != runs {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
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

func EnvSyncService(class providerkit.Class) string {
	return strings.Replace(envSyncTemplate, "@", "@"+string(class), 1)
}

func envSyncUnit() []byte {
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=keeps the %i class's values in step with the env sources its deploys registered",
		"Wants=network-online.target",
		"After=network-online.target",
		"",
		"[Service]",
		"ExecStart=" + LiveBinary + " " + envSyncCommand + " --class %i",
		"Restart=on-failure",
		"RestartSec=" + envSyncRestartWait,
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ReadWritePaths=" + stateRoot + "/%i/records",
		"ProtectHome=yes",
		"PrivateTmp=yes",
		"CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n"))
}

func EnvSyncItems(class providerkit.Class, arch string) []Item {
	template := envSyncUnit()
	return []Item{
		{Kind: KindFile, Name: envSyncTemplateFile, Mode: 0o644, Owner: rootOwner, Content: template},
		{Kind: KindUnit, Name: EnvSyncService(class), Owner: rootOwner, Content: unitWatchFacts(template, liveAgent(arch)),
			Watch: []string{envSyncTemplateFile, LiveBinary},
			Slow:  true, Note: "keeps values in step with env sources"},
	}
}

func envSyncRemovals() []removal {
	return []removal{taking(KindFile, envSyncTemplateFile, "")}
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
