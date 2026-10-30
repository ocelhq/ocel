package gcp

import (
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

func (p *Provider) bucketHooks() *bucket.Bucket {
	return bucket.New(p.OpenClients, p.KeyValues())
}

func composed(b bootstrap.Engine) bootstrap.Engine {
	b.Removable, b.Accounts, b.FrontParts = tierParts(network.Patches{}), tierAccounts(newProvider(&control.Provider{}).Components()), tierFronts()
	b.TakeRecords, b.SweepAccounts = variables.TakeProjectRecords, release.DeleteUnusedAccounts
	return b
}
