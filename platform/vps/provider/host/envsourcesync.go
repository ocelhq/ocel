package host

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
)

const (
	envSourceSyncTemplate     = "ocel-envsourcesync@.service"
	envSourceSyncTemplateFile = "/etc/systemd/system/" + envSourceSyncTemplate
	envSourceSyncRestartWait  = "10s"

	unitInstance environment.Tier = "%i"
)

func EnvSourceSyncService(tier environment.Tier) string {
	return strings.Replace(envSourceSyncTemplate, "@", "@"+string(tier), 1)
}

func envSourceSyncUnit() []byte {
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=keeps the %i tier's values in step with the env sources its deploys registered",
		"Wants=network-online.target",
		"After=network-online.target",
		"",
		"[Service]",
		"ExecStart=" + LiveBinary + " " + boxapi.EnvSourceSyncCommand + " --tier %i",
		"Restart=on-failure",
		"RestartSec=" + envSourceSyncRestartWait,
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ReadWritePaths=" + KeyValuesDir(unitInstance),
		"ProtectHome=yes",
		"PrivateTmp=yes",
		"CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n"))
}

func EnvSourceSyncItems(tier environment.Tier, architecture string) []machine.Item {
	template := envSourceSyncUnit()
	return []machine.Item{
		{Kind: machine.KindFile, Name: envSourceSyncTemplateFile, Mode: 0o644, Owner: machine.RootOwner, Content: template},
		machine.SystemdUnit(machine.Item{Name: EnvSourceSyncService(tier), Owner: machine.RootOwner, Content: machine.UnitWatchFacts(template, liveAgent(architecture)),
			Watch: []string{envSourceSyncTemplateFile, LiveBinary},
			Slow:  true, Note: "keeps values in step with env sources"}),
	}
}

func envSourceSyncRemovals() []machine.Removal {
	return []machine.Removal{machine.Taking(machine.KindFile, envSourceSyncTemplateFile, "")}
}
