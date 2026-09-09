package schematest

import (
	"bytes"
	"os"
	"testing"
)

const CoreSchemaFile = "schema.core.json"

const OptionsSchemaFile = "schema.options.json"

const updateEnvVar = "OCEL_UPDATE_SCHEMA"

func AssertCommitted(t *testing.T, path string, generated []byte) {
	t.Helper()
	generated = append(bytes.TrimRight(generated, "\n"), '\n')

	committed, err := os.ReadFile(path)
	if err == nil && bytes.Equal(committed, generated) {
		return
	}
	if os.Getenv(updateEnvVar) == "" {
		t.Fatalf("%s is stale: the Go types it is generated from have changed. Regenerate it with %s=1 go test ./... and commit the result", path, updateEnvVar)
	}
	if err := os.WriteFile(path, generated, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
