package host

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Grant struct {
	Name   string
	Detail string
}

func Grants(tier environment.Tier, fronting front.Proxy) []Grant {
	return grants(tier, ssh.AMD64, fronting)
}

func grants(tier environment.Tier, arch string, fronting front.Proxy) []Grant {
	items := Items(tier, nil, arch, fronting)
	deployer := deployLogin()

	var grants []Grant
	if keys := written(items, machine.KindFile, authorizedKeys); keys.Name != "" {
		grants = append(grants, Grant{
			Name:   "an ssh login as " + deployer.name,
			Detail: "a " + deployer.shell + " shell and the keys in " + keys.Name + ". No password: locked with `usermod -p '" + lockedPassword + "'`",
		})
	}
	if line := written(items, machine.KindFile, sudoersDocker); line.Name != "" {
		grants = append(grants, Grant{
			Name: "runs docker only through " + LiveBinary + " " + boxapi.DockerCommand + ", as root, through one line in " + line.Name,
			Detail: "the line is\n\n      " + strings.TrimSpace(string(line.Content)) +
				"\n\n    and " + deployer.name + " is in no group that reaches the docker socket, which docker documents as root-level (https://docs.docker.com/engine/install/linux-postinstall/). " +
				"The " + DockerShim + " on its PATH hands every docker command to that check, which refuses what reaches the host: privileged or device access, a host namespace, a capability past the ones ocel's containers use, a published port beyond loopback, " +
				"a mount of anything but the directories " + boxapi.DockerGrantsPath + " lists under a path only root can change, a network not ocel's or granted, exec into a container ocel does not run, and any file it reads or writes outside " + boxapi.StateRoot + ". " +
				"An adopted proxy whose container mounts the docker socket reads the config this login writes for it, and through that config can reach the socket. " +
				"Bootstrap uses a docker " + engineFloor + " or later it finds, or installs the daemon from " + dockerSource + "; destroy removes the login and leaves the engine and its containers",
		})
	}
	if agent := written(items, machine.KindFile, LiveBinary); agent.Name != "" {
		grants = append(grants, Grant{
			Name:   "runs " + agent.Name + " " + boxapi.RecordsCommand,
			Detail: fmt.Sprintf("root-owned at %04o, run without sudo; %s runs it to read and compare-and-set each tier's entries under %s, which it owns, and cannot write the binary", agent.Mode, deployer.name, boxapi.StateRoot),
		})
	}
	if helper := written(items, machine.KindFile, boxapi.ReleasesHelper); helper.Name != "" {
		grants = append(grants, Grant{
			Name:   "runs " + helper.Name,
			Detail: fmt.Sprintf("root-owned at %04o, run without sudo; %s runs it to record releases, archive a retired release's image under %s and load it back for a rollback, and remove the images and archives no release or running container names. It never forces or prunes", helper.Mode, deployer.name, boxapi.ReleasesDir),
		})
	}
	if board := written(items, machine.KindFile, boxapi.SwitchboardBinary); board.Name != "" {
		grants = append(grants, Grant{
			Name: "runs " + board.Name,
			Detail: fmt.Sprintf("root-owned at %04o, run without sudo; %s runs it on the box to read the certificate and the edge the box answers with on its own :443, and cannot write it. "+
				"The same binary runs as the %s container that routes every hostname, reached over a socket inside that container through docker",
				board.Mode, deployer.name, boxapi.Switchboard),
		})
	}
	grants = append(grants, sealing(items, tier, deployer)...)
	if line := written(items, machine.KindFile, sudoersPins); line.Name != "" {
		grants = append(grants, Grant{
			Name: "runs " + LiveBinary + " " + boxapi.PinCommand + " as root, through one line in " + line.Name,
			Detail: "the line is\n\n      " + strings.TrimSpace(string(line.Content)) +
				"\n\n    which takes one name directly under " + boxapi.PinsDir + " and prints the certificate blocks pinned there and whether the key beside them matches. It never prints the key, and " +
				boxapi.PinsDir + " stays root's alone",
		})
	}
	if line := written(items, machine.KindFile, machine.SudoersCaddyReload); line.Name != "" {
		grants = append(grants, Grant{
			Name: "reloads your Caddy as root, through one line in " + line.Name,
			Detail: "the line is\n\n      " + strings.TrimSpace(string(line.Content)) +
				"\n\n    which runs the unit's own `caddy reload`, in Caddy's environment. Bootstrap writes it only for a Caddy that runs as caddy.service, and `ocel bootstrap destroy` removes it",
		})
	}
	if agent := written(items, machine.KindFile, LiveBinary); agent.Name != "" {
		grants = append(grants, Grant{
			Name: "no hand in " + LiveSocket,
			Detail: "root's agent at " + agent.Name + ", run by systemd as " + LiveService + ", not by " + deployer.name +
				". App containers get " + boxapi.SocketDir + " read-only; the agent identifies them by connecting process, " +
				"opens values under the tier key, and answers no process outside a container. " +
				"It also runs each environment's topics and tasks against that environment's queue database, and delivers them to its worker containers",
		})
	}
	if unit := written(items, machine.KindUnit, EnvSourceSyncService(tier)); unit.Name != "" {
		grants = append(grants, Grant{
			Name: "no hand in " + unit.Name,
			Detail: "root's env source sync, " + LiveBinary + " " + boxapi.EnvSourceSyncCommand + ", run by systemd, not by " + deployer.name +
				". Bounded to CAP_CHOWN and CAP_DAC_OVERRIDE on a read-only system, it writes " + KeyValuesDir(tier) +
				" and nothing beside, seals in process under the tier key, and logs in to each env source with the credential " +
				string(tier) + " stores in ocel's own values",
		})
	}
	for _, item := range items {
		if item.Owner != deployer.name || item.Kind == machine.KindUser {
			continue
		}
		grants = append(grants, Grant{
			Name:   "owns " + item.Name,
			Detail: fmt.Sprintf("written at %04o to %s; only root reads it besides", item.Mode, deployer.name),
		})
	}
	return grants
}

func sealing(items []machine.Item, tier environment.Tier, deployer login) []Grant {
	fragment := written(items, machine.KindFile, sudoersSeal(tier))
	key := written(items, machine.KindSealKey, SealKeyPath(tier))
	if fragment.Name == "" || key.Name == "" {
		return nil
	}
	return []Grant{{
		Name: "runs " + LiveBinary + " " + boxapi.SealCommand + " as root, through one line in " + fragment.Name,
		Detail: "the line is\n\n      " + strings.TrimSpace(string(fragment.Content)) +
			"\n\n    which seals and opens values under the " + string(tier) +
			" key only; it creates no key, reaches no other tier, and never prints the key",
	}, {
		Name: "no read of " + key.Name,
		Detail: fmt.Sprintf("root-owned at %04o, created on this machine and never leaves it; %s uses it through ocel-live seal and cannot read the %s key. It is never rotated; `ocel bootstrap destroy %s` removes it with the tier",
			key.Mode, deployer.name, machine.SealAlgorithm, tier),
	}}
}

func written(items []machine.Item, kind, name string) machine.Item {
	for _, item := range items {
		if item.Kind == kind && item.Name == name {
			return item
		}
	}
	return machine.Item{}
}
