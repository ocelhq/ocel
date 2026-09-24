package projectconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

func readYAML(_ context.Context, configPath string) ([]byte, error) {
	read, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(read))
	var tree any
	if err := decoder.Decode(&tree); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s is not valid YAML: %w", configPath, err)
	}
	switch err := decoder.Decode(new(any)); {
	case err == nil:
		return nil, fmt.Errorf("%s holds more than one YAML document, and a config is one document", configPath)
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("%s is not valid YAML: %w", configPath, err)
	}

	standard, err := jsonTree("", tree)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}
	return json.Marshal(standard)
}

func jsonTree(path string, value any) (any, error) {
	switch held := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(held))
		for key, item := range held {
			at := key
			if path != "" {
				at = path + "." + key
			}
			converted, err := jsonTree(at, item)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case map[any]any:
		for key := range held {
			if _, named := key.(string); !named {
				return nil, fmt.Errorf("%s has the key %v, and a config key must be a string — quote it", describe(path), key)
			}
		}
		return nil, fmt.Errorf("%s must be an object of string keys", describe(path))
	case []any:
		out := make([]any, len(held))
		for i, item := range held {
			converted, err := jsonTree(fmt.Sprintf("%s[%d]", path, i), item)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	case float64:
		if math.IsInf(held, 0) || math.IsNaN(held) {
			return nil, fmt.Errorf("%s is %v, which a config cannot hold", describe(path), held)
		}
		return held, nil
	default:
		return value, nil
	}
}

func describe(path string) string {
	if path == "" {
		return "the config"
	}
	return strconv.Quote(path)
}
