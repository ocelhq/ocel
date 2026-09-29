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

func AddKnownIDs(provider string, edges, dns []string) (restore func()) {
	previous := known
	known = selections{
		Provider: selection{IDs: append(slices.Clone(previous.Provider.IDs), provider), Shorthand: previous.Provider.Shorthand},
		Edge:     selection{IDs: slices.Concat(previous.Edge.IDs, edges), Shorthand: slices.Concat(previous.Edge.Shorthand, edges)},
		DNS:      selection{IDs: slices.Concat(previous.DNS.IDs, dns), Shorthand: slices.Concat(previous.DNS.Shorthand, dns)},
	}
	return func() { known = previous }
}

func ProviderNamedAlone(id string) bool { return slices.Contains(known.Provider.Shorthand, id) }

type Selector[O any] struct {
	ID      string
	Options O
}

func (s *Selector[O]) UnmarshalJSON(data []byte) error {
	id, options, err := unmarshalSelector(data)
	if err != nil {
		return err
	}
	s.ID = id
	return json.Unmarshal(options, &s.Options)
}

type ProviderDescriptor struct {
	Selector[json.RawMessage]
}

func (ProviderDescriptor) checkShape(path string, value any) error {
	return checkSelector(path, value, "provider", known.Provider, reflect.TypeFor[json.RawMessage]())
}

func (ProviderDescriptor) jsonSchema() object {
	return selectorSchema("ProviderDescriptor", schemaOf(reflect.TypeFor[json.RawMessage]()))
}

type EdgeDescriptor struct {
	Selector[EdgeOptions]
}

type EdgeOptions struct{}

func (EdgeDescriptor) checkShape(path string, value any) error {
	return checkSelector(path, value, "edge", known.Edge, reflect.TypeFor[EdgeOptions]())
}

func (EdgeDescriptor) jsonSchema() object {
	return selectorSchema("EdgeDescriptor", schemaOf(reflect.TypeFor[EdgeOptions]()))
}

type DNSDescriptor struct {
	Selector[DNSOptions]
}

type DNSOptions struct {
	Zone string `json:"zone,omitempty" doc:"The zone the records are written into. Omit it and ocel picks the zone that covers the hostname."`
}

func (DNSDescriptor) checkShape(path string, value any) error {
	return checkSelector(path, value, "DNS service", known.DNS, reflect.TypeFor[DNSOptions]())
}

func (DNSDescriptor) jsonSchema() object {
	return selectorSchema("DNSDescriptor", schemaOf(reflect.TypeFor[DNSOptions]()))
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
	return ids[0], keyed[ids[0]], nil
}

func checkSelector(path string, value any, noun string, of selection, options reflect.Type) error {
	listed := strings.Join(of.IDs, ", ")
	switch typed := value.(type) {
	case string:
		return checkNamedAlone(path, noun, typed, of)
	case map[string]any:
		keys := keysOf(typed)
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
		if _, ok := typed[id].(map[string]any); !ok {
			return typeError(JoinPath(path, id), "an object of options")
		}
		return checkValue(JoinPath(path, id), options, typed[id])
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
