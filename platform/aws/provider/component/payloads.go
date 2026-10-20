package component

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type Artifacts struct {
	Objects payloads.ObjectStore
	Bucket  string
}

func PlacePayload(ctx context.Context, artifacts Artifacts, tier environment.Tier, name, what string, payload payloads.Payload) (payloads.Placement, error) {
	if artifacts.Bucket == "" {
		return payloads.Placement{}, fmt.Errorf("no artifact bucket to place the %s into; re-run `%s`", what, provider.BootstrapCommand(tier))
	}
	return payloads.Place(ctx, artifacts.Objects, artifacts.Bucket, name, what, payload)
}
