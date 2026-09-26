package host

import (
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	KindEngine = "docker:engine"
	KindUnit   = "systemd:unit"
)

const (
	dockerEngine = "docker"
	dockerUnit   = "docker.service"
)

const (
	dockerVersion      = "29.8.0"
	dockerScriptCommit = "2b32480025b223ebfddae9a3a8bef09027680f53"
	dockerScriptSum    = "fefa50ccd50efb42f438b506fc3a88574118f314aaf2a7cd5b6e1ffb1bffcf26"
	dockerSource       = "https://raw.githubusercontent.com/docker/docker-install/" + dockerScriptCommit + "/install.sh"
)

const engineInstallTries = 3

type hold struct {
	counter string
	base    int
	ceiling int
	spread  int
}

var (
	engineInstallHold = hold{counter: "tries", base: 15, ceiling: 60, spread: 15}
	pullHold          = hold{counter: "at", base: 2, ceiling: 30, spread: 5}
)

func (h hold) start() string { return "backoff=" + strconv.Itoa(h.base) + "\n" }

func (h hold) again() string {
	return `jitter=$(awk -v seed=$$ -v n="$` + h.counter + `" -v spread=` + strconv.Itoa(h.spread) + ` 'BEGIN{srand(seed+n);print int(rand()*spread)}' 2>/dev/null || echo 0)
[ -n "$jitter" ] || jitter=0
sleep $((backoff + jitter))
backoff=$((backoff * 2))
if [ "$backoff" -gt ` + strconv.Itoa(h.ceiling) + ` ]; then backoff=` + strconv.Itoa(h.ceiling) + `; fi
`
}

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
		Note:    "docker " + dockerVersion + ", installed once; upgrading it is yours from then on",
	}
}

func adoptedEngine(version string) string {
	return "docker " + version + ", not managed by ocel: upgrading it is yours"
}

func unitItem() Item {
	return Item{
		Kind:    KindUnit,
		Name:    dockerUnit,
		Owner:   rootOwner,
		Content: []byte(unitFacts),
		Slow:    true,
	}
}

func keptEngine() removal {
	return removal{
		kind:   KindEngine,
		path:   dockerEngine,
		action: providerkit.ActionKeep,
		reason: "docker and its containers stay",
	}
}

func engineCommand() string {
	return `set -e
script=$(mktemp)
trap 'rm -f "$script"' EXIT
if command -v curl >/dev/null 2>&1; then curl -fsSL --retry 5 --retry-delay 2 ` + dockerSource + ` -o "$script"
elif command -v wget >/dev/null 2>&1; then wget -qO "$script" ` + dockerSource + `
else echo 'this host has neither curl nor wget' >&2; exit 1
fi
if ! printf '%s  %s\n' ` + dockerScriptSum + ` "$script" | sha256sum -c - >/dev/null 2>&1; then
echo '` + dockerSource + ` does not match the pinned sha256 ` + dockerScriptSum + `' >&2
exit 1
fi
tries=0
` + engineInstallHold.start() + `while :; do
if VERSION=` + dockerVersion + ` sh "$script"; then break; fi
tries=$((tries + 1))
if [ "$tries" -ge ` + strconv.Itoa(engineInstallTries) + ` ]; then
echo '` + dockerSource + ` failed ` + strconv.Itoa(engineInstallTries) + ` times' >&2
exit 1
fi
` + engineInstallHold.again() + `done`
}

func unitCommand(i Item) string {
	name := quoted(i.Name)
	if len(i.Watch) == 0 {
		return "systemctl enable --now " + name
	}
	return "set -e\nsystemctl daemon-reload\nsystemctl enable " + name + "\nsystemctl restart " + name
}

const kindEngineHeld = "probe:engine"

const (
	engineStandard = "standard"
	engineMasked   = "masked"
	engineSnap     = "snap"
	engineRootless = "rootless"
	engineUnserved = unservedFact
)

type Engine struct {
	Kind    string
	Version string
}

func engineProbe() string {
	return `held=''
case "$(systemctl is-enabled ` + quoted(dockerUnit) + ` 2>/dev/null)" in masked*) held=` + engineMasked + ` ;; esac
version=''
if command -v ` + quoted(dockerEngine) + ` >/dev/null 2>&1; then
if systemctl cat ` + quoted(dockerUnit) + ` >/dev/null 2>&1; then engine=present
elif ` + systemdAnswers + `; then engine=` + quoted(unservedFact) + `
else engine=''
` + unreadable(KindEngine, quoted(dockerEngine), `'systemctl did not answer: check systemctl status (ocel needs systemd)'`) + `
fi
if [ -n "$engine" ]; then
` + reports(quoted(KindEngine), quoted(dockerEngine), "0", quoted(rootOwner),
		`"$(printf 'engine=%s\n' "$engine" | sha256sum | cut -d' ' -f1)"`) + `
if [ -n "$held" ]; then :
elif [ "$engine" = present ]; then held=` + engineStandard + `
elif snap list ` + quoted(dockerEngine) + ` >/dev/null 2>&1; then held=` + engineSnap + `
elif pgrep -x rootlesskit >/dev/null 2>&1 || command -v dockerd-rootless.sh >/dev/null 2>&1; then held=` + engineRootless + `
else held=` + engineUnserved + `
fi
version=$(timeout 10 docker version --format '{{.Server.Version}}' 2>/dev/null) || version=''
[ -n "$version" ] || version=$(dockerd --version 2>/dev/null) || version=''
fi
fi
if [ -n "$held" ]; then
` + reports(quoted(kindEngineHeld), quoted(dockerEngine), "0", `"$held"`, `"$version"`) + `
fi`
}

const (
	engineFloorMajor = 28
	engineFloorMinor = 0
)

var engineFloor = strconv.Itoa(engineFloorMajor) + "." + strconv.Itoa(engineFloorMinor)

func (r Reading) runnableEngine(named string) error {
	command := providerkit.BootstrapCommand(r.Class)
	held := r.Engine
	switch held.Kind {
	case "":
		return nil
	case engineSnap:
		return providerkit.Refuse(providerkit.CodeNotReady,
			"docker on %s is the snap package, which ocel does not run on\n"+
				"Replace it with docker %s or later from docker's own packages; its containers do not carry over",
			named, engineFloor)
	case engineRootless:
		return providerkit.Refuse(providerkit.CodeNotReady,
			"docker on %s runs rootless, and ocel needs the system daemon behind %s\n"+
				"Install docker %s or later as the system daemon and run `%s`",
			named, dockerUnit, engineFloor, command)
	case engineUnserved:
		return providerkit.Refuse(providerkit.CodeNotReady,
			"docker on %s is not run by %s, the system daemon ocel needs\n"+
				"Install docker %s or later as the system daemon and run `%s`",
			named, dockerUnit, engineFloor, command)
	}
	major, minor, read := held.release()
	switch {
	case !read:
		return providerkit.Refuse(providerkit.CodeNotReady,
			"docker on %s reports no version ocel can read\n"+
				"Check that `docker version` answers as root and run `%s`",
			named, command)
	case major < engineFloorMajor || (major == engineFloorMajor && minor < engineFloorMinor):
		return providerkit.Refuse(providerkit.CodeNotReady,
			"docker %s on %s is older than %s, the oldest ocel runs on\n"+
				"Upgrade docker to %s or later and run `%s`",
			held.Version, named, engineFloor, engineFloor, command)
	}
	return nil
}

func (e Engine) release() (int, int, bool) {
	majorSaid, rest, split := strings.Cut(e.Version, ".")
	if !split {
		return 0, 0, false
	}
	minorSaid := rest
	if end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' }); end >= 0 {
		minorSaid = rest[:end]
	}
	major, err := strconv.Atoi(majorSaid)
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(minorSaid)
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func readEngine(rendered string) Engine {
	for line := range strings.Lines(rendered) {
		columns := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
		if len(columns) == 5 && columns[0] == kindEngineHeld {
			return Engine{Kind: columns[3], Version: engineVersion(columns[4])}
		}
	}
	return Engine{}
}

func engineVersion(said string) string {
	said = strings.TrimSpace(said)
	if rest, told := strings.CutPrefix(said, "Docker version "); told {
		said, _, _ = strings.Cut(rest, ",")
	}
	return said
}

const systemdAnswers = "systemctl list-unit-files >/dev/null 2>&1"

func unitProbe(i Item) string {
	name := quoted(i.Name)
	watching := ""
	for _, path := range i.Watch {
		watching += "printf 'watch=%s\\n' \"$(sha256sum " + quoted(path) + " 2>/dev/null | cut -d' ' -f1)\"\n"
	}
	return `if systemctl cat ` + name + ` >/dev/null 2>&1; then
active=$(systemctl is-active ` + name + ` 2>/dev/null || true)
enabled=$(systemctl is-enabled ` + name + ` 2>/dev/null || true)
facts=$( printf 'active=%s\nenabled=%s\n' "$active" "$enabled"
` + watching + `)
` + reports(quoted(KindUnit), name, "0", quoted(rootOwner), `"$(printf '%s\n' "$facts" | sha256sum | cut -d' ' -f1)"`) + `
elif ! ` + systemdAnswers + `; then
` + unreadable(KindUnit, name, `'systemctl did not answer'`) + `
fi`
}

func unitWatchFacts(watched ...[]byte) []byte {
	facts := unitFacts
	for _, content := range watched {
		facts += "watch=" + contentSum(content) + "\n"
	}
	return []byte(facts)
}
