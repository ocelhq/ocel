package host

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	envSourceSyncTemplate     = "ocel-envsourcesync@.service"
	envSourceSyncTemplateFile = "/etc/systemd/system/" + envSourceSyncTemplate
	envSourceSyncRestartWait  = "10s"

	unitInstance edge.Class = "%i"
)

func EnvSourceSyncService(class edge.Class) string {
	return strings.Replace(envSourceSyncTemplate, "@", "@"+string(class), 1)
}

func envSourceSyncUnit() []byte {
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=keeps the %i class's values in step with the env sources its deploys registered",
		"Wants=network-online.target",
		"After=network-online.target",
		"",
		"[Service]",
		"ExecStart=" + LiveBinary + " " + live.EnvSourceSyncCommand + " --class %i",
		"Restart=on-failure",
		"RestartSec=" + envSourceSyncRestartWait,
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ReadWritePaths=" + RecordsDir(unitInstance),
		"ProtectHome=yes",
		"PrivateTmp=yes",
		"CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n"))
}

func EnvSourceSyncItems(class edge.Class, architecture string) []Item {
	template := envSourceSyncUnit()
	return []Item{
		{Kind: KindFile, Name: envSourceSyncTemplateFile, Mode: 0o644, Owner: rootOwner, Content: template},
		{Kind: KindUnit, Name: EnvSourceSyncService(class), Owner: rootOwner, Content: unitWatchFacts(template, liveAgent(architecture)),
			Watch: []string{envSourceSyncTemplateFile, LiveBinary},
			Slow:  true, Note: "keeps values in step with env sources"},
	}
}

func envSourceSyncRemovals() []removal {
	return []removal{taking(KindFile, envSourceSyncTemplateFile, "")}
}
