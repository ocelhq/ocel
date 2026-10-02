package runs

import (
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
)

func RefuseNonObject(metadata []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &object); err != nil || object == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the metadata is not a JSON object"))
	}
	return nil
}
