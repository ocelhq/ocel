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
			Detail: "root on this machine under another name wherever a daemon listens on the " + held.group + " socket: anything in the group can start a container that mounts / and writes anywhere, so a deploy login that can talk to that socket is a login that can become root. Ocel takes it because a deploy is containers and there is no smaller grant that runs them, and it creates the group where the host carries none, so a host that gains a daemon later hands the login that same root",
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
