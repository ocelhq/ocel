package boxapi

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

const (
	BackupsHelper = LibDir + "/backups"

	backupsDir = "backups"

	LabelBackup = "ocel.backup"

	BackupPostgres = "pg"
	BackupKV       = "kv"
	BackupStore    = "s3"
)

func BackupsDir(tier environment.Tier, container string) string {
	return StateDir(tier) + "/" + backupsDir + "/" + container
}

func DumpCommand(tier environment.Tier, container string) string {
	return shell.Words([]string{BackupsHelper, string(tier), "dump", container})
}

func RestoreCommand(tier environment.Tier, container, file string) string {
	return shell.Words([]string{BackupsHelper, string(tier), "restore", container, file})
}
