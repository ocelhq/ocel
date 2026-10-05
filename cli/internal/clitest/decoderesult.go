package clitest

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func DecodeResult(t *testing.T, out string) map[string]any {
	t.Helper()
	if lines := strings.Count(strings.TrimSuffix(out, "\n"), "\n"); lines != 0 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout = %q, want exactly one newline-terminated line", out)
	}
	envelope := DecodeJSON(t, out)
	if envelope["ok"] != true {
		t.Fatalf("envelope = %v, want ok true", envelope)
	}
	if len(envelope) != 2 {
		t.Fatalf("envelope = %v, want only ok and data", envelope)
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("envelope = %v, want a data object", envelope)
	}
	return data
}

func DecodeResultInto(t *testing.T, out string, result proto.Message) {
	t.Helper()
	data, err := json.Marshal(DecodeResult(t, out))
	if err != nil {
		t.Fatalf("re-encode the result data: %v", err)
	}
	if err := protojson.Unmarshal(data, result); err != nil {
		t.Fatalf("data is not a %s: %v\n%s", result.ProtoReflect().Descriptor().FullName(), err, data)
	}
}
