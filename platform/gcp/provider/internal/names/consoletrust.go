package names

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	consoleWord     = "console"
	deployWord      = "deploy"
	deployRoleWord  = "deploy_"
	deployRoleChars = 8
	targetChars     = 24
)

func (n Names) ConsolePool() string {
	return naming.MustUntieredOcelName(string(n.namespace), consoleWord)
}

func (n Names) ConsolePoolPath() string {
	return "projects/" + n.project + "/locations/global/workloadIdentityPools/" + n.ConsolePool()
}

func (n Names) ConsoleProvider(target string) string {
	return consoleWord + naming.WordSeparator + naming.HashDigits(targetChars, target)
}

func (n Names) DeployAccount(tier environment.Tier) string {
	return n.ocelName(tier, deployWord)
}

func (n Names) DeployAccountEmail(tier environment.Tier) string {
	return n.DeployAccount(tier) + "@" + n.project + AccountDomain
}

func (n Names) DeployRole(permissions []string) string {
	return customRoleID(n.namespace, deployRoleWord+naming.HashDigits(deployRoleChars, permissions...))
}

func (n Names) DeployRolePath(permissions []string) string {
	return "projects/" + n.project + "/roles/" + n.DeployRole(permissions)
}
