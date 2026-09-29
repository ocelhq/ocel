package binding

import (
	"bytes"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

type Resolved struct {
	Name   string
	Type   resourcesv1.ResourceType
	Env    map[string]string
	Origin string
}

func Encode(t resourcesv1.ResourceType, binding *bindingsv1.Binding) (Resolved, error) {
	key, err := envName(t, binding.GetName())
	if err != nil {
		return Resolved{}, err
	}
	value, err := protojson.Marshal(binding)
	if err != nil {
		return Resolved{}, err
	}
	var stable bytes.Buffer
	if err := json.Compact(&stable, value); err != nil {
		return Resolved{}, err
	}
	return Resolved{Name: binding.GetName(), Type: t, Env: map[string]string{key: stable.String()}}, nil
}

func envName(t resourcesv1.ResourceType, name string) (string, error) {
	bound, bindable := naming.BindableAs(t)
	if !bindable {
		return "", fmt.Errorf("resource has unsupported type %s", t)
	}
	return naming.ResourceEnvName(bound, name), nil
}
