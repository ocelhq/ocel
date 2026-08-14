package deployenv

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

const EnvVar = "OCEL_DEPLOY_ENV"

func Load() (map[string]string, error) {
	return Parse(os.Getenv(EnvVar))
}

func Parse(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var env map[string]string
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, fmt.Errorf("%s must hold a JSON object mapping variable names to string values: %w", EnvVar, err)
	}

	for key := range env {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%s names a variable with an empty key", EnvVar)
		}
		if strings.ContainsAny(key, "= \t\n\x00") {
			return nil, fmt.Errorf("%s names a variable %q, which no environment can carry", EnvVar, key)
		}
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

func Keys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
