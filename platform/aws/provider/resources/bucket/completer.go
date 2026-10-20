package bucket

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const UploadCompleterPayload = "upload-completer"

func placeUploadCompleter(ctx context.Context, artifacts component.Artifacts, tier environment.Tier) (payloads.Placement, error) {
	return component.PlacePayload(ctx, artifacts, tier, UploadCompleterPayload, "upload completer", payloads.UploadCompleter())
}
