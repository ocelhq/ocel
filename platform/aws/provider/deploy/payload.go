package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	uploadCompleterKeyPrefix = "ocel-upload-completer"
)

func placeUploadCompleter(ctx context.Context, cfg Config) (payloads.Placement, error) {
	if cfg.ArtifactBucket == "" {
		return payloads.Placement{}, fmt.Errorf("no artifact bucket to place the upload completer into; re-run `%s`", provider.BootstrapCommand(cfg.Class))
	}
	return payloads.Place(ctx, cfg.Objects, cfg.ArtifactBucket, uploadCompleterKeyPrefix, "upload completer", payloads.UploadCompleter())
}
