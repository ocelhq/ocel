package machine

import (
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

const (
	ReasonCurrent = "already current"
	BootstrapDocs = "https://ocel.dev/docs/providers/vps#bootstrap"
	BaseSummary   = "the directories, files, services and engine the box runs every deploy of this tier with"
)

const DirNonEmpty = "dir=nonempty"

type Removal struct {
	Kind    string
	Path    string
	Reason  string
	Action  provider.ChangeAction
	Shared  bool
	Reload  string
	Origins string
}

func (r Removal) Phrase() string { return Phrase(r.Kind, r.Path) }

func (r Removal) Kept() string {
	if r.Reason == "" {
		return "Kept " + r.Phrase()
	}
	return "Kept " + r.Phrase() + ": " + r.Reason
}

func Taking(kind, path, reason string) Removal {
	return Removal{Kind: kind, Path: path, Reason: reason, Action: provider.ActionDelete}
}

func Sharing(path, reason string) Removal {
	return Removal{Kind: KindDir, Path: path, Reason: reason, Action: provider.ActionDelete, Shared: true}
}

func (r Removal) Command() string {
	switch {
	case r.Kind == KindContainer:
		return "docker rm --force " + shell.Quote(r.Path)
	case r.Kind == KindUnit:
		return UnitRemoval(r.Path)
	case r.Kind == KindApps:
		return "docker ps --all --quiet --filter " + shell.Quote("label="+r.Path) + " | xargs -r docker rm --force >/dev/null"
	case r.Kind == KindResourceVolumes:
		return "docker volume ls --quiet --filter " + shell.Quote("label="+r.Path) + " | xargs -r docker volume rm >/dev/null"
	case r.Kind == KindAppNetworks:
		return "for net in $(docker network ls --quiet --filter " + shell.Quote("label="+r.Path) + "); do\n" +
			"docker network disconnect --force \"$net\" " + shell.Quote(boxapi.Switchboard) + " >/dev/null 2>&1 || true\n" +
			"docker network rm \"$net\" >/dev/null\n" +
			"done"
	case r.Kind == KindNetwork:
		return "if ! docker network rm " + shell.Quote(r.Path) + " >/dev/null 2>&1 && " +
			"docker network inspect " + shell.Quote(r.Path) + " >/dev/null 2>&1; then printf '%s\\n' " + shell.Quote(docker.NetworkInUse) + "; fi"
	case r.Kind == KindPlaced:
		unplaced := shell.Words(routing.Placement(filepath.Dir(r.Path), "unplace", r.Path)) + " 2>/dev/null || rm -f " + shell.Quote(r.Path) + "\n"
		if r.Reload != "" {
			unplaced += r.Reload + "\n"
		}
		placed := "if [ -e " + shell.Quote(r.Path) + " ] || [ -L " + shell.Quote(r.Path) + " ]; then\n" + unplaced + "fi"
		if r.Origins == "" {
			return placed
		}
		return placed + "\n" + shell.Words(routing.Placement(r.Origins, "unplace-origins")) + " 2>/dev/null || rm -f " + shell.Quote(r.Origins) + "/" + boxapi.OriginPrefix + "*.pem"
	case r.Kind == KindRoutingTable || r.Kind == KindProxyConfig:
		return routing.Locked("-x") + "rm -f " + shell.Quote(r.Path)
	case r.Shared:
		return "rmdir " + shell.Quote(r.Path) + " 2>/dev/null || printf '%s\\n' " + shell.Quote(DirNonEmpty)
	default:
		return "rm -rf " + shell.Quote(r.Path)
	}
}

func UnitRemoval(name string) string {
	unit := shell.Quote(name)
	return "if systemctl cat " + unit + " >/dev/null 2>&1; then systemctl disable --now " + unit + " >/dev/null 2>&1 || true; fi"
}
