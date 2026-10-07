package build

import (
	"encoding/base64"
	"maps"
	"slices"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func SecretValues(live map[string]string) []string {
	return append(slices.Collect(maps.Values(live)), readBindingSecrets(live)...)
}

func readBindingSecrets(live map[string]string) []string {
	var secrets []string
	for _, value := range live {
		var binding bindingsv1.Binding
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(value), &binding); err != nil {
			continue
		}
		secrets = appendRedacted(secrets, binding.ProtoReflect())
	}
	return secrets
}

func appendRedacted(secrets []string, message protoreflect.Message) []string {
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.Kind() == protoreflect.MessageKind && !field.IsList() && !field.IsMap():
			secrets = appendRedacted(secrets, value.Message())
		case !isDebugRedacted(field):
		case field.Kind() == protoreflect.StringKind && !field.IsList() && !field.IsMap():
			secrets = append(secrets, value.String())
		case field.Kind() == protoreflect.BytesKind && !field.IsList() && !field.IsMap():
			secrets = append(secrets, string(value.Bytes()), base64.StdEncoding.EncodeToString(value.Bytes()))
		}
		return true
	})
	return secrets
}

func isDebugRedacted(field protoreflect.FieldDescriptor) bool {
	options, _ := field.Options().(*descriptorpb.FieldOptions)
	return options.GetDebugRedact()
}
