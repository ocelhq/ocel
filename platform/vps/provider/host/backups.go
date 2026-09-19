package host

import (
	_ "embed"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
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

	// BackupPostgres is the ocel.backup label a container carries when its dump
	// is taken with pg_dump.
	BackupPostgres = "pg"
	// BackupVolume is the ocel.backup label a container carries when its dump is
	// a tar of the volume it mounts.
	BackupVolume = "vol"
)

func BackupsDir(class providerkit.Class, container string) string {
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
			Note: "dumps a resource's data beside it, keeps the newest few, and reads one back"},
		{Kind: KindFile, Name: backupsServiceFile, Mode: 0o644, Owner: rootOwner, Content: service,
			Note: "one pass over every resource on this host that asks for a dump"},
		{Kind: KindFile, Name: backupsTimerFile, Mode: 0o644, Owner: rootOwner, Content: timer,
			Note: "runs that pass once a day, and once at boot if a day was missed"},
		{Kind: KindUnit, Name: BackupsTimer, Owner: rootOwner, Content: unitWatchFacts(timer, service, backupsScript),
			Watch: []string{backupsTimerFile, backupsServiceFile, BackupsHelper},
			Slow:  true, Note: "armed now and at every boot"},
	}
}

func backupRemovals() []removal {
	return []removal{
		taking(KindUnit, BackupsTimer, "the timer that dumped every resource on this host once a day; the dumps themselves go with the class that kept them"),
		taking(KindFile, backupsTimerFile, ""),
		taking(KindFile, backupsServiceFile, ""),
		taking(KindFile, BackupsHelper, ""),
	}
}

func dumpCommand(class providerkit.Class, container, database string) string {
	return words([]string{BackupsHelper, string(class), "dump", container, database})
}

func restoreCommand(class providerkit.Class, container, database, file string) string {
	return words([]string{BackupsHelper, string(class), "restore", container, database, file})
}
