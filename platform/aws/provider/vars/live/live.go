package live

import (
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const FilePath = constants.ProjectStateDirName + "/variables.live.json"

type Manifest struct {
	Slug        string    `json:"slug"`
	Table       string    `json:"table"`
	KeyARN      string    `json:"keyArn"`
	Class       string    `json:"class"`
	Environment string    `json:"environment,omitempty"`
	Keys        []Key     `json:"keys"`
	Bindings    []Binding `json:"bindings,omitempty"`
}

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
		return fmt.Errorf("%w: binding %s published a record under %s that carries no properties, and this deployment was built to read a %s", ErrDrift, l.Name, l.Key, l.Type)
	}
	if published != l.Type {
		return fmt.Errorf("%w: binding %s publishes a %s record under %s, and this deployment was built to read a %s", ErrDrift, l.Name, published, l.Key, l.Type)
	}
	return nil
}

func Render(m Manifest) ([]byte, error) {
	if len(m.Keys) == 0 && len(m.Bindings) == 0 {
		return nil, nil
	}
	for _, component := range []struct{ name, value string }{
		{"project slug", m.Slug},
		{"variable table", m.Table},
		{"environment class", m.Class},
	} {
		if component.value == "" {
			return nil, fmt.Errorf("the live-value manifest names %d keys but no %s", len(m.Keys)+len(m.Bindings), component.name)
		}
	}
	if m.KeyARN == "" {
		return nil, fmt.Errorf("the live-value manifest names %d keys but the %s bootstrap holds no key to read them through.\nRun `%s` to add one, then deploy again",
			len(m.Keys)+len(m.Bindings), m.Class, providerkit.BootstrapVarsKeyCommand(providerkit.Class(m.Class)))
	}
	return json.Marshal(m)
}

func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", FilePath, err)
	}
	return m, nil
}
