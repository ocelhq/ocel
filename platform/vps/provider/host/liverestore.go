package host

import (
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

func liveRestoreItem() machine.Item {
	return machine.Item{
		Kind:    machine.KindLiveRestore,
		Name:    machine.DaemonConfig,
		Script:  func(machine.Item) string { return liveRestoreCommand(machine.DaemonConfig) },
		Survey:  func(machine.Item) string { return liveRestoreProbe(machine.DaemonConfig) },
		Owner:   machine.RootOwner,
		Content: []byte(machine.LiveRestoreFact),
		Note:    "keeps every container running while the daemon restarts or upgrades",
	}
}

func liveRestoreProbe(config string) string {
	named := shell.Quote(config)
	return "if command -v " + shell.Quote(dockerEngine) + " >/dev/null 2>&1; then\n" +
		"restoring=$(timeout 10 " + shell.Quote(dockerEngine) + " info --format '{{.LiveRestoreEnabled}} {{.Swarm.LocalNodeState}}' 2>/dev/null) || restoring=''\n" +
		"case \"$restoring\" in\n" +
		"'true '*|*' active'|*' pending'|*' locked') decided=decided ;;\n" +
		"*) if [ -f " + named + " ] && grep -q '\"live-restore\"' " + named + "; then decided=decided; else decided=undecided; fi ;;\n" +
		"esac\n" +
		machine.Reports(shell.Quote(machine.KindLiveRestore), named, "0", shell.Quote(machine.RootOwner), `"$(printf 'live-restore=%s\n' "$decided" | sha256sum | cut -d' ' -f1)"`) + "\n" +
		"fi"
}

func liveRestoreCommand(config string) string {
	named := shell.Quote(config)
	return strings.Join([]string{
		"set -e",
		"case \"$(timeout 10 " + shell.Quote(dockerEngine) + " info --format '{{.Swarm.LocalNodeState}}' 2>/dev/null || true)\" in " + machine.SwarmStates + ") exit 0 ;; esac",
		"if [ -f " + named + " ] && grep -q '\"live-restore\"' " + named + "; then exit 0; fi",
		"mkdir -p \"$(dirname " + named + ")\"",
		"next=$(mktemp " + shell.Quote(config+".XXXXXX") + ")",
		"trap 'rm -f \"$next\"' EXIT",
		"if [ ! -s " + named + " ] || [ -z \"$(tr -d ' \\t\\r\\n{}' <" + named + ")\" ]; then",
		"printf '{\\n  \"live-restore\": true\\n}\\n' >\"$next\"",
		"else",
		"sed '0,/{/s//{\\n  \"live-restore\": true,/' " + named + " >\"$next\"",
		"fi",
		"grep -q '\"live-restore\": true' \"$next\" || { printf '%s\\n' " + shell.Quote(config+" holds no JSON object ocel can add live-restore to: add \"live-restore\": true to it yourself") + " >&2; exit 1; }",
		"dockerd --validate --config-file \"$next\" >/dev/null 2>&1 || { printf '%s\\n' " + shell.Quote("the daemon would not read "+config+" with live-restore added: add \"live-restore\": true to it yourself") + " >&2; exit 1; }",
		"chmod 0644 \"$next\"",
		"mv -f \"$next\" " + named,
		"trap - EXIT",
		"systemctl reload " + shell.Quote(dockerUnit),
	}, "\n")
}
