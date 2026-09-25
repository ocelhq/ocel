package s3

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
)

type Records interface {
	Value(key string) string
	Bindings() []live.Binding
	Generation() uint32
}

func backends(records Records, callbacks Poster) ([]*Service, error) {
	var backends []*Service
	for _, l := range records.Bindings() {
		if l.Type != bindingsv1.BindingType_BINDING_TYPE_BUCKET {
			continue
		}
		record := &bindingsv1.Binding{}
		if err := protojson.Unmarshal([]byte(records.Value(l.Key)), record); err != nil {
			return nil, fmt.Errorf("the bucket record %s delivered under %s is not a binding record", l.Name, l.Key)
		}
		bucket := record.GetBucket()
		if !Endpointed(bucket) {
			continue
		}
		backends = append(backends, Bound(bindingTag(l.Key), l.Key, bucket, callbacks))
	}
	return backends, nil
}

func bindingTag(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}
