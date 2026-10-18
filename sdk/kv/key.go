package kv

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

type keySegment struct {
	literal   string
	parameter string
}

type keyPattern struct {
	written  string
	segments []keySegment
}

func parseKeyPattern(written string) keyPattern {
	pattern := keyPattern{written: written}
	for _, part := range strings.Split(written, "/") {
		if parameter, ok := strings.CutPrefix(part, ":"); ok {
			pattern.segments = append(pattern.segments, keySegment{parameter: parameter})
			continue
		}
		pattern.segments = append(pattern.segments, keySegment{literal: part})
	}
	return pattern
}

func (p keyPattern) parameters() []string {
	var names []string
	for _, segment := range p.segments {
		if segment.parameter != "" {
			names = append(names, segment.parameter)
		}
	}
	return names
}

type keyBuilder struct {
	pattern keyPattern
	fields  map[string][]int
	scalar  bool
}

func newKeyBuilder(pattern keyPattern, keyType reflect.Type) (keyBuilder, error) {
	parameters := pattern.parameters()
	builder := keyBuilder{pattern: pattern, fields: map[string][]int{}}
	if keyType.Kind() != reflect.Struct {
		if len(parameters) != 1 {
			if len(parameters) == 0 {
				return keyBuilder{}, fmt.Errorf("pattern %q has no parameters, so its key type is struct{}, not %s", pattern.written, keyType)
			}
			return keyBuilder{}, fmt.Errorf("pattern %q has %d parameters, so its key type is a struct with a field for each, not %s", pattern.written, len(parameters), keyType)
		}
		if !isScalar(keyType) {
			return keyBuilder{}, fmt.Errorf("key type %s of pattern %q is no string or integer: a key is a string or an integer, or a struct of them", keyType, pattern.written)
		}
		builder.scalar = true
		return builder, nil
	}

	unmatched := map[string]bool{}
	for _, parameter := range parameters {
		unmatched[parameter] = true
	}
	for i := range keyType.NumField() {
		field := keyType.Field(i)
		if !field.IsExported() {
			continue
		}
		parameter := matchParameter(field, parameters)
		if parameter == "" {
			return keyBuilder{}, fmt.Errorf("key type %s has field %s, which names no parameter of pattern %q: its fields are its parameters %s", keyType, field.Name, pattern.written, describeParameters(parameters))
		}
		if !isScalar(field.Type) {
			return keyBuilder{}, fmt.Errorf("key type %s has field %s of type %s: each field is a string or an integer", keyType, field.Name, field.Type)
		}
		builder.fields[parameter] = field.Index
		delete(unmatched, parameter)
	}
	for _, parameter := range parameters {
		if unmatched[parameter] {
			return keyBuilder{}, fmt.Errorf("key type %s has no field for parameter %q of pattern %q: name a field after it, or tag one `kv:%q`", keyType, parameter, pattern.written, parameter)
		}
	}
	return builder, nil
}

func matchParameter(field reflect.StructField, parameters []string) string {
	if tag, ok := field.Tag.Lookup("kv"); ok {
		for _, parameter := range parameters {
			if parameter == tag {
				return parameter
			}
		}
		return ""
	}
	for _, parameter := range parameters {
		if strings.EqualFold(parameter, field.Name) {
			return parameter
		}
	}
	return ""
}

func describeParameters(parameters []string) string {
	if len(parameters) == 0 {
		return "(it has none)"
	}
	return strconv.Quote(strings.Join(parameters, `", "`))
}

func isScalar(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

func (b keyBuilder) build(key reflect.Value) string {
	var built strings.Builder
	for i, segment := range b.pattern.segments {
		if i > 0 {
			built.WriteByte('/')
		}
		if segment.parameter == "" {
			built.WriteString(segment.literal)
			continue
		}
		value := key
		if !b.scalar {
			value = key.FieldByIndex(b.fields[segment.parameter])
		}
		built.WriteString(encodeParameter(formatScalar(value)))
	}
	return built.String()
}

func formatScalar(value reflect.Value) string {
	switch value.Kind() {
	case reflect.String:
		return value.String()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10)
	default:
		return strconv.FormatInt(value.Int(), 10)
	}
}

func encodeParameter(value string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := range len(value) {
		c := value[i]
		if isUnreserved(c) {
			encoded.WriteByte(c)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[c>>4])
		encoded.WriteByte(hex[c&15])
	}
	return encoded.String()
}

func isUnreserved(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}
