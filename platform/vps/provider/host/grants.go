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

func Grants(class providerkit.Class) []Grant {
	items := Items(class, nil)
	held := deployLogin()

	var grants []Grant
	if keys := written(items, authorizedKeys); keys.Kind == KindFile {
		grants = append(grants, Grant{
			Name:   "an ssh login as " + held.name,
			Detail: "a " + held.shell + " shell and the keys in " + keys.Name + ", nothing else. The account holds no password at all: it is locked with `usermod -p '" + lockedPassword + "'`, which sshd still lets past on a key, and no password will ever authenticate it",
		})
	}
	if held.group != "" {
		grants = append(grants, Grant{
			Name:   "membership of the " + held.group + " group",
			Detail: "root on this machine under another name wherever a daemon listens on the " + held.group + " socket: anything in the group can start a container that mounts / and writes anywhere, so a deploy login that can talk to that socket is a login that can become root. Ocel takes it because a deploy is containers and there is no smaller grant that runs them, and " + daemonBehind(items),
		})
	}
	if !under(items, sudoersRoot) {
		grants = append(grants, Grant{
			Name:   "no line in any sudoers file",
			Detail: "a bootstrap writes " + held.name + " nothing under " + sudoersRoot + ", and every root-owned path below stands as root's",
		})
	}
	if helper := written(items, recordsHelper); helper.Kind == KindFile {
		grants = append(grants, Grant{
			Name:   "runs " + helper.Name,
			Detail: fmt.Sprintf("root's own file at %04o: %s executes it to compare-and-set records under its own tier, and cannot write the helper itself", helper.Mode, held.name),
		})
	}
	grants = append(grants, sealing(items, class, held)...)
	for _, item := range items {
		if item.Owner != held.name || item.Kind == KindUser {
			continue
		}
		grants = append(grants, Grant{
			Name:   "owns " + item.Name,
			Detail: fmt.Sprintf("written at %04o to %s, and nothing but root reads it beside", item.Mode, held.name),
		})
	}
	return grants
}

func sealing(items []Item, class providerkit.Class, held login) []Grant {
	fragment := written(items, sudoersSeal)
	key := written(items, SealKeyPath(class))
	if fragment.Kind != KindFile || key.Kind != KindSealKey {
		return nil
	}
	return []Grant{{
		Name: "runs " + SealHelper + " as root, through one line in " + fragment.Name,
		Detail: "the line is\n\n      " + strings.TrimSpace(string(fragment.Content)) +
			"\n\n    and it is the whole of what sudo will let " + held.name +
			" do. The helper seals and opens a value at a coordinate it is given; it never prints the key, and no other command on this host runs under sudo for " + held.name,
	}, {
		Name: "no read of " + key.Name,
		Detail: fmt.Sprintf("root's own at %04o, minted from this machine's own randomness and never off it: %s can ask the helper to seal and to open, and cannot read the %s key either does it with. A key is minted once per class and ocel rotates it for nobody — what it sealed, it alone opens, and `ocel destroy` takes it with the class",
			key.Mode, held.name, SealAlgorithm),
	}}
}

func daemonBehind(items []Item) string {
	if written(items, dockerEngine).Kind != KindEngine {
		return "it creates the group where the host carries none, so a host that gains a daemon later hands the login that same root"
	}
	return "the daemon behind that socket is the one bootstrap installs, from the script at " + dockerSource + " run as root, and left serving. A destroy takes the login and leaves the engine and every container on it standing"
}

func written(items []Item, name string) Item {
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	return Item{}
}

func under(items []Item, root string) bool {
	for _, item := range items {
		if item.Name == root || strings.HasPrefix(item.Name, root+"/") {
			return true
		}
	}
	return false
}
