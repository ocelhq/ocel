package gcloud

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func MemberOf(principal string) string {
	if strings.HasSuffix(principal, names.AccountDomain) {
		return "serviceAccount:" + principal
	}
	return "user:" + principal
}

func AccountPath(c *Clients, name string) string {
	return "projects/" + c.Project() + "/serviceAccounts/" + name + "@" + c.Project() + names.AccountDomain
}

func EmailAccountPath(email string) string {
	return "projects/-/serviceAccounts/" + email
}

func RepositoryParent(c *Clients) string {
	return fmt.Sprintf("projects/%s/locations/%s", c.Project(), c.Region)
}

func RepositoryPath(c *Clients, name string) string {
	return RepositoryParent(c) + "/repositories/" + name
}

func SecretPath(project, name string) string {
	return "projects/" + project + "/secrets/" + name
}

func KeyRingPath(c *Clients) string {
	return fmt.Sprintf("projects/%s/locations/%s/keyRings/%s", c.Project(), c.Region, c.KeyRing())
}

func KeyPath(c *Clients, name string) string {
	return KeyRingPath(c) + "/cryptoKeys/" + name
}

const EnabledVersion = "ENABLED"
