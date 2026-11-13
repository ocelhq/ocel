package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/format"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/pulumi/inflector"

	"github.com/ocelhq/ocel/pkg/pricing/ruleset"
	"github.com/ocelhq/ocel/pkg/pricing/source/pulumi"
	"github.com/ocelhq/ocel/pkg/tracing"
)

var packages = []struct{ vendor, plugin string }{
	{"aws", "aws@7.36.0"},
	{"gcp", "gcp@9.36.1"},
	{"cloudflare", "cloudflare@6.23.1"},
}

const (
	typeList = 5
	typeMap  = 6
	typeSet  = 7
)

type terraformSchema struct {
	Type     int `json:"type"`
	MaxItems int `json:"maxItems"`
	Element  *struct {
		Resource map[string]*terraformSchema `json:"resource"`
	} `json:"element"`
}

type override struct {
	Name        string               `json:"name"`
	MaxItemsOne *bool                `json:"maxItemsOne"`
	Element     *override            `json:"element"`
	Fields      map[string]*override `json:"fields"`
}

type mapping struct {
	Provider struct {
		Resources map[string]map[string]*terraformSchema `json:"resources"`
	} `json:"provider"`
	Resources map[string]struct {
		Token  string               `json:"tok"`
		Fields map[string]*override `json:"fields"`
	} `json:"resources"`
}

type pulumiProperty struct {
	Ref   string          `json:"$ref"`
	Items *pulumiProperty `json:"items"`
}

type pulumiObject struct {
	Properties      map[string]pulumiProperty `json:"properties"`
	InputProperties map[string]pulumiProperty `json:"inputProperties"`
}

type pulumiSchema struct {
	Resources map[string]pulumiObject `json:"resources"`
	Types     map[string]pulumiObject `json:"types"`
}

type field struct {
	terraform string
	fields    map[string]field
}

type bridgedType struct {
	token  string
	fields map[string]field
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: generate <bridged.go>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out string) error {
	priced := map[string][]string{}
	for _, rules := range ruleset.Vendors() {
		for typ := range rules.Table {
			priced[rules.Ingest.Vendor] = append(priced[rules.Ingest.Vendor], typ)
		}
	}
	bridged := map[string]bridgedType{}
	var unbridged []string
	for _, pkg := range packages {
		types := priced[pkg.vendor]
		delete(priced, pkg.vendor)
		if len(types) == 0 {
			continue
		}
		held, schema, err := fetch(pkg.plugin)
		if err != nil {
			return err
		}
		for _, typ := range types {
			resource, found := held.Resources[typ]
			if !found {
				unbridged = append(unbridged, typ)
				continue
			}
			object, found := schema.Resources[resource.Token]
			if !found {
				return fmt.Errorf("%s: the mapping names %s, which the Pulumi schema of %s lacks", typ, resource.Token, pkg.plugin)
			}
			props := maps.Clone(object.Properties)
			maps.Copy(props, object.InputProperties)
			fields, err := walk(schema, typ+".", held.Provider.Resources[typ], resource.Fields, props)
			if err != nil {
				return err
			}
			bridged[typ] = bridgedType{token: resource.Token, fields: fields}
		}
	}
	for _, types := range priced {
		unbridged = append(unbridged, types...)
	}
	slices.Sort(unbridged)
	source, err := format.Source(render(bridged, unbridged))
	if err != nil {
		return err
	}
	return os.WriteFile(out, source, 0o644)
}

func fetch(plugin string) (*mapping, *pulumiSchema, error) {
	dir, err := os.MkdirTemp("", "pulumi-mapping")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "mapping.json")
	if output, err := tracing.Command(context.Background(), "pulumi", "package", "get-mapping", "terraform", plugin, "-o", path).CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("pulumi package get-mapping terraform %s: %w: %s", plugin, err, output)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	held := &mapping{}
	if err := json.Unmarshal(raw, held); err != nil {
		return nil, nil, fmt.Errorf("%s mapping: %w", plugin, err)
	}
	var stderr bytes.Buffer
	cmd := tracing.Command(context.Background(), "pulumi", "package", "get-schema", plugin)
	cmd.Stderr = &stderr
	raw, err = cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("pulumi package get-schema %s: %w: %s", plugin, err, stderr.Bytes())
	}
	schema := &pulumiSchema{}
	if err := json.Unmarshal(raw, schema); err != nil {
		return nil, nil, fmt.Errorf("%s schema: %w", plugin, err)
	}
	return held, schema, nil
}

func walk(schema *pulumiSchema, path string, terraform map[string]*terraformSchema, overrides map[string]*override, props map[string]pulumiProperty) (map[string]field, error) {
	out := map[string]field{}
	for _, name := range slices.Sorted(maps.Keys(terraform)) {
		held := terraform[name]
		key := pulumiName(name, terraform, overrides)
		prop, found := props[key]
		if !found {
			if name == "id" && strings.Count(path, ".") == 1 {
				continue
			}
			return nil, fmt.Errorf("%s%s: the bridge names it %s, which the Pulumi schema lacks", path, name, key)
		}
		var nested map[string]field
		if fields, nestedOverrides, isObject := object(held, overrides[name]); isObject {
			ref := prop.Ref
			if ref == "" && prop.Items != nil {
				ref = prop.Items.Ref
			}
			if typ, found := schema.Types[strings.TrimPrefix(ref, "#/types/")]; found {
				var err error
				if nested, err = walk(schema, path+name+".", fields, nestedOverrides, typ.Properties); err != nil {
					return nil, err
				}
			}
		}
		renamed := pulumi.DeriveTerraformName(key) != name
		if !renamed && len(nested) == 0 {
			continue
		}
		entry := field{fields: nested}
		if renamed {
			entry.terraform = name
		}
		out[key] = entry
	}
	return out, nil
}

func object(held *terraformSchema, own *override) (map[string]*terraformSchema, map[string]*override, bool) {
	if held.Element == nil || len(held.Element.Resource) == 0 {
		return nil, nil, false
	}
	switch held.Type {
	case typeList, typeSet:
		if own != nil && own.Element != nil {
			return held.Element.Resource, own.Element.Fields, true
		}
		return held.Element.Resource, nil, true
	case typeMap:
		if own != nil {
			return held.Element.Resource, own.Fields, true
		}
		return held.Element.Resource, nil, true
	}
	return nil, nil, false
}

func pulumiName(name string, terraform map[string]*terraformSchema, overrides map[string]*override) string {
	own := overrides[name]
	if own != nil && own.Name != "" {
		return own.Name
	}
	if isPluralized(terraform[name], own) {
		candidate := inflector.Pluralize(name)
		_, conflict := terraform[candidate]
		other := overrides[candidate]
		if !conflict || (other != nil && other.Name != "" && other.Name != candidate) {
			name = candidate
		}
	}
	return camel(name)
}

func isPluralized(held *terraformSchema, own *override) bool {
	if held == nil || (held.Type != typeList && held.Type != typeSet) {
		return false
	}
	if own != nil && own.MaxItemsOne != nil {
		return !*own.MaxItemsOne
	}
	return held.MaxItems != 1
}

func camel(name string) string {
	var out strings.Builder
	upper, started := false, false
	var previous rune
	for _, r := range name {
		if r == '_' && started {
			upper, previous = true, r
			continue
		}
		started = started || r != '_'
		if upper && r >= 'a' && r <= 'z' {
			r = unicode.ToUpper(r)
		}
		out.WriteRune(r)
		upper, previous = false, r
	}
	if previous == '_' {
		out.WriteRune('_')
	}
	return out.String()
}

func render(bridged map[string]bridgedType, unbridged []string) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by scripts/pricingbridge; DO NOT EDIT.\n\npackage pulumi\n\nvar bridged = map[string]bridgedType{\n")
	for _, typ := range slices.Sorted(maps.Keys(bridged)) {
		held := bridged[typ]
		fmt.Fprintf(&b, "%s: {token: %s", strconv.Quote(typ), strconv.Quote(held.token))
		if len(held.fields) > 0 {
			b.WriteString(", fields: ")
			renderFields(&b, held.fields)
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n\nvar unbridged = []string{")
	for _, typ := range unbridged {
		fmt.Fprintf(&b, "%s, ", strconv.Quote(typ))
	}
	b.WriteString("}\n")
	return b.Bytes()
}

func renderFields(b *bytes.Buffer, fields map[string]field) {
	b.WriteString("fieldNames{\n")
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		held := fields[name]
		fmt.Fprintf(b, "%s: {", strconv.Quote(name))
		if held.terraform != "" {
			fmt.Fprintf(b, "terraform: %s", strconv.Quote(held.terraform))
		}
		if len(held.fields) > 0 {
			if held.terraform != "" {
				b.WriteString(", ")
			}
			b.WriteString("fields: ")
			renderFields(b, held.fields)
		}
		b.WriteString("},\n")
	}
	b.WriteString("}")
}
