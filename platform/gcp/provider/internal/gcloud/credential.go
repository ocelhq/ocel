package gcloud

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
)

func (c *Clients) DeployCredential(tier environment.Tier) credential.Deploy {
	return credential.Deploy{Names: c.Names, Region: c.Region, Tier: tier}
}
