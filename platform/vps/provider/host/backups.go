package host

import (
	"bytes"
	_ "embed"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

//go:embed backups.sh
var backupsTemplate []byte

const StoreCopierImage = "rclone/rclone:1.74.4@sha256:c61954aaa32328a5486715dd063a81c7879f5195ad3505cd362deddd509dc4a1"

var backupsScript = bytes.ReplaceAll(backupsTemplate, []byte("@STORE_COPIER@"), []byte(StoreCopierImage))

const (
	BackupsHelper = boxapi.LibDir + "/backups"

	backupsServiceFile = "/etc/systemd/system/ocel-backups.service"
	backupsTimerFile   = "/etc/systemd/system/ocel-backups.timer"
	BackupsTimer       = "ocel-backups.timer"
	backupsService     = "ocel-backups.service"

	backupsDir = "backups"

	LabelBackup = "ocel.backup"

	BackupPostgres = "pg"
	BackupKV       = "kv"
	BackupStore    = "s3"
)

func BackupsDir(tier environment.Tier, container string) string {
	return StateDir(tier) + "/" + backupsDir + "/" + container
}

func backupsServiceUnit() []byte {
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=a dump of every resource on this host that asks for one",
		"Requires=" + dockerUnit,
		"After=" + dockerUnit,
		"",
		"[Service]",
		"Type=oneshot",
		"ExecStart=" + BackupsHelper + " sweep",
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

func BackupItems() []Item {
	service, timer := backupsServiceUnit(), backupsTimerUnit()
	return []Item{
		{Kind: KindFile, Name: BackupsHelper, Mode: 0o755, Owner: rootOwner, Content: backupsScript,
			Note: "dump and restore"},
		{Kind: KindFile, Name: backupsServiceFile, Mode: 0o644, Owner: rootOwner, Content: service},
		{Kind: KindFile, Name: backupsTimerFile, Mode: 0o644, Owner: rootOwner, Content: timer},
		systemdUnit(Item{Name: BackupsTimer, Owner: rootOwner, Content: unitWatchFacts(timer, service, backupsScript),
			Watch: []string{backupsTimerFile, backupsServiceFile, BackupsHelper},
			Slow:  true, Note: "daily backups"}),
	}
}

func backupRemovals() []removal {
	return []removal{
		taking(KindUnit, BackupsTimer, ""),
		taking(KindFile, backupsTimerFile, ""),
		taking(KindFile, backupsServiceFile, ""),
		taking(KindFile, BackupsHelper, ""),
	}
}

func dumpCommand(tier environment.Tier, container string) string {
	return shell.Words([]string{BackupsHelper, string(tier), "dump", container})
}

func restoreCommand(tier environment.Tier, container, file string) string {
	return shell.Words([]string{BackupsHelper, string(tier), "restore", container, file})
}
