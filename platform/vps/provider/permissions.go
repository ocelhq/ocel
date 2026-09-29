package vps

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
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

func deployDocument(front host.Front) string {
	var written strings.Builder
	written.WriteString("A bootstrapped host runs every deploy as " + host.DeployUser() + ", which is granted:\n")
	for _, grant := range deployGrants(front) {
		written.WriteString("\n  " + grant.Name + "\n    " + grant.Detail + "\n")
	}
	written.WriteString("\nIt accepts only the keys in its authorized_keys; `ocel destroy` removes it.\n")
	return written.String()
}

func deployGrants(front host.Front) []host.Grant {
	var grants []host.Grant
	named := map[string]bool{}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, grant := range host.Grants(tier, front) {
			if named[grant.Name] {
				continue
			}
			named[grant.Name] = true
			grants = append(grants, grant)
		}
	}
	return grants
}
