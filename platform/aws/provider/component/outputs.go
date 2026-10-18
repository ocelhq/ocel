package component

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
)

func RequireOutputFields(outputs auto.OutputMap, name string) (map[string]any, error) {
	raw, produced := outputs[name]
	if !produced {
		return nil, fmt.Errorf("stack produced no output for %s", name)
	}
	fields, mapped := raw.Value.(map[string]any)
	if !mapped {
		return nil, fmt.Errorf("output for %s is not a map", name)
	}
	return fields, nil
}

func RequireStringField(fields map[string]any, name, key string) (string, error) {
	v, ok := fields[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("output %q for %s is missing or not a non-empty string", key, name)
	}
	return v, nil
}

const (
	OutputKeyHost = "host"
	OutputKeyPort = "port"
)
