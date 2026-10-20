package gcloud

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
)

func (c *Clients) DeployCredential(tier environment.Tier) credential.Deploy {
	return credential.New(c.Names, c.Region, tier)
}
