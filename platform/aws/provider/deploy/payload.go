package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	membraneLayerKeyPrefix   = "ocel-membrane-layer"
	uploadCompleterKeyPrefix = "ocel-upload-completer"
)

func placePayload(ctx context.Context, cfg Config, prefix, label string, p payloads.Payload) (payloads.Placement, error) {
	if cfg.ArtifactBucket == "" {
		return payloads.Placement{}, fmt.Errorf("no artifact bucket to place the %s into; re-run `%s`", label, bootstrapCommand(cfg))
	}
	return payloads.Place(ctx, cfg.Uploader, cfg.ArtifactBucket, prefix, label, p)
}

func placeMembraneLayer(ctx context.Context, cfg Config) (payloads.Placement, error) {
	return placePayload(ctx, cfg, membraneLayerKeyPrefix, "membrane layer", payloads.MembraneLayer())
}

func placeUploadCompleter(ctx context.Context, cfg Config) (payloads.Placement, error) {
	return placePayload(ctx, cfg, uploadCompleterKeyPrefix, "upload completer", payloads.UploadCompleter())
}
