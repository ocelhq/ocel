package machine

import (
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

const (
	KindEngine = "docker:engine"
	KindUnit   = "systemd:unit"
)

const (
	EngineFact = "engine=present\n"
	UnitFacts  = "active=active\nenabled=enabled\n"
)

func UnitCommand(i Item) string {
	name := shell.Quote(i.Name)
	if len(i.Watch) == 0 {
		return "systemctl enable --now " + name
	}
	return "set -e\nsystemctl daemon-reload\nsystemctl enable " + name + "\nsystemctl restart " + name
}

const EngineFactsRow = "probe:engine"

const SystemdAnswers = "systemctl list-unit-files >/dev/null 2>&1"

func UnitProbe(i Item) string {
	name := shell.Quote(i.Name)
	watching := ""
	for _, path := range i.Watch {
		watching += "printf 'watch=%s\\n' \"$(sha256sum " + shell.Quote(path) + " 2>/dev/null | cut -d' ' -f1)\"\n"
	}
	return `if systemctl cat ` + name + ` >/dev/null 2>&1; then
active=$(systemctl is-active ` + name + ` 2>/dev/null || true)
enabled=$(systemctl is-enabled ` + name + ` 2>/dev/null || true)
facts=$( printf 'active=%s\nenabled=%s\n' "$active" "$enabled"
` + watching + `)
` + Reports(shell.Quote(KindUnit), name, "0", shell.Quote(RootOwner), `"$(printf '%s\n' "$facts" | sha256sum | cut -d' ' -f1)"`) + `
elif ! ` + SystemdAnswers + `; then
` + Unreadable(KindUnit, name, `'systemctl did not answer'`) + `
fi`
}

func UnitWatchFacts(watched ...[]byte) []byte {
	facts := UnitFacts
	for _, content := range watched {
		facts += "watch=" + ContentSum(content) + "\n"
	}
	return []byte(facts)
}
