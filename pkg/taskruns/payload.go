package taskruns

import (
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
)

func RefuseNonJSON(payload []byte) error {
	if len(payload) > 0 && !json.Valid(payload) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the payload is not JSON"))
	}
	return nil
}

func RefuseNonObject(metadata []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &object); err != nil || object == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the metadata is not a JSON object"))
	}
	return nil
}
