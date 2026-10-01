package ocel

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

type kvSegment struct {
	literal   string
	parameter string
}

type kvPattern struct {
	written  string
	segments []kvSegment
}

var (
	kvLiteralSegment   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	kvParameterSegment = regexp.MustCompile(`^:[A-Za-z_][A-Za-z0-9_]*$`)
)

func parseKVPattern(written string) (kvPattern, error) {
	if written == "" {
		return kvPattern{}, fmt.Errorf("the pattern is empty: write one or more segments joined by /, each a literal or a :parameter")
	}
	if strings.ContainsAny(written, "{}") {
		return kvPattern{}, fmt.Errorf("pattern %q holds { or }, which a store reserves as the hash tag syntax", written)
	}
	pattern := kvPattern{written: written}
	named := map[string]bool{}
	for _, part := range strings.Split(written, "/") {
		switch {
		case part == "":
			return kvPattern{}, fmt.Errorf("pattern %q has an empty segment: it neither starts nor ends with /, and no two / are adjacent", written)
		case strings.HasPrefix(part, ":"):
			if !kvParameterSegment.MatchString(part) {
				return kvPattern{}, fmt.Errorf("pattern %q has segment %q, which is no parameter: a parameter is : and a name of letters, digits and _ that starts with a letter or _", written, part)
			}
			name := part[1:]
			if named[name] {
				return kvPattern{}, fmt.Errorf("pattern %q names parameter %q twice, so a key could not say which value is which", written, name)
			}
			named[name] = true
			pattern.segments = append(pattern.segments, kvSegment{parameter: name})
		case !kvLiteralSegment.MatchString(part):
			return kvPattern{}, fmt.Errorf("pattern %q has segment %q, which is no literal: a literal is letters, digits, ., _ and -", written, part)
		default:
			pattern.segments = append(pattern.segments, kvSegment{literal: part})
		}
	}
	return pattern, nil
}

func (p kvPattern) parameters() []string {
	var names []string
	for _, segment := range p.segments {
		if segment.parameter != "" {
			names = append(names, segment.parameter)
		}
	}
	return names
}

func (p kvPattern) overlaps(other kvPattern) bool {
	if len(p.segments) != len(other.segments) {
		return false
	}
	for i, mine := range p.segments {
		theirs := other.segments[i]
		if mine.parameter == "" && theirs.parameter == "" && mine.literal != theirs.literal {
			return false
		}
	}
	return true
}

type kvKeyBuilder struct {
	pattern kvPattern
	fields  map[string][]int
	scalar  bool
}

func newKVKeyBuilder(pattern kvPattern, keyType reflect.Type) (kvKeyBuilder, error) {
	parameters := pattern.parameters()
	builder := kvKeyBuilder{pattern: pattern, fields: map[string][]int{}}
	if keyType.Kind() != reflect.Struct {
		if len(parameters) != 1 {
			if len(parameters) == 0 {
				return kvKeyBuilder{}, fmt.Errorf("pattern %q has no parameters, so its key type is struct{}, not %s", pattern.written, keyType)
			}
			return kvKeyBuilder{}, fmt.Errorf("pattern %q has %d parameters, so its key type is a struct with a field for each, not %s", pattern.written, len(parameters), keyType)
		}
		if !isKVScalar(keyType) {
			return kvKeyBuilder{}, fmt.Errorf("key type %s of pattern %q is no string or integer: a key is a string or an integer, or a struct of them", keyType, pattern.written)
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
		parameter := matchKVParameter(field, parameters)
		if parameter == "" {
			return kvKeyBuilder{}, fmt.Errorf("key type %s has field %s, which names no parameter of pattern %q: its fields are its parameters %s", keyType, field.Name, pattern.written, describeKVParameters(parameters))
		}
		if !isKVScalar(field.Type) {
			return kvKeyBuilder{}, fmt.Errorf("key type %s has field %s of type %s: each field is a string or an integer", keyType, field.Name, field.Type)
		}
		builder.fields[parameter] = field.Index
		delete(unmatched, parameter)
	}
	for _, parameter := range parameters {
		if unmatched[parameter] {
			return kvKeyBuilder{}, fmt.Errorf("key type %s has no field for parameter %q of pattern %q: name a field after it, or tag one `kv:%q`", keyType, parameter, pattern.written, parameter)
		}
	}
	return builder, nil
}

func matchKVParameter(field reflect.StructField, parameters []string) string {
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

func describeKVParameters(parameters []string) string {
	if len(parameters) == 0 {
		return "(it has none)"
	}
	return strconv.Quote(strings.Join(parameters, `", "`))
}

func isKVScalar(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

func (b kvKeyBuilder) build(key reflect.Value) string {
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
		built.WriteString(encodeKVParameter(formatKVScalar(value)))
	}
	return built.String()
}

func formatKVScalar(value reflect.Value) string {
	switch value.Kind() {
	case reflect.String:
		return value.String()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10)
	default:
		return strconv.FormatInt(value.Int(), 10)
	}
}

func encodeKVParameter(value string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := range len(value) {
		c := value[i]
		if isKVUnreserved(c) {
			encoded.WriteByte(c)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[c>>4])
		encoded.WriteByte(hex[c&15])
	}
	return encoded.String()
}

func isKVUnreserved(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}
