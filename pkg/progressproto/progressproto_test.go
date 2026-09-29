package progressproto_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/progressproto"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestEveryProtoAttributeKeyDecodesToOneNamedKey(t *testing.T) {
	t.Parallel()

	names := map[string]bool{}
	for number := range progressv1.AttributeKey_name {
		key := progressv1.AttributeKey(number)
		if key == progressv1.AttributeKey_ATTRIBUTE_KEY_UNSPECIFIED {
			continue
		}
		found, ok := progressproto.DecodeAttrKey(key)
		if !ok || found.Name == "" {
			t.Errorf("%s has no named key", key)
			continue
		}
		if names[found.Name] {
			t.Errorf("%s shares the name %q with another key", key, found.Name)
		}
		names[found.Name] = true
	}
	if len(progress.AttrKeys) != len(names) {
		t.Errorf("AttrKeys holds %d keys, want one for each of the %d proto keys", len(progress.AttrKeys), len(names))
	}
}

func TestEveryNamedAttributeKeyEncodesToAProtoKey(t *testing.T) {
	t.Parallel()

	for _, key := range progress.AttrKeys {
		if progressproto.EncodeAttrKey(key) == progressv1.AttributeKey_ATTRIBUTE_KEY_UNSPECIFIED {
			t.Errorf("%s encodes to no proto key", key.Name)
		}
	}
}

func TestTheUnspecifiedProtoKeyDecodesToNoNamedKey(t *testing.T) {
	t.Parallel()

	if key, ok := progressproto.DecodeAttrKey(progressv1.AttributeKey_ATTRIBUTE_KEY_UNSPECIFIED); ok {
		t.Errorf("unspecified decodes to %s", key.Name)
	}
}
