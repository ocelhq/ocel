package host

import "github.com/ocelhq/ocel/pkg/providerkit"

const (
	KindEngine = "docker:engine"
	KindUnit   = "systemd:unit"
)

const (
	dockerEngine = "docker"
	dockerUnit   = "docker.service"
	dockerSource = "https://get.docker.com"
)

const (
	engineFact   = "engine=present\n"
	unservedFact = "unserved"
	unitFacts    = "active=active\nenabled=enabled\n"
)

func EngineItems() []Item {
	return []Item{engineItem(), unitItem()}
}

func engineItem() Item {
	return Item{
		Kind:    KindEngine,
		Name:    dockerEngine,
		Owner:   rootOwner,
		Content: []byte(engineFact),
		Slow:    true,
		Note:    "runs the install script at " + dockerSource + " as root",
	}
}

func unitItem() Item {
	return Item{
		Kind:    KindUnit,
		Name:    dockerUnit,
		Owner:   rootOwner,
		Content: []byte(unitFacts),
		Slow:    true,
		Note:    "started now and at every boot",
	}
}

func keptEngine() removal {
	return removal{
		kind:   KindEngine,
		path:   dockerEngine,
		action: providerkit.ActionKeep,
		reason: "the engine and every container it runs stay: ocel installed it, and removing ocel is not removing the workloads this host serves",
	}
}

func engineCommand() string {
	return `set -e
script=$(mktemp)
trap 'rm -f "$script"' EXIT
if command -v curl >/dev/null 2>&1; then curl -fsSL --retry 5 --retry-delay 2 ` + dockerSource + ` -o "$script"
elif command -v wget >/dev/null 2>&1; then wget -qO "$script" ` + dockerSource + `
else echo 'neither curl nor wget stands on this host, so ocel cannot fetch ` + dockerSource + `' >&2; exit 1
fi
sh "$script"`
}

func unitCommand() string {
	return "systemctl enable --now " + quoted(dockerUnit)
}

func engineProbe() string {
	return `if command -v ` + quoted(dockerEngine) + ` >/dev/null 2>&1; then
if systemctl cat ` + quoted(dockerUnit) + ` >/dev/null 2>&1; then engine=present; else engine=` + quoted(unservedFact) + `; fi
` + reports(quoted(KindEngine), quoted(dockerEngine), "0", quoted(rootOwner),
		`"$(printf 'engine=%s\n' "$engine" | sha256sum | cut -d' ' -f1)"`) + `
fi`
}

func unitProbe() string {
	return `if systemctl cat ` + quoted(dockerUnit) + ` >/dev/null 2>&1; then
active=$(systemctl is-active ` + quoted(dockerUnit) + ` 2>/dev/null || true)
enabled=$(systemctl is-enabled ` + quoted(dockerUnit) + ` 2>/dev/null || true)
` + reports(quoted(KindUnit), quoted(dockerUnit), "0", quoted(rootOwner), `"$(printf 'active=%s\nenabled=%s\n' "$active" "$enabled" | sha256sum | cut -d' ' -f1)"`) + `
fi`
}
