package ocel

import (
	"bytes"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
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

func encodeValueAsJSON(value *structpb.Value) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := protojson.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("the value is not JSON: %w", err)
	}
	return raw, nil
}

func decodeValue[P any](value *structpb.Value, into *P) error {
	raw, err := encodeValueAsJSON(value)
	if err != nil {
		return err
	}
	if raw == nil {
		raw = json.RawMessage("null")
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("the payload does not decode into a %T: %w", *into, err)
	}
	return nil
}
