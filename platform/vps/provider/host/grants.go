package host

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Grant struct {
	Name   string
	Detail string
}

func Grants(class providerkit.Class) []Grant {
	grants := []Grant{
		{
			Name:   "an ssh login as " + deployUser,
			Detail: "a " + deployShell + " shell and the keys in " + authorizedKeys + ", nothing else. The account holds no password at all: it is locked with `usermod -p '*'`, which sshd still lets past on a key, and no password will ever authenticate it",
		},
		{
			Name:   "membership of the " + dockerGroup + " group",
			Detail: "root on this machine under another name. Anything in it can start a container that mounts / and writes anywhere, so a deploy login that can talk to the docker socket is a login that can become root. Ocel takes it because a deploy is containers and there is no smaller grant that runs them",
		},
		{
			Name:   "no line in any sudoers file",
			Detail: "a bootstrap writes " + deployUser + " no sudo entry, and every root-owned path below stands as root's",
		},
		{
			Name:   "runs " + recordsHelper,
			Detail: fmt.Sprintf("root's own file at %04o: %s executes it to compare-and-set records under its own tier, and cannot write the helper itself", 0o755, deployUser),
		},
	}
	for _, item := range Items(class, nil) {
		if item.Owner != deployUser || item.Kind == KindUser {
			continue
		}
		grants = append(grants, Grant{
			Name:   "owns " + item.Name,
			Detail: fmt.Sprintf("written at %04o to %s, and nothing but root reads it beside", item.Mode, deployUser),
		})
	}
	return grants
}
