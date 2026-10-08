package s3

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
)

type StaticRecords struct {
	bound  []live.Binding
	values map[string]string
}

func NewStaticRecords(bindings ...*bindingsv1.Binding) (StaticRecords, error) {
	records := StaticRecords{values: map[string]string{}}
	for _, binding := range bindings {
		kind := naming.BindingTypeOf(binding)
		if kind == bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED {
			return StaticRecords{}, fmt.Errorf("the binding %s has no properties, so it is no record of any type", binding.GetName())
		}
		encoded, err := protojson.Marshal(binding)
		if err != nil {
			return StaticRecords{}, fmt.Errorf("encode the binding %s: %w", binding.GetName(), err)
		}
		key := naming.ResourceEnvName(kind, binding.GetName())
		records.bound = append(records.bound, live.Binding{Name: binding.GetName(), Key: key, Type: kind})
		records.values[key] = string(encoded)
	}
	return records, nil
}

func (r StaticRecords) Value(key string) string { return r.values[key] }

func (r StaticRecords) Bindings() []live.Binding { return r.bound }

func (r StaticRecords) Generation() uint32 { return 1 }
