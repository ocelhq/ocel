package binding

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"ocel.dev"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

func formatKey(name string, typ bindingsv1.BindingType) string {
	return fmt.Sprintf("OCEL_RESOURCE_%s_%s", kindOf(typ), name)
}

func kindOf(typ bindingsv1.BindingType) string {
	return strings.TrimPrefix(typ.String(), "BINDING_TYPE_")
}

func Read(name string, typ bindingsv1.BindingType) (*bindingsv1.Binding, error) {
	key := formatKey(name, typ)
	raw, delivered := ReadDelivered(key)
	if !delivered || raw == "" {
		return nil, &ocel.MissingBindingError{Key: key}
	}

	record := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(raw), record); err != nil {
		return nil, fmt.Errorf("%s does not contain a binding record, so this app cannot read it as a %s", key, kindOf(typ))
	}
	if got := typeOf(record); got != typ {
		return nil, fmt.Errorf("%s contains a %s binding, and this app reads it as a %s", key, kindOf(got), kindOf(typ))
	}
	return record, nil
}

func RefuseUnbound(name string, typ bindingsv1.BindingType) error {
	_, err := Read(name, typ)
	return err
}

func ReadDelivered(key string) (string, bool) {
	if value, ok := os.LookupEnv(key); ok {
		return value, true
	}
	return readLiveFile(key)
}

func readLiveFile(key string) (string, bool) {
	dir := os.Getenv(LiveDirEnv)
	if dir == "" {
		return "", false
	}
	value, err := os.ReadFile(filepath.Join(dir, key))
	if err != nil {
		return "", false
	}
	return string(value), true
}

func typeOf(delivered *bindingsv1.Binding) bindingsv1.BindingType {
	message := delivered.ProtoReflect()
	field := message.WhichOneof(message.Descriptor().Oneofs().ByName("properties"))
	if field == nil {
		return bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED
	}
	return bindingsv1.BindingType(bindingsv1.BindingType_value["BINDING_TYPE_"+strings.ToUpper(string(field.Name()))])
}
