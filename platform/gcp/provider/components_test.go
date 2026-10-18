package gcp

import (
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

func (p *Provider) bucketHooks() *bucket.Bucket { return bucket.New(p.openClients) }

func composed(b bootstrap.Engine) bootstrap.Engine {
	b.Removable, b.Accounts, b.FrontParts = tierParts(), tierAccounts(), tierFronts()
	b.TakeRecords, b.SweepAccounts = variables.TakeProjectRecords, release.DeleteUnusedAccounts
	return b
}
