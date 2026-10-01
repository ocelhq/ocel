package ocel

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const maxPayloadBytes = 256 << 10

func encodeJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func encodePayload(payload any) ([]byte, error) {
	body, err := encodeJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("the payload does not encode as JSON: %w", err)
	}
	if len(body) > maxPayloadBytes {
		return nil, fmt.Errorf("the payload is %d bytes of JSON, and a payload is at most %d bytes (256 KiB)", len(body), maxPayloadBytes)
	}
	return body, nil
}

func decodePayload[P any](raw []byte, into *P) error {
	if len(raw) == 0 {
		raw = []byte("null")
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("the payload does not decode into a %T: %w", *into, err)
	}
	return nil
}

func readJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
