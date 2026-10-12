package previewgate

import (
	"bytes"
	"encoding/json"
	"errors"
)

const (
	MountedFileVar   = "OCEL_PREVIEW_GATE_FILE"
	MountedInlineVar = "OCEL_PREVIEW_GATE"
)

var withdrawn = []byte("null")

var ErrMountedGateUnreadable = errors.New("the mounted preview gate cannot be read")

func EncodeMounted(gate *Gate) ([]byte, error) {
	if gate == nil {
		return bytes.Clone(withdrawn), nil
	}
	return json.Marshal(gate)
}

func DecodeMounted(contents []byte) (*Gate, error) {
	trimmed := bytes.TrimSpace(contents)
	if bytes.Equal(trimmed, withdrawn) {
		return nil, nil
	}
	var gate Gate
	if err := json.Unmarshal(trimmed, &gate); err != nil {
		return nil, errors.Join(ErrMountedGateUnreadable, err)
	}
	if gate.Key == "" {
		return nil, errors.Join(ErrMountedGateUnreadable, errors.New("it names no key"))
	}
	return &gate, nil
}
