package providerkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/configdoc"
)

const optionsPath = "provider.options"

type Options map[string]any

func Decode[T any](options Options) (T, error) {
	var into T
	raw, err := json.Marshal(map[string]any(options))
	if err != nil {
		return into, Refuse(CodeInvalid, "options are not representable: %v", err)
	}
	var spelled any
	if err := json.Unmarshal(raw, &spelled); err != nil {
		return into, Refuse(CodeInvalid, "options are not representable: %v", err)
	}
	if err := checkOptions(into, spelled); err != nil {
		var unknown configdoc.UnknownKeyError
		if errors.As(err, &unknown) {
			return into, Refuse(CodeUnknownOption, "%s", err)
		}
		return into, Refuse(CodeInvalid, "%s", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&into); err != nil {
		return into, Refuse(CodeInvalid, "%s", decodeProblem(err))
	}
	return into, nil
}

func checkOptions(into any, spelled any) error {
	if spelled == nil {
		return nil
	}
	return configdoc.Check(optionsPath, into, spelled)
}

func decodeProblem(err error) string {
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) && mismatch.Field != "" {
		return fmt.Sprintf("option %q is not a %s", mismatch.Field, mismatch.Type)
	}
	return err.Error()
}
