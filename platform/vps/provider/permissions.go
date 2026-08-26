package vps

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const anyLogin = "<login>"

func bootstrapDocument(login string) string {
	var written strings.Builder
	written.WriteString("The login ocel bootstraps a host with needs:\n")
	for _, need := range session.Requirements() {
		written.WriteString("\n  " + need.Name + "\n    " + need.Detail + "\n")
	}
	written.WriteString("\nSudo without a password is a file of its own, written with `visudo -f /etc/sudoers.d/ocel`:\n\n")
	written.WriteString("  " + login + " ALL=(ALL) NOPASSWD: ALL\n")
	written.WriteString("\nThat is root, spelled out. A bootstrap creates accounts, installs under /etc, /var/lib\n")
	written.WriteString("and /usr/local, and locks a password; nothing narrower covers it. Ocel keeps the grant to\n")
	written.WriteString("the login that bootstraps: what deploys run as is the smaller set `ocel permissions deploy`\n")
	written.WriteString("prints, and it never holds this one.\n")
	return written.String()
}

func deployDocument() string {
	var written strings.Builder
	written.WriteString("A bootstrapped host runs every deploy as " + host.DeployUser() + ", which holds:\n")
	for _, grant := range deployGrants() {
		written.WriteString("\n  " + grant.Name + "\n    " + grant.Detail + "\n")
	}
	written.WriteString("\nThe login is the host's, not ocel's: it answers to the keys in its authorized_keys and to\n")
	written.WriteString("nothing else, and `ocel destroy` takes it with the last class it was created for.\n")
	return written.String()
}

func deployGrants() []host.Grant {
	var grants []host.Grant
	named := map[string]bool{}
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		for _, grant := range host.Grants(class) {
			if named[grant.Name] {
				continue
			}
			named[grant.Name] = true
			grants = append(grants, grant)
		}
	}
	return grants
}
