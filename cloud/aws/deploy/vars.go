package deploy

import (
	"encoding/json"
	"fmt"
)

// varsDecryptPolicy grants a function execution role decrypt on exactly one
// key: the one its own env class's variable store encrypts under, named by ARN
// rather than a wildcard so the class boundary holds. Decrypt is the only
// action — values are encrypted provider-side, never by a runtime.
func varsDecryptPolicy(keyARN string) (string, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"kms:Decrypt"},
				"Resource": keyARN,
			},
		},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render vars decrypt policy: %w", err)
	}
	return string(out), nil
}
