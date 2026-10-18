package component

import "github.com/ocelhq/ocel/pkg/edge"

func GuardOrigin(env map[string]string, secret, previous string) {
	env[edge.OriginFrontedVar] = "1"
	env[edge.OriginSecretVar] = secret
	if previous != "" {
		env[edge.OriginSecretPreviousVar] = previous
	}
}
