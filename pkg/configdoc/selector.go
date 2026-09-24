package configdoc

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

//go:embed selectors.json
var selectorsJSON []byte

type selection struct {
	IDs       []string `json:"ids"`
	Shorthand []string `json:"shorthand"`
}

type selections struct {
	Provider selection `json:"provider"`
	Edge     selection `json:"edge"`
	DNS      selection `json:"dns"`
}

var known = mustSelections(selectorsJSON)

func mustSelections(data []byte) selections {
	var read selections
	if err := json.Unmarshal(data, &read); err != nil {
		panic(fmt.Sprintf("selectors.json is not what scripts/schema/build.mjs writes: %v", err))
	}
	return read
}

func ProviderIDs() []string { return slices.Clone(known.Provider.IDs) }

type Selector[O any, K selectorKind] struct {
	ID      string
	Options O
}

type selectorKind interface {
	title() string
	noun() string
	of() selection
}

type ProviderDescriptor = Selector[json.RawMessage, providerSelector]

type EdgeDescriptor = Selector[EdgeOptions, edgeSelector]

type EdgeOptions struct{}

type DnsDescriptor = Selector[DnsOptions, dnsSelector]

type DnsOptions struct {
	Zone string `json:"zone,omitempty" doc:"The zone the records are written into. Omit it and ocel picks the zone that covers the hostname."`
}

type providerSelector struct{}

func (providerSelector) title() string { return "ProviderDescriptor" }
func (providerSelector) noun() string  { return "provider" }
func (providerSelector) of() selection { return known.Provider }

type edgeSelector struct{}

func (edgeSelector) title() string { return "EdgeDescriptor" }
func (edgeSelector) noun() string  { return "edge" }
func (edgeSelector) of() selection { return known.Edge }

type dnsSelector struct{}

func (dnsSelector) title() string { return "DnsDescriptor" }
func (dnsSelector) noun() string  { return "DNS service" }
func (dnsSelector) of() selection { return known.DNS }

func (s *Selector[O, K]) UnmarshalJSON(data []byte) error {
	id, options, err := unmarshalSelector(data)
	if err != nil {
		return err
	}
	s.ID = id
	return json.Unmarshal(options, &s.Options)
}

func (Selector[O, K]) checkShape(path string, value any) error {
	var kind K
	return checkSelector(path, value, kind.noun(), kind.of(), reflect.TypeFor[O]())
}

func (Selector[O, K]) jsonSchema() object {
	var kind K
	return selectorSchema(kind.title(), schemaOf(reflect.TypeFor[O]()))
}

func selectorSchema(title string, options object) object {
	return object{
		"title": title,
		"oneOf": []any{
			object{"type": "string"},
			object{"type": "object", "minProperties": 1, "maxProperties": 1, "additionalProperties": options},
		},
	}
}

var noOptions = json.RawMessage("{}")

func unmarshalSelector(data []byte) (string, json.RawMessage, error) {
	var spelled string
	if err := json.Unmarshal(data, &spelled); err == nil {
		return spelled, noOptions, nil
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(data, &keyed); err != nil {
		return "", nil, err
	}
	ids := slices.Collect(maps.Keys(keyed))
	if len(ids) != 1 {
		return "", nil, errors.New("a selector is keyed by exactly one identifier")
	}
	options := keyed[ids[0]]
	if string(options) == "null" {
		options = noOptions
	}
	return ids[0], options, nil
}

func checkSelector(path string, value any, noun string, of selection, options reflect.Type) error {
	listed := strings.Join(of.IDs, ", ")
	switch held := value.(type) {
	case string:
		return checkNamedAlone(path, noun, held, of)
	case map[string]any:
		keys := slices.Sorted(mapKeys(held))
		switch len(keys) {
		case 0:
			return fmt.Errorf("%s is keyed by nothing — key it by one of %s", PathName(path), listed)
		case 1:
		default:
			return fmt.Errorf("%s is keyed by %s, and a project has one %s — keep one of %s", PathName(path), strings.Join(keys, " and "), noun, listed)
		}
		id := keys[0]
		if !slices.Contains(of.IDs, id) {
			return unknownSelection(path, noun, id, of)
		}
		if held[id] == nil {
			return checkNamedAlone(path, noun, id, of)
		}
		if _, ok := held[id].(map[string]any); !ok {
			return typeError(JoinPath(path, id), "an object of options")
		}
		return checkValue(JoinPath(path, id), options, held[id])
	default:
		if len(of.Shorthand) == 0 {
			return fmt.Errorf("%s must be an object keyed by one of %s", PathName(path), listed)
		}
		return fmt.Errorf("%s must be an object keyed by one of %s, or one of %s named alone", PathName(path), listed, strings.Join(of.Shorthand, ", "))
	}
}

func checkNamedAlone(path, noun, id string, of selection) error {
	if !slices.Contains(of.IDs, id) {
		return unknownSelection(path, noun, id, of)
	}
	if slices.Contains(of.Shorthand, id) {
		return nil
	}
	alone := ""
	if len(of.Shorthand) > 0 {
		alone = fmt.Sprintf("; only %s may be named alone", strings.Join(of.Shorthand, ", "))
	}
	return fmt.Errorf("%s names %q with no options, and %s cannot go without them — write { %q: { … } }%s", PathName(path), id, id, id, alone)
}

func unknownSelection(path, noun, id string, of selection) error {
	return fmt.Errorf("%s names %q, and ocel knows no such %s — name one of %s", PathName(path), id, noun, strings.Join(of.IDs, ", "))
}
