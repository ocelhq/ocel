package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

type Options map[string]any

type Settings struct {
	Options    Options
	Transforms []string
	Slug       string
}

func RefuseTransforms(vendor Vendor, transforms []string) error {
	if len(transforms) == 0 {
		return nil
	}
	listed := make([]string, len(transforms))
	for i, module := range transforms {
		listed[i] = strconv.Quote(module)
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"this project lists %s under \"transforms\" and the %s provider renders nothing a transform can patch; drop them, or deploy to a provider that does",
		strings.Join(listed, ", "), vendor)
}

func Decode[T any](vendor Vendor, options Options) (T, error) {
	var into T
	raw, err := json.Marshal(map[string]any(options))
	if err != nil {
		return into, refusal.Refuse(refusal.CodeInvalid, "options are not representable: %v", err)
	}
	var spelled any
	if err := json.Unmarshal(raw, &spelled); err != nil {
		return into, refusal.Refuse(refusal.CodeInvalid, "options are not representable: %v", err)
	}
	if err := checkOptions(vendor, into, spelled); err != nil {
		var unknown configdoc.UnknownKeyError
		if errors.As(err, &unknown) {
			return into, refusal.Refuse(refusal.CodeUnknownOption, "%s", err)
		}
		return into, refusal.Refuse(refusal.CodeInvalid, "%s", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&into); err != nil {
		return into, refusal.Refuse(refusal.CodeInvalid, "%s", decodeProblem(err))
	}
	return into, nil
}

func checkOptions(vendor Vendor, into any, spelled any) error {
	if spelled == nil {
		return nil
	}
	return configdoc.Check(configdoc.JoinPath("provider", string(vendor)), into, spelled)
}

func decodeProblem(err error) string {
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) && mismatch.Field != "" {
		return fmt.Sprintf("option %q is not a %s", mismatch.Field, mismatch.Type)
	}
	return err.Error()
}
