package host

import (
	_ "embed"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

//go:embed backups.sh
var backupsScript []byte

const (
	BackupsHelper = helperRoot + "/backups"

	backupsServiceFile = "/etc/systemd/system/ocel-backups.service"
	backupsTimerFile   = "/etc/systemd/system/ocel-backups.timer"
	BackupsTimer       = "ocel-backups.timer"
	backupsService     = "ocel-backups.service"

	backupsDir = "backups"

	LabelBackup = "ocel.backup"

	BackupPostgres = "pg"
	BackupVolume   = "vol"
)

func BackupsDir(class edge.Class, container string) string {
	return StateDir(class) + "/" + backupsDir + "/" + container
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
		{Kind: KindUnit, Name: BackupsTimer, Owner: rootOwner, Content: unitWatchFacts(timer, service, backupsScript),
			Watch: []string{backupsTimerFile, backupsServiceFile, BackupsHelper},
			Slow:  true, Note: "daily backups"},
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

func dumpCommand(class edge.Class, container, database string) string {
	return words([]string{BackupsHelper, string(class), "dump", container, database})
}

func restoreCommand(class edge.Class, container, database, file string) string {
	return words([]string{BackupsHelper, string(class), "restore", container, database, file})
}
