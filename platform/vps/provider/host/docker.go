package host

import "fmt"

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
	engineFact = "engine=present\n"
	unitFacts  = "active=active\nenabled=enabled\n"
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
		Note:    "the install script at " + dockerSource + ", downloaded and run as root: deploys onto this host pull images and run them, and nothing here runs a container yet",
	}
}

func unitItem() Item {
	return Item{
		Kind:    KindUnit,
		Name:    dockerUnit,
		Owner:   rootOwner,
		Content: []byte(unitFacts),
		Slow:    true,
		Note:    "started now and at every boot, because an engine that is installed and not serving deploys nothing",
	}
}

func engineCommand() string {
	return `set -e
script=$(mktemp)
trap 'rm -f "$script"' EXIT
if command -v curl >/dev/null 2>&1; then curl -fsSL ` + dockerSource + ` -o "$script"
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
` + reports(KindEngine, dockerEngine, `"$(printf '%s' `+quoted(engineFact)+` | sha256sum | cut -d' ' -f1)"`) + `
fi`
}

func unitProbe() string {
	return `if systemctl cat ` + quoted(dockerUnit) + ` >/dev/null 2>&1; then
active=$(systemctl is-active ` + quoted(dockerUnit) + ` 2>/dev/null || true)
enabled=$(systemctl is-enabled ` + quoted(dockerUnit) + ` 2>/dev/null || true)
` + reports(KindUnit, dockerUnit, `"$(printf 'active=%s\nenabled=%s\n' "$active" "$enabled" | sha256sum | cut -d' ' -f1)"`) + `
fi`
}

func reports(kind, name, sum string) string {
	return fmt.Sprintf(`printf '%%s\t%%s\t%%s\t%%s\t%%s\n' %s %s 0 %s %s`, quoted(kind), quoted(name), quoted(rootOwner), sum)
}
