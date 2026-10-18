package typescript

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

type Import struct {
	Type reflect.Type
	Name string
	From string
}

type declarations struct {
	imports  map[reflect.Type]Import
	declared map[reflect.Type]bool
	order    []reflect.Type
}

var rawMessage = reflect.TypeFor[json.RawMessage]()

func Declare(header string, imports []Import, roots ...reflect.Type) (string, error) {
	d := &declarations{imports: map[reflect.Type]Import{}, declared: map[reflect.Type]bool{}}
	for _, imported := range imports {
		d.imports[imported.Type] = imported
	}
	for _, root := range roots {
		if _, err := d.typeOf(root); err != nil {
			return "", err
		}
	}
	return d.render(header)
}

func (d *declarations) typeOf(t reflect.Type) (string, error) {
	if imported, ok := d.imports[t]; ok {
		return imported.Name, nil
	}
	if t == rawMessage {
		return "unknown", nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return d.typeOf(t.Elem())
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "boolean", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return "number", nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return "string", nil
		}
		elem, err := d.typeOf(t.Elem())
		return elem + "[]", err
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return "", fmt.Errorf("%s is keyed by %s, and a JSON object is keyed by strings", t, t.Key())
		}
		elem, err := d.typeOf(t.Elem())
		return "Record<string, " + elem + ">", err
	case reflect.Struct:
		if t.Name() == "" {
			return "", fmt.Errorf("an unnamed struct has no interface name to declare it under")
		}
		if !d.declared[t] {
			d.declared[t] = true
			d.order = append(d.order, t)
			fields, extends, err := d.fieldsOf(t)
			if err != nil {
				return "", err
			}
			for _, embedded := range extends {
				if _, err := d.typeOf(embedded); err != nil {
					return "", err
				}
			}
			for _, f := range fields {
				if _, err := d.typeOf(f.typ); err != nil {
					return "", err
				}
			}
		}
		return interfaceName(t), nil
	}
	return "", fmt.Errorf("%s has no JSON shape to declare", t)
}

type field struct {
	name     string
	typ      reflect.Type
	optional bool
}

func (d *declarations) fieldsOf(t reflect.Type) ([]field, []reflect.Type, error) {
	var fields []field
	var extends []reflect.Type
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if !f.IsExported() || tag == "-" || f.Tag.Get("ts") == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			if _, imported := d.imports[f.Type]; imported || f.Type.Kind() == reflect.Struct {
				extends = append(extends, f.Type)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		opts := strings.Split(options, ",")
		optional := f.Type.Kind() == reflect.Pointer || slices.Contains(opts, "omitempty") || slices.Contains(opts, "omitzero")
		fields = append(fields, field{name: name, typ: f.Type, optional: optional})
	}
	return fields, extends, nil
}

func (d *declarations) render(header string) (string, error) {
	var out strings.Builder
	out.WriteString(header)
	out.WriteString("\n")
	byModule := map[string][]string{}
	for _, t := range d.order {
		if err := d.collectImports(t, byModule); err != nil {
			return "", err
		}
	}
	if len(byModule) > 0 {
		out.WriteString("\n")
	}
	for _, from := range slices.Sorted(maps.Keys(byModule)) {
		names := slices.Compact(slices.Sorted(slices.Values(byModule[from])))
		fmt.Fprintf(&out, "import type { %s } from %q;\n", strings.Join(names, ", "), from)
	}
	for _, t := range d.order {
		fields, extends, err := d.fieldsOf(t)
		if err != nil {
			return "", err
		}
		out.WriteString("\n")
		heading := "export interface " + interfaceName(t)
		if len(extends) > 0 {
			names := make([]string, 0, len(extends))
			for _, embedded := range extends {
				name, err := d.typeOf(embedded)
				if err != nil {
					return "", err
				}
				names = append(names, name)
			}
			heading += " extends " + strings.Join(names, ", ")
		}
		out.WriteString(heading + " {\n")
		for _, f := range fields {
			ts, err := d.typeOf(f.typ)
			if err != nil {
				return "", err
			}
			mark := ""
			if f.optional {
				mark = "?"
			}
			fmt.Fprintf(&out, "  %s%s: %s;\n", f.name, mark, ts)
		}
		out.WriteString("}\n")
	}
	return out.String(), nil
}

func (d *declarations) collectImports(t reflect.Type, byModule map[string][]string) error {
	fields, extends, err := d.fieldsOf(t)
	if err != nil {
		return err
	}
	types := slices.Clone(extends)
	for _, f := range fields {
		types = append(types, f.typ)
	}
	for _, used := range types {
		for used.Kind() == reflect.Pointer || used.Kind() == reflect.Slice || used.Kind() == reflect.Map {
			used = used.Elem()
		}
		if imported, ok := d.imports[used]; ok {
			byModule[imported.From] = append(byModule[imported.From], imported.Name)
		}
	}
	return nil
}

func interfaceName(t reflect.Type) string {
	name := t.Name()
	return strings.ToUpper(name[:1]) + name[1:]
}
