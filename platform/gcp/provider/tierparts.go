package gcp

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/bastion"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
)

func tierParts() []bootstrap.RemovablePart {
	return []bootstrap.RemovablePart{
		{Part: provider.Part{Name: network.PartName, Summary: network.Summary}, Deployed: network.NewPart},
		albpart.Part,
		envsource.Part,
		bastion.Part,
	}
}

func tierAccounts() []bootstrap.Account {
	return []bootstrap.Account{
		envsource.Account,
		{Name: names.Names.PushAccount, Purpose: task.PushPurpose},
		{Name: names.Names.RealtimeAccount, Purpose: realtime.Purpose},
	}
}

func tierFronts() bootstrap.FrontParts { return cloudflareorigin.FrontParts }
