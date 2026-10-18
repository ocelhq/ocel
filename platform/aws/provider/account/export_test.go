package account

import "github.com/ocelhq/ocel/pkg/environment"

var (
	DeployCredentialPermissions    func(Namespace, environment.Tier, string) (string, error)
	BootstrapCredentialPermissions func(Namespace, environment.Tier, string) (string, error)
)
