package ocel

import (
	"fmt"
	"os"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

// An UnprovisionedError is returned by every accessor of a resource during
// discovery, the pass that reads declarations before anything is provisioned. Match it
// with errors.As to keep a boot path alive when the resource is optional there.
type UnprovisionedError struct {
	// Resource is the declaration the accessor belongs to, as written in code.
	Resource string
	// Access is the accessor that was called.
	Access string
}

// Error reports which accessor reached for a resource that discovery has not
// provisioned.
func (e *UnprovisionedError) Error() string {
	return fmt.Sprintf(
		"'%s' cannot be used during discovery: tried to access '%s' before the resource was provisioned",
		e.Resource, e.Access,
	)
}

// A MissingBindingError is returned by every accessor of a resource whose binding was
// never delivered to the process. Match it with errors.As to tell a resource
// this deploy does not include from one that is misconfigured.
type MissingBindingError struct {
	// Key is the environment variable the binding arrives in.
	Key string
}

// Error names the key and the commands that deliver a binding into it.
func (e *MissingBindingError) Error() string {
	return fmt.Sprintf(
		"Value for %s is not defined. Run `ocel dev` to resolve it locally, "+
			"or `ocel deploy` to have it delivered from the resource this app binds.",
		e.Key,
	)
}

func bindingKey(name string, typ bindingsv1.BindingType) string {
	return fmt.Sprintf("OCEL_RESOURCE_%s_%s", kindOf(typ), name)
}

func kindOf(typ bindingsv1.BindingType) string {
	return strings.TrimPrefix(typ.String(), "BINDING_TYPE_")
}

func binding(name string, typ bindingsv1.BindingType) (*bindingsv1.Binding, error) {
	key := bindingKey(name, typ)
	raw, delivered := readBinding(key)
	if !delivered || raw == "" {
		return nil, &MissingBindingError{Key: key}
	}

	record := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(raw), record); err != nil {
		return nil, fmt.Errorf("%s does not carry a binding record, so this app cannot read it as a %s", key, kindOf(typ))
	}
	if got := typeOf(record); got != typ {
		return nil, fmt.Errorf("%s carries a %s binding, and this app reads it as a %s", key, kindOf(got), kindOf(typ))
	}
	return record, nil
}

func readBinding(key string) (string, bool) {
	if value, ok := os.LookupEnv(key); ok {
		return value, true
	}
	return readLiveFile(key)
}

func typeOf(delivered *bindingsv1.Binding) bindingsv1.BindingType {
	switch delivered.GetProperties().(type) {
	case *bindingsv1.Binding_Postgres:
		return bindingsv1.BindingType_BINDING_TYPE_POSTGRES
	case *bindingsv1.Binding_Bucket:
		return bindingsv1.BindingType_BINDING_TYPE_BUCKET
	case *bindingsv1.Binding_Custom:
		return bindingsv1.BindingType_BINDING_TYPE_CUSTOM
	}
	return bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED
}
