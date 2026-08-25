package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	membraneLayerKeyPrefix   = "ocel-membrane-layer"
	uploadCompleterKeyPrefix = "ocel-upload-completer"
)

func placeUploadCompleter(ctx context.Context, cfg Config) (payloads.Placement, error) {
	if cfg.ArtifactBucket == "" {
		return payloads.Placement{}, fmt.Errorf("no artifact bucket to place the upload completer into; re-run `%s`", providerkit.BootstrapCommand(cfg.Class))
	}
	return payloads.Place(ctx, cfg.Uploader, cfg.ArtifactBucket, uploadCompleterKeyPrefix, "upload completer", payloads.UploadCompleter())
}
