package progresswire

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func EncodeAttrKey(key progress.AttrKey) progressv1.AttributeKey {
	return progressv1.AttributeKey(progressv1.AttributeKey_value["ATTRIBUTE_KEY_"+strings.ToUpper(strings.TrimPrefix(key.Name, "ocel."))])
}

func DecodeAttrKey(wire progressv1.AttributeKey) (progress.AttrKey, bool) {
	if wire == progressv1.AttributeKey_ATTRIBUTE_KEY_UNSPECIFIED {
		return progress.AttrKey{}, false
	}
	for _, key := range progress.AttrKeys {
		if EncodeAttrKey(key) == wire {
			return key, true
		}
	}
	return progress.AttrKey{}, false
}

func EncodeAttrs(attrs []progress.Attr) []*progressv1.SpanAttribute {
	encoded := make([]*progressv1.SpanAttribute, len(attrs))
	for i, a := range attrs {
		encoded[i] = &progressv1.SpanAttribute{Key: EncodeAttrKey(a.Key), Value: a.Value}
	}
	return encoded
}
