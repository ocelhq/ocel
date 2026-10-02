package runs

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
