package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
)

var PreviewGatesAssociatedData = seal.AssociatedData{{Name: "record", Value: "preview-gates"}}

func ReadPreviewGates(ctx context.Context, store keyvalue.Store, cipher seal.Cipher) (map[string]previewgate.Gate, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, NewPreviewGatesRecordKey())
	if err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	if len(recorded.Value) == 0 {
		return nil, nil
	}
	var sealed []byte
	if err := json.Unmarshal(recorded.Value, &sealed); err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	plaintext, err := cipher.Open(ctx, environment.TierPreview, PreviewGatesAssociatedData, sealed)
	if err != nil {
		return nil, fmt.Errorf("open the keys that gate previews: %w", err)
	}
	var gates map[string]previewgate.Gate
	if err := json.Unmarshal(plaintext, &gates); err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	return gates, nil
}
