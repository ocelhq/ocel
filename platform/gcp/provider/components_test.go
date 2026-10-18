package gcp

import (
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/bucket"
)

func (p *Provider) bucketHooks() *bucket.Bucket { return bucket.New(p.openClients) }

func composed(b bootstrap.Engine) bootstrap.Engine {
	b.Removable, b.Accounts, b.FrontParts = tierParts(), tierAccounts(), tierFronts()
	b.TakeRecords, b.SweepAccounts = takeProjectRecords, deleteUnusedAccounts
	return b
}
