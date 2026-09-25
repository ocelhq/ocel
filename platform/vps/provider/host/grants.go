package host

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Grant struct {
	Name   string
	Detail string
}

func Grants(class providerkit.Class) []Grant { return grants(class, ArchAMD64) }

func grants(class providerkit.Class, arch string) []Grant {
	items := Items(class, nil, arch)
	held := deployLogin()

	var grants []Grant
	if keys := written(items, KindFile, authorizedKeys); keys.Name != "" {
		grants = append(grants, Grant{
			Name:   "an ssh login as " + held.name,
			Detail: "a " + held.shell + " shell and the keys in " + keys.Name + ". No password: locked with `usermod -p '" + lockedPassword + "'`",
		})
	}
	if held.group != "" {
		grants = append(grants, Grant{
			Name:   "membership of the " + held.group + " group",
			Detail: "equivalent to root: the group can start a container that mounts /, so this login can become root. No smaller grant runs containers. The daemon is installed by bootstrap from " + dockerSource + "; destroy removes the login and leaves the engine and its containers",
		})
	}
	if !under(items, sudoersRoot) {
		grants = append(grants, Grant{
			Name:   "no line in any sudoers file",
			Detail: "bootstrap writes nothing for " + held.name + " under " + sudoersRoot,
		})
	}
	if helper := written(items, KindFile, recordsHelper); helper.Name != "" {
		grants = append(grants, Grant{
			Name:   "runs " + helper.Name,
			Detail: fmt.Sprintf("root-owned at %04o; %s runs it to compare-and-set its records and cannot write it", helper.Mode, held.name),
		})
	}
	if helper := written(items, KindFile, releasesHelper); helper.Name != "" {
		grants = append(grants, Grant{
			Name:   "runs " + helper.Name,
			Detail: fmt.Sprintf("root-owned at %04o, run without sudo; %s runs it to record releases and remove images no release or running container names. It never forces or prunes", helper.Mode, held.name),
		})
	}
	if board := written(items, KindFile, SwitchboardBinary); board.Name != "" {
		grants = append(grants, Grant{
			Name: "runs " + board.Name,
			Detail: fmt.Sprintf("root-owned at %04o, run without sudo; %s runs it on the box to read the certificate and the edge the box answers with on its own :443, and cannot write it. "+
				"The same binary runs as the %s container that routes every hostname, reached over a socket inside that container through docker",
				board.Mode, held.name, SwitchboardContainer),
		})
	}
	grants = append(grants, sealing(items, class, held)...)
	if agent := written(items, KindFile, LiveBinary); agent.Name != "" {
		grants = append(grants, Grant{
			Name: "no hand in " + LiveSocket,
			Detail: "root's agent at " + agent.Name + ", run by systemd as " + LiveService + ", not by " + held.name +
				". App containers get " + LiveSocketDir + " read-only; the agent identifies them by connecting process, " +
				"opens values under the class key, and answers no process outside a container",
		})
	}
	for _, item := range items {
		if item.Owner != held.name || item.Kind == KindUser {
			continue
		}
		grants = append(grants, Grant{
			Name:   "owns " + item.Name,
			Detail: fmt.Sprintf("written at %04o to %s; only root reads it besides", item.Mode, held.name),
		})
	}
	return grants
}

func sealing(items []Item, class providerkit.Class, held login) []Grant {
	fragment := written(items, KindFile, sudoersSeal(class))
	key := written(items, KindSealKey, SealKeyPath(class))
	if fragment.Name == "" || key.Name == "" {
		return nil
	}
	return []Grant{{
		Name: "runs " + SealHelper + " as root, through one line in " + fragment.Name,
		Detail: "the line is\n\n      " + strings.TrimSpace(string(fragment.Content)) +
			"\n\n    the only sudo " + held.name + " has. The helper seals and opens values under the " + string(class) +
			" key only; it mints no key, reaches no other class, and never prints the key",
	}, {
		Name: "no read of " + key.Name,
		Detail: fmt.Sprintf("root-owned at %04o, minted on this machine and never leaves it; %s uses it through the helper and cannot read the %s key. It is never rotated; `ocel destroy` removes it with the class",
			key.Mode, held.name, SealAlgorithm),
	}}
}

func written(items []Item, kind, name string) Item {
	for _, item := range items {
		if item.Kind == kind && item.Name == name {
			return item
		}
	}
	return Item{}
}

func under(items []Item, root string) bool {
	for _, item := range items {
		if beneath(root, item.Name) {
			return true
		}
	}
	return false
}
