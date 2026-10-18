package host

import (
	"bytes"
	_ "embed"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
)

//go:embed backups.sh
var backupsTemplate []byte

const StoreCopierImage = "rclone/rclone:1.74.4@sha256:c61954aaa32328a5486715dd063a81c7879f5195ad3505cd362deddd509dc4a1"

var backupsScript = bytes.ReplaceAll(backupsTemplate, []byte("@STORE_COPIER@"), []byte(StoreCopierImage))

const (
	backupsServiceFile = "/etc/systemd/system/ocel-backups.service"
	backupsTimerFile   = "/etc/systemd/system/ocel-backups.timer"
	BackupsTimer       = "ocel-backups.timer"
	backupsService     = "ocel-backups.service"
)

func backupsServiceUnit() []byte {
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=a dump of every resource on this host that asks for one",
		"Requires=" + dockerUnit,
		"After=" + dockerUnit,
		"",
		"[Service]",
		"Type=oneshot",
		"ExecStart=" + boxapi.BackupsHelper + " sweep",
		"Nice=10",
		"IOSchedulingClass=idle",
		"",
	}, "\n"))
}

func backupsTimerUnit() []byte {
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=takes a dump of every resource on this host once a day",
		"",
		"[Timer]",
		"OnCalendar=daily",
		"RandomizedDelaySec=1h",
		"Persistent=true",
		"Unit=" + backupsService,
		"",
		"[Install]",
		"WantedBy=timers.target",
		"",
	}, "\n"))
}

func BackupItems() []machine.Item {
	service, timer := backupsServiceUnit(), backupsTimerUnit()
	return []machine.Item{
		{Kind: machine.KindFile, Name: boxapi.BackupsHelper, Mode: 0o755, Owner: machine.RootOwner, Content: backupsScript,
			Note: "dump and restore"},
		{Kind: machine.KindFile, Name: backupsServiceFile, Mode: 0o644, Owner: machine.RootOwner, Content: service},
		{Kind: machine.KindFile, Name: backupsTimerFile, Mode: 0o644, Owner: machine.RootOwner, Content: timer},
		machine.SystemdUnit(machine.Item{Name: BackupsTimer, Owner: machine.RootOwner, Content: machine.UnitWatchFacts(timer, service, backupsScript),
			Watch: []string{backupsTimerFile, backupsServiceFile, boxapi.BackupsHelper},
			Slow:  true, Note: "daily backups"}),
	}
}

func backupRemovals() []machine.Removal {
	return []machine.Removal{
		machine.Taking(machine.KindUnit, BackupsTimer, ""),
		machine.Taking(machine.KindFile, backupsTimerFile, ""),
		machine.Taking(machine.KindFile, backupsServiceFile, ""),
		machine.Taking(machine.KindFile, boxapi.BackupsHelper, ""),
	}
}
