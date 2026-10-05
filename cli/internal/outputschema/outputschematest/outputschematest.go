package outputschematest

import (
	"bytes"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaURL = "https://ocel.dev/schema/test/output.schema.json"

func Compile(t *testing.T, schema []byte) *jsonschema.Schema {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		t.Fatalf("the schema is not JSON: %v\n%s", err, schema)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, document); err != nil {
		t.Fatalf("add the schema: %v", err)
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatalf("compile the schema: %v", err)
	}
	return compiled
}

func Validate(schema *jsonschema.Schema, document []byte) error {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}
