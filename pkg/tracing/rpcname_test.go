package tracing_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing"
)

func TestAnRPCSpanIsNamedServiceSlashMethodWithItsSystemServiceAndMethodAsAttributes(t *testing.T) {
	t.Parallel()

	name, attrs := tracing.NameRPCSpan("aws-api", "DynamoDB", "GetItem")

	if name != "DynamoDB/GetItem" {
		t.Errorf("name = %q, want DynamoDB/GetItem", name)
	}
	got := map[string]string{}
	for _, attr := range attrs {
		got[string(attr.Key)] = attr.Value.AsString()
	}
	want := map[string]string{"rpc.system": "aws-api", "rpc.service": "DynamoDB", "rpc.method": "GetItem"}
	if len(got) != len(want) {
		t.Errorf("attributes = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
}
