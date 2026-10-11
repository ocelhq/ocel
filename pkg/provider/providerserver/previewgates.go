package providerserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var previewGatesAssociatedData = seal.AssociatedData{{Name: "record", Value: "preview-gates"}}

func readPreviewGates(ctx context.Context, p provider.Provider) (map[string]edge.PreviewGate, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, p.KeyValues(), stackrecords.NewPreviewGatesRecordKey())
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
	plaintext, err := p.Cipher().Open(ctx, environment.TierPreview, previewGatesAssociatedData, sealed)
	if err != nil {
		return nil, fmt.Errorf("open the keys that gate previews: %w", err)
	}
	var gates map[string]edge.PreviewGate
	if err := json.Unmarshal(plaintext, &gates); err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	return gates, nil
}

func readPreviewGate(ctx context.Context, p provider.Provider, slug string) (*edge.PreviewGate, error) {
	gates, err := readPreviewGates(ctx, p)
	if err != nil {
		return nil, err
	}
	gate, recorded := gates[slug]
	if !recorded {
		return nil, nil
	}
	return &gate, nil
}
