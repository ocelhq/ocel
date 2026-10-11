package cloudfront

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore"
	kvstypes "github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore/types"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	previewGatePrefix = "gate/"

	keyValueStoreValueMaxBytes = 1024
)

func previewGateKey(stack string) string { return previewGatePrefix + stack }

func (s *stack) reconcilePreviewGate(ctx context.Context, c Clients, gate *previewgate.Gate) error {
	if s.tier() != environment.TierPreview {
		return nil
	}
	want, err := encodePreviewGate(s.slug(), gate)
	if err != nil {
		return err
	}
	routes := s.routes(c)
	key := previewGateKey(s.spec().name)
	have, recorded, err := routes.read(ctx, key)
	if err != nil {
		return err
	}
	switch {
	case gate == nil && !recorded:
		return nil
	case gate == nil:
		return routes.write(ctx, nil, []kvstypes.DeleteKeyRequestListItem{{Key: aws.String(key)}})
	case recorded && have == want:
		return nil
	}
	return routes.write(ctx, []kvstypes.PutKeyRequestListItem{{Key: aws.String(key), Value: aws.String(want)}}, nil)
}

func encodePreviewGate(slug string, gate *previewgate.Gate) (string, error) {
	if gate == nil {
		return "", nil
	}
	encoded, err := json.Marshal(gate)
	if err != nil {
		return "", fmt.Errorf("encode the keys that gate the preview of %s: %w", slug, err)
	}
	if len(encoded) > keyValueStoreValueMaxBytes {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the keys that gate the preview of %s take %d bytes, and the %q edge keeps them in a key value store that holds %d per value. Revoke bypass secrets or shorten the allowed OPTIONS prefixes until they fit",
			slug, len(encoded), Kind, keyValueStoreValueMaxBytes)
	}
	return string(encoded), nil
}

func (w routeStore) read(ctx context.Context, key string) (string, bool, error) {
	if w.arn == "" {
		return "", false, fmt.Errorf("the %q edge names no key value store to read %s from; bootstrap the account first", Kind, key)
	}
	out, err := w.clients.KeyValueStore.GetKey(ctx, &cloudfrontkeyvaluestore.GetKeyInput{
		KvsARN: aws.String(w.arn),
		Key:    aws.String(key),
	})
	if err != nil {
		if missingRoute(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s from the key value store the %q edge routes with: %w", key, Kind, err)
	}
	return aws.ToString(out.Value), true, nil
}
