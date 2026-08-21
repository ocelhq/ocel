package providerkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Options map[string]any

func Decode[T any](options Options) (T, error) {
	var into T
	raw, err := json.Marshal(map[string]any(options))
	if err != nil {
		return into, Refuse(CodeInvalid, "options are not representable: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&into); err != nil {
		return into, Refuse(CodeInvalid, "%s", decodeProblem(err))
	}
	return into, nil
}

func decodeProblem(err error) string {
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return "unknown option " + field
	}
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) && mismatch.Field != "" {
		return fmt.Sprintf("option %q is not a %s", mismatch.Field, mismatch.Type)
	}
	return err.Error()
}
