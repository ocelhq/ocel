package outputschema

import (
	"embed"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
)

//go:embed schemas/*.schema.json
var schemas embed.FS

const (
	runEventMessage protoreflect.FullName = "cli.stream.v1.RunEvent"
	runErrorMessage protoreflect.FullName = "cli.stream.v1.RunError"
	draft2020       string                = "https://json-schema.org/draft/2020-12/schema"
)

func RunEvent() ([]byte, error) {
	return read(runEventMessage)
}

func Result(messages ...protoreflect.FullName) ([]byte, error) {
	definitions := map[string]any{}
	success := make([]any, 0, len(messages))
	for _, message := range messages {
		reference, err := mergeDefinitions(definitions, message)
		if err != nil {
			return nil, err
		}
		success = append(success, map[string]any{"$ref": reference})
	}
	failure, err := mergeDefinitions(definitions, runErrorMessage)
	if err != nil {
		return nil, err
	}
	data := success[0]
	if len(success) > 1 {
		data = map[string]any{"anyOf": success}
	}
	return json.MarshalIndent(map[string]any{
		"$schema": draft2020,
		"title":   "Result envelope",
		"oneOf": []any{
			envelope(true, "data", data),
			envelope(false, "error", map[string]any{"$ref": failure}),
		},
		"$defs": definitions,
	}, "", "  ")
}

func envelope(ok bool, key string, payload any) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"ok", key},
		"properties": map[string]any{
			"ok": map[string]any{"const": ok},
			key:  payload,
		},
	}
}

func mergeDefinitions(into map[string]any, message protoreflect.FullName) (string, error) {
	raw, err := read(message)
	if err != nil {
		return "", err
	}
	var bundle struct {
		Ref         string         `json:"$ref"`
		Definitions map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return "", fmt.Errorf("read the schema of %s: %w", message, err)
	}
	for name, definition := range bundle.Definitions {
		into[name] = definition
	}
	return bundle.Ref, nil
}

func read(message protoreflect.FullName) ([]byte, error) {
	raw, err := schemas.ReadFile("schemas/" + string(message) + ".schema.json")
	if err != nil {
		return nil, fmt.Errorf("no schema for %s: %w", message, err)
	}
	return raw, nil
}
