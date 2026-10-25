package bindings

import (
	"encoding/json"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func VariableText(l *bindingsv1.Binding, property string) (string, bool) {
	value, ok := l.GetCustom().GetFields()[property]
	if !ok {
		return "", false
	}
	if text, isText := value.AsInterface().(string); isText {
		return text, true
	}
	encoded, err := json.Marshal(value.AsInterface())
	if err != nil {
		return "", false
	}
	return string(encoded), true
}
