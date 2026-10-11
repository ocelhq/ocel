package deploy

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
)

func encodePreviewGate(gate *edge.PreviewGate) (string, error) {
	if gate == nil {
		return "", nil
	}
	encoded, err := json.Marshal(gate)
	if err != nil {
		return "", fmt.Errorf("encode the keys that gate the preview: %w", err)
	}
	return string(encoded), nil
}

func listPreviewGateSecrets(gate *edge.PreviewGate) []string {
	if gate == nil {
		return nil
	}
	encoded, _ := encodePreviewGate(gate)
	return append([]string{encoded, gate.Key, gate.PasswordMAC}, gate.BypassSecrets...)
}
