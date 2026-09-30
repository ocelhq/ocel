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

var previewKeyAssociatedData = seal.AssociatedData{{Name: "record", Value: "preview-key"}}

func ensurePreviewKey(ctx context.Context, p provider.Provider) (edge.PreviewKey, error) {
	var key edge.PreviewKey
	err := keyvalue.Change(ctx, p.KeyValues(), stackrecords.NewPreviewKeyRecordKey(), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		opened, err := openRecordedPreviewKey(ctx, p, recorded)
		if err != nil || opened != "" {
			key = opened
			return nil, false, err
		}
		if key, err = edge.NewPreviewKey(); err != nil {
			return nil, false, err
		}
		sealed, err := p.Cipher().Seal(ctx, environment.TierPreview, previewKeyAssociatedData, []byte(key))
		if err != nil {
			return nil, false, fmt.Errorf("seal the key that signs preview hostnames: %w", err)
		}
		value, err := json.Marshal(sealed)
		if err != nil {
			return nil, false, fmt.Errorf("record the key that signs preview hostnames: %w", err)
		}
		return value, true, nil
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

func openPreviewKey(ctx context.Context, p provider.Provider) (edge.PreviewKey, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, p.KeyValues(), stackrecords.NewPreviewKeyRecordKey())
	if err != nil {
		return "", fmt.Errorf("read the key that signs preview hostnames: %w", err)
	}
	return openRecordedPreviewKey(ctx, p, recorded)
}

func openRecordedPreviewKey(ctx context.Context, p provider.Provider, recorded keyvalue.Entry) (edge.PreviewKey, error) {
	if len(recorded.Value) == 0 {
		return "", nil
	}
	var sealed []byte
	if err := json.Unmarshal(recorded.Value, &sealed); err != nil {
		return "", fmt.Errorf("read the key that signs preview hostnames: %w", err)
	}
	opened, err := p.Cipher().Open(ctx, environment.TierPreview, previewKeyAssociatedData, sealed)
	if err != nil {
		return "", fmt.Errorf("open the key that signs preview hostnames: %w", err)
	}
	return edge.PreviewKey(opened), nil
}
