package terminal

import (
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

func WriteResultJSON(w io.Writer, result proto.Message) error {
	document, err := marshalEnvelope(true, result)
	if err != nil {
		return fmt.Errorf("render the result as JSON: %w", err)
	}
	_, err = w.Write(document)
	return err
}
