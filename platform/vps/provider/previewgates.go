package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

func newPreviewGateReader(store keyvalue.Store, cipher seal.Cipher) box.PreviewGateReader {
	return func(ctx context.Context, project string) (*previewgate.Gate, error) {
		return stackrecords.ReadPreviewGate(ctx, store, cipher, project)
	}
}
