package vps

import (
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	written.WriteString("\nDeploys run with the smaller set `ocel permissions deploy` prints.\n")
	return written.String()
}

func deployDocument() string {
	var written strings.Builder
	written.WriteString("A bootstrapped host runs every deploy as " + host.DeployUser() + ", which is granted:\n")
	for _, grant := range deployGrants() {
		written.WriteString("\n  " + grant.Name + "\n    " + grant.Detail + "\n")
	}
	written.WriteString("\nIt accepts only the keys in its authorized_keys; `ocel destroy` removes it.\n")
	return written.String()
}

func deployGrants() []host.Grant {
	var grants []host.Grant
	named := map[string]bool{}
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
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
