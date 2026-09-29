package clitest

import (
	"encoding/json"
	"strings"
	"testing"
)

func DecodeJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("stdout = %q is not JSON: %v", out, err)
	}
	return rec
}
