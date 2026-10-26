package release

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/gcp/provider/assets"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

type CacheGrants func(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, member string) ([]grants.Grant, error)

type Release struct {
	OpenClients       func(context.Context) (*gcloud.Clients, error)
	ReadNames         func(context.Context) (names.Names, error)
	Services          *run.Services
	Records           records.Store
	Cipher            seal.Cipher
	Components        component.Registry
	Topics            *topic.Topic
	Objects           *assets.Store
	CacheGrants       CacheGrants
	Offers            cloudflareorigin.Offers
	Region            string
	ContainerEndpoint string

	ContainerTagEndpoint string
	GateInEnv            bool
	Transformed          bool
	SkipChecks           bool
}
