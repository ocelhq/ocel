package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func WriteResultJSON(w io.Writer, result proto.Message) error {
	body, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(result)
	if err != nil {
		return fmt.Errorf("render the result as JSON: %w", err)
	}
	var document bytes.Buffer
	if err := json.Compact(&document, fmt.Appendf(nil, `{"ok":true,"data":%s}`, body)); err != nil {
		return fmt.Errorf("render the result as JSON: %w", err)
	}
	document.WriteByte('\n')
	_, err = w.Write(document.Bytes())
	return err
}
