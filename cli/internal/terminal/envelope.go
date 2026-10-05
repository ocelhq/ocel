package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func marshalEnvelope(ok bool, payload proto.Message) ([]byte, error) {
	body, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(payload)
	if err != nil {
		return nil, err
	}
	key := "error"
	if ok {
		key = "data"
	}
	var document bytes.Buffer
	if err := json.Compact(&document, fmt.Appendf(nil, `{"ok":%t,%q:%s}`, ok, key, body)); err != nil {
		return nil, err
	}
	document.WriteByte('\n')
	return document.Bytes(), nil
}
