package host

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	dockerEngine = "docker"
	dockerDaemon = "dockerd"
	dockerUnit   = "docker.service"
)

const (
	dockerVersion      = "29.8.0"
	dockerScriptCommit = "2b32480025b223ebfddae9a3a8bef09027680f53"
	dockerScriptSum    = "fefa50ccd50efb42f438b506fc3a88574118f314aaf2a7cd5b6e1ffb1bffcf26"
	dockerSource       = "https://raw.githubusercontent.com/docker/docker-install/" + dockerScriptCommit + "/install.sh"
)

const engineInstallTries = 3

var engineInstallBackoff = shell.Backoff{Base: 15, Ceiling: 60, Spread: 15}

func EngineItems() []machine.Item {
	return []machine.Item{engineItem(), unitItem(), liveRestoreItem()}
}

func engineItem() machine.Item {
	return machine.Item{
		Kind:    machine.KindEngine,
		Name:    dockerEngine,
		Script:  func(machine.Item) string { return engineCommand() },
		Survey:  func(machine.Item) string { return engineProbe() },
		Owner:   machine.RootOwner,
		Content: []byte(machine.EngineFact),
		Slow:    true,
		Note:    "docker " + dockerVersion + ", installed once; upgrading it is yours from then on",
	}
}

func adoptedEngine(version string) string {
	return "docker " + version + ", not managed by ocel: upgrading it is yours"
}

func unitItem() machine.Item {
	return machine.SystemdUnit(machine.Item{
		Name:    dockerUnit,
		Owner:   machine.RootOwner,
		Content: []byte(machine.UnitFacts),
		Slow:    true,
	})
}

func keptEngine() machine.Removal {
	return machine.Removal{
		Kind:   machine.KindEngine,
		Path:   dockerEngine,
		Action: provider.ActionKeep,
		Reason: "docker and its containers stay",
	}
}

const (
	aptWaitSeconds    = 30
	dpkgLockSeconds   = 300
	fetchWaitSeconds  = 30
	fetchTotalSeconds = 120
	engineTailLines   = 3
)

func engineCommand() string {
	apt := strconv.Itoa(aptWaitSeconds)
	return `set -e
script=$(mktemp)
log=$(mktemp)
apt=$(mktemp)
CURL_HOME=$(mktemp -d)
export CURL_HOME
trap 'rm -rf "$script" "$log" "$apt" "$CURL_HOME"' EXIT
printf '%s\n' 'connect-timeout = ` + strconv.Itoa(fetchWaitSeconds) + `' 'max-time = ` + strconv.Itoa(fetchTotalSeconds) + `' >"$CURL_HOME/.curlrc"
if command -v curl >/dev/null 2>&1; then curl -fsSL --retry 5 --retry-delay 2 ` + dockerSource + ` -o "$script"
elif command -v wget >/dev/null 2>&1; then wget -qO "$script" ` + dockerSource + `
else echo 'this host has neither curl nor wget' >&2; exit 1
fi
if ! printf '%s  %s\n' ` + dockerScriptSum + ` "$script" | sha256sum -c - >/dev/null 2>&1; then
echo '` + dockerSource + ` does not match the pinned sha256 ` + dockerScriptSum + `' >&2
exit 1
fi
printf '%s\n' 'Acquire::http::Timeout "` + apt + `";' 'Acquire::https::Timeout "` + apt + `";' 'DPkg::Lock::Timeout "` + strconv.Itoa(dpkgLockSeconds) + `";' >"$apt"
if APT_CONFIG="$apt" VERSION=` + dockerVersion + ` sh "$script" >"$log" 2>&1; then exit 0; fi
echo "` + dockerSource + ` failed; its last lines:" >&2
tail -n ` + strconv.Itoa(engineTailLines) + ` "$log" >&2
exit 1`
}

func (h *Host) installEngine(ctx context.Context, progress progress.Log) error {
	elevation, err := ssh.Root, h.Assume(ctx, ssh.Root)
	if err != nil {
		return err
	}
	for try := 1; ; try++ {
		result, err := h.Stream(ctx, elevation, engineCommand(), nil)
		if err != nil || result.Code == 0 {
			return err
		}
		attempt := fmt.Sprintf("try %d of %d", try, engineInstallTries)
		if try == engineInstallTries || ssh.IsSudoRefused(result) {
			return h.Refuse("install docker, "+attempt+",", result)
		}
		progress.Warn(fmt.Sprintf("Docker's install on %s failed on try %d of %d and runs again",
			h.Named(), try, engineInstallTries))
		for _, line := range strings.Split(ssh.Reason(result), "\n") {
			progress.Detail(line)
		}
		if err := h.Wait(ctx, engineInstallBackoff.After(try)); err != nil {
			return err
		}
	}
}

type engineKind string

const (
	engineStandard engineKind = "standard"
	engineMasked   engineKind = "masked"
	engineSnap     engineKind = "snap"
	engineRootless engineKind = "rootless"
	engineUnserved engineKind = "unserved"
)

type InstalledEngine struct {
	Kind    engineKind
	Version string
}

func engineProbe() string {
	return `kind=''
case "$(systemctl is-enabled ` + shell.Quote(dockerUnit) + ` 2>/dev/null)" in masked*) kind=` + string(engineMasked) + ` ;; esac
version=''
if command -v ` + shell.Quote(dockerEngine) + ` >/dev/null 2>&1; then
if systemctl cat ` + shell.Quote(dockerUnit) + ` >/dev/null 2>&1; then engine=present
elif ` + machine.SystemdAnswers + `; then engine=` + shell.Quote(string(engineUnserved)) + `
else engine=''
` + machine.Unreadable(machine.KindEngine, shell.Quote(dockerEngine), `'systemctl did not answer: check systemctl status (ocel needs systemd)'`) + `
fi
if [ -n "$engine" ]; then
` + machine.Reports(shell.Quote(machine.KindEngine), shell.Quote(dockerEngine), "0", shell.Quote(machine.RootOwner),
		`"$(printf 'engine=%s\n' "$engine" | sha256sum | cut -d' ' -f1)"`) + `
if [ -n "$kind" ]; then :
elif [ "$engine" = present ]; then kind=` + string(engineStandard) + `
elif snap list ` + shell.Quote(dockerEngine) + ` >/dev/null 2>&1; then kind=` + string(engineSnap) + `
elif pgrep -x rootlesskit >/dev/null 2>&1; then kind=` + string(engineRootless) + `
elif pgrep -x ` + shell.Quote(dockerDaemon) + ` >/dev/null 2>&1; then kind=` + string(engineUnserved) + `
fi
version=$(timeout 10 ` + shell.Quote(dockerEngine) + ` version --format '{{.Server.Version}}' 2>/dev/null) || version=''
[ -n "$version" ] || version=$(` + shell.Quote(dockerDaemon) + ` --version 2>/dev/null) || version=''
fi
fi
if [ -n "$kind" ]; then
` + machine.Reports(shell.Quote(machine.EngineFactsRow), shell.Quote(dockerEngine), "0", `"$kind"`, `"$version"`) + `
fi`
}

const (
	engineFloorMajor = 28
	engineFloorMinor = 0
)

var engineFloor = strconv.Itoa(engineFloorMajor) + "." + strconv.Itoa(engineFloorMinor)

func (r Reading) runnableEngine(named string) error {
	command := provider.BootstrapCommand(r.Tier)
	current := r.Engine
	switch current.Kind {
	case "":
		return nil
	case engineMasked:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s on %s is masked\n"+
				"Run `systemctl unmask %s` and run `%s`",
			dockerUnit, named, dockerUnit, command)
	case engineSnap:
		return refusal.Refuse(refusal.CodeNotReady,
			"docker on %s is the snap package, which ocel does not run on\n"+
				"Replace it with docker %s or later from docker's own packages; its containers do not move over",
			named, engineFloor)
	case engineRootless:
		return refusal.Refuse(refusal.CodeNotReady,
			"docker on %s runs rootless, and ocel needs the system daemon behind %s\n"+
				"Install docker %s or later as the system daemon and run `%s`",
			named, dockerUnit, engineFloor, command)
	case engineUnserved:
		return refusal.Refuse(refusal.CodeNotReady,
			"a docker daemon on %s runs outside %s, the system daemon ocel needs\n"+
				"Install docker %s or later as the system daemon and run `%s`",
			named, dockerUnit, engineFloor, command)
	}
	major, minor, read := current.release()
	switch {
	case !read:
		return refusal.Refuse(refusal.CodeNotReady,
			"docker on %s reports no version ocel can read\n"+
				"Check that `docker version` answers as root and run `%s`",
			named, command)
	case major < engineFloorMajor || (major == engineFloorMajor && minor < engineFloorMinor):
		return refusal.Refuse(refusal.CodeNotReady,
			"docker %s on %s is older than %s, the oldest ocel runs on\n"+
				"Upgrade docker to %s or later and run `%s`",
			current.Version, named, engineFloor, engineFloor, command)
	}
	return nil
}

func (e InstalledEngine) release() (int, int, bool) {
	var major, minor int
	read, err := fmt.Sscanf(e.Version, "%d.%d", &major, &minor)
	return major, minor, err == nil && read == 2
}

func readEngine(rendered string) InstalledEngine {
	for line := range strings.Lines(rendered) {
		columns := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
		if len(columns) == 5 && columns[0] == machine.EngineFactsRow {
			return InstalledEngine{Kind: engineKind(columns[3]), Version: engineVersion(columns[4])}
		}
	}
	return InstalledEngine{}
}

func engineVersion(said string) string {
	said = strings.TrimSpace(said)
	if rest, told := strings.CutPrefix(said, "Docker version "); told {
		said, _, _ = strings.Cut(rest, ",")
	}
	return said
}
