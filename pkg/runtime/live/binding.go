package live

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

type Key struct {
	Key    string `json:"key"`
	Folder string `json:"folder,omitempty"`
}

type Binding struct {
	Name    string                 `json:"name"`
	Key     string                 `json:"key"`
	Type    bindingsv1.BindingType `json:"type"`
	Granted int64                  `json:"granted,omitempty"`
}

func (l Binding) MarshalJSON() ([]byte, error) {
	type wire Binding
	return json.Marshal(struct {
		wire
		Type string `json:"type"`
	}{wire(l), l.Type.String()})
}

func (l *Binding) UnmarshalJSON(data []byte) error {
	type wire Binding
	var decoded struct {
		wire
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value, ok := bindingsv1.BindingType_value[decoded.Type]
	if !ok {
		return fmt.Errorf("binding %s names type %q, which no binding type is called", decoded.Name, decoded.Type)
	}
	*l = Binding(decoded.wire)
	l.Type = bindingsv1.BindingType(value)
	return nil
}

var ErrDrift = errors.New("binding record drift")

func Conform(bindings []Binding, values map[string]string) error {
	for _, l := range bindings {
		raw, ok := values[l.Key]
		if !ok {
			return fmt.Errorf("%w: binding %s published no record under %s, which this deployment reads as a %s", ErrDrift, l.Name, l.Key, l.Type)
		}
		if err := l.conform(raw); err != nil {
			return err
		}
	}
	return nil
}

func (l Binding) conform(raw string) error {
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(raw), binding); err != nil {
		return fmt.Errorf("%w: binding %s published something under %s that is not a binding record at all, and this deployment was built to read a %s", ErrDrift, l.Name, l.Key, l.Type)
	}
	published := naming.BindingTypeOf(binding)
	if published == bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED {
		return fmt.Errorf("%w: binding %s published a record under %s that has no properties, and this deployment was built to read a %s", ErrDrift, l.Name, l.Key, l.Type)
	}
	if published != l.Type {
		return fmt.Errorf("%w: binding %s publishes a %s record under %s, and this deployment was built to read a %s", ErrDrift, l.Name, published, l.Key, l.Type)
	}
	return nil
}

func Keys(keys []Key, bindings []Binding) []string {
	out := make([]string, 0, len(keys)+len(bindings))
	for _, k := range keys {
		out = append(out, k.Key)
	}
	for _, l := range bindings {
		out = append(out, l.Key)
	}
	return out
}

func shown(bindings []Binding, values map[string]string) map[string]string {
	shownValues := maps.Clone(values)
	for _, l := range bindings {
		if raw, ok := values[l.Key]; ok {
			shownValues[l.Key] = l.shown(raw)
		}
	}
	return shownValues
}

func (l Binding) shown(raw string) string {
	if l.Type != bindingsv1.BindingType_BINDING_TYPE_BUCKET {
		return raw
	}
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(raw), binding); err != nil || binding.GetBucket().GetEndpoint() == "" {
		return raw
	}
	stored := binding.GetBucket()
	binding.Properties = &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{
		Bucket:        l.Key,
		PublicBaseUrl: stored.GetPublicBaseUrl(),
		Public:        stored.GetPublic(),
	}}
	out, err := protojson.Marshal(binding)
	if err != nil {
		return raw
	}
	return string(out)
}
