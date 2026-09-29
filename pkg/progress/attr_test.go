package progress

import (
	"testing"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestEveryWireAttributeKeyHasOneNamedKey(t *testing.T) {
	t.Parallel()

	names := map[string]bool{}
	for wire := range progressv1.AttributeKey_name {
		key := progressv1.AttributeKey(wire)
		if key == progressv1.AttributeKey_ATTRIBUTE_KEY_UNSPECIFIED {
			continue
		}
		found, ok := FindAttrKey(key)
		if !ok || found.Name == "" {
			t.Errorf("%s has no named key", key)
			continue
		}
		if names[found.Name] {
			t.Errorf("%s shares the name %q with another key", key, found.Name)
		}
		names[found.Name] = true
	}
	if len(AttrKeys) != len(names) {
		t.Errorf("AttrKeys holds %d keys, want one for each of the %d wire keys", len(AttrKeys), len(names))
	}
}
