package providerserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var previewKeyAssociatedData = seal.AssociatedData{{Name: "record", Value: "preview-key"}}

type openedPreviewKeys struct {
	mu       sync.Mutex
	bySealed map[string]edge.PreviewKey
}

func (o *openedPreviewKeys) find(sealed []byte) (edge.PreviewKey, bool) {
	if o == nil {
		return "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	key, found := o.bySealed[string(sealed)]
	return key, found
}

func (o *openedPreviewKeys) keep(sealed []byte, key edge.PreviewKey) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.bySealed == nil {
		o.bySealed = map[string]edge.PreviewKey{}
	}
	o.bySealed[string(sealed)] = key
}

func ensurePreviewKey(ctx context.Context, p provider.Provider, opened *openedPreviewKeys) (edge.PreviewKey, error) {
	var key edge.PreviewKey
	err := keyvalue.Change(ctx, p.KeyValues(), stackrecords.NewPreviewKeyRecordKey(), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		recordedKey, err := openRecordedPreviewKey(ctx, p, opened, recorded)
		if err != nil || recordedKey != "" {
			key = recordedKey
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
		opened.keep(sealed, key)
		return value, true, nil
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

func openPreviewKey(ctx context.Context, p provider.Provider, opened *openedPreviewKeys) (edge.PreviewKey, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, p.KeyValues(), stackrecords.NewPreviewKeyRecordKey())
	if err != nil {
		return "", fmt.Errorf("read the key that signs preview hostnames: %w", err)
	}
	return openRecordedPreviewKey(ctx, p, opened, recorded)
}

func openRecordedPreviewKey(ctx context.Context, p provider.Provider, opened *openedPreviewKeys, recorded keyvalue.Entry) (edge.PreviewKey, error) {
	if len(recorded.Value) == 0 {
		return "", nil
	}
	var sealed []byte
	if err := json.Unmarshal(recorded.Value, &sealed); err != nil {
		return "", fmt.Errorf("read the key that signs preview hostnames: %w", err)
	}
	if key, found := opened.find(sealed); found {
		return key, nil
	}
	plaintext, err := p.Cipher().Open(ctx, environment.TierPreview, previewKeyAssociatedData, sealed)
	if err != nil {
		return "", fmt.Errorf("open the key that signs preview hostnames: %w", err)
	}
	key := edge.PreviewKey(plaintext)
	opened.keep(sealed, key)
	return key, nil
}
