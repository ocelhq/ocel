package runtrace

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func attributesOf(keyValues []*commonv1.KeyValue) []attribute.KeyValue {
	if len(keyValues) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, 0, len(keyValues))
	for _, keyValue := range keyValues {
		out = append(out, attribute.KeyValue{Key: attribute.Key(keyValue.GetKey()), Value: valueOf(keyValue.GetValue())})
	}
	return out
}

func valueOf(v *commonv1.AnyValue) attribute.Value {
	switch value := v.GetValue().(type) {
	case *commonv1.AnyValue_StringValue:
		return attribute.StringValue(value.StringValue)
	case *commonv1.AnyValue_BoolValue:
		return attribute.BoolValue(value.BoolValue)
	case *commonv1.AnyValue_IntValue:
		return attribute.Int64Value(value.IntValue)
	case *commonv1.AnyValue_DoubleValue:
		return attribute.Float64Value(value.DoubleValue)
	case *commonv1.AnyValue_BytesValue:
		return attribute.ByteSliceValue(value.BytesValue)
	case *commonv1.AnyValue_ArrayValue:
		return arrayOf(value.ArrayValue.GetValues())
	case *commonv1.AnyValue_KvlistValue:
		return attribute.MapValue(attributesOf(value.KvlistValue.GetValues())...)
	default:
		return attribute.Value{}
	}
}

func arrayOf(items []*commonv1.AnyValue) attribute.Value {
	values := make([]attribute.Value, len(items))
	for i, item := range items {
		values[i] = valueOf(item)
	}
	if len(values) == 0 || slices.ContainsFunc(values, func(v attribute.Value) bool { return v.Type() != values[0].Type() }) {
		return attribute.SliceValue(values...)
	}
	switch values[0].Type() {
	case attribute.STRING:
		return attribute.StringSliceValue(sliceOf(values, attribute.Value.AsString))
	case attribute.BOOL:
		return attribute.BoolSliceValue(sliceOf(values, attribute.Value.AsBool))
	case attribute.INT64:
		return attribute.Int64SliceValue(sliceOf(values, attribute.Value.AsInt64))
	case attribute.FLOAT64:
		return attribute.Float64SliceValue(sliceOf(values, attribute.Value.AsFloat64))
	default:
		return attribute.SliceValue(values...)
	}
}

func sliceOf[T any](values []attribute.Value, as func(attribute.Value) T) []T {
	out := make([]T, len(values))
	for i, v := range values {
		out[i] = as(v)
	}
	return out
}

var otlpIDFields = map[string]bool{"traceId": true, "spanId": true, "parentSpanId": true}

func marshalOTLPJSON(m proto.Message) ([]byte, error) {
	raw, err := protojson.Marshal(m)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(hexIDs(doc)); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func hexIDs(node any) any {
	switch value := node.(type) {
	case map[string]any:
		for key, field := range value {
			if encoded, isString := field.(string); isString && otlpIDFields[key] {
				if id, err := base64.StdEncoding.DecodeString(encoded); err == nil {
					value[key] = hex.EncodeToString(id)
				}
				continue
			}
			value[key] = hexIDs(field)
		}
	case []any:
		for i, item := range value {
			value[i] = hexIDs(item)
		}
	}
	return node
}
