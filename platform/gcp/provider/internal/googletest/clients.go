package googletest

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const (
	Project = "acme-prod"
	Region  = "europe-west1"
)

var Names = names.New(provider.Namespace("ocel"), Project)

func Clients(endpoint string) *gcloud.Clients {
	return &gcloud.Clients{Names: Names, Region: Region, Endpoint: endpoint}
}

func Opener(clients *gcloud.Clients) func(context.Context) (*gcloud.Clients, error) {
	return func(context.Context) (*gcloud.Clients, error) { return clients, nil }
}
