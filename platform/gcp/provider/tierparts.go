package gcp

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/bastion"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

func tierParts() []bootstrap.RemovablePart {
	return []bootstrap.RemovablePart{
		{Part: provider.Part{Name: network.PartName, Summary: network.Summary}, Deployed: network.NewPart},
		albpart.Part,
		envsource.Part,
		bastion.Part,
	}
}

func tierAccounts(registry component.Registry) []bootstrap.Account {
	return append([]bootstrap.Account{envsource.Account}, registry.Accounts()...)
}

func tierFronts() bootstrap.FrontParts { return cloudflareorigin.FrontParts }
