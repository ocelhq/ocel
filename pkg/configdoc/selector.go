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
	IDs       []string                    `json:"ids"`
	Shorthand []string                    `json:"shorthand"`
	Required  map[string][]ProviderOption `json:"required,omitempty"`
}

type ProviderOption struct {
	Name string `json:"name"`
	Doc  string `json:"doc"`
}

type selections struct {
	Provider selection            `json:"provider"`
	Edges    map[string]selection `json:"edges"`
	DNS      map[string]selection `json:"dns"`
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
		Provider: selection{IDs: append(slices.Clone(previous.Provider.IDs), provider), Shorthand: previous.Provider.Shorthand, Required: previous.Provider.Required},
		Edges:    maps.Clone(previous.Edges),
		DNS:      maps.Clone(previous.DNS),
	}
	known.Edges[provider] = selection{IDs: slices.Clone(edges), Shorthand: slices.Clone(edges)}
	known.DNS[provider] = selection{IDs: slices.Clone(dns), Shorthand: slices.Clone(dns)}
	return func() { known = previous }
}

func RequiredProviderOptions(id string) []ProviderOption {
	return slices.Clone(known.Provider.Required[id])
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

const (
	edgeKey = "edge"
	dnsKey  = "dns"
)

const edgeDoc = "The edge in front of the origin, keyed by its identifier with its options as the value, or named alone. Omit it for the provider's default: CloudFront on AWS, and no edge on GCP or a VPS."

const dnsDoc = "Where the project's hostname records are written, keyed by the DNS service's identifier with its options as the value, or named alone."

type ProviderDescriptor struct {
	Selector[json.RawMessage]
	Edge *EdgeDescriptor
	DNS  *DNSDescriptor
}

func (p *ProviderDescriptor) UnmarshalJSON(data []byte) error {
	if err := p.Selector.UnmarshalJSON(data); err != nil {
		return err
	}
	var options map[string]json.RawMessage
	if err := json.Unmarshal(p.Options, &options); err != nil {
		return err
	}
	if raw, set := options[edgeKey]; set {
		p.Edge = &EdgeDescriptor{}
		if err := json.Unmarshal(raw, p.Edge); err != nil {
			return err
		}
	}
	if raw, set := options[dnsKey]; set {
		p.DNS = &DNSDescriptor{}
		if err := json.Unmarshal(raw, p.DNS); err != nil {
			return err
		}
	}
	if p.Edge == nil && p.DNS == nil {
		return nil
	}
	delete(options, edgeKey)
	delete(options, dnsKey)
	rest, err := json.Marshal(options)
	if err != nil {
		return err
	}
	p.Options = rest
	return nil
}

func (ProviderDescriptor) checkShape(path string, value any) error {
	if err := checkSelector(path, value, "provider", known.Provider, reflect.TypeFor[json.RawMessage]()); err != nil {
		return err
	}
	keyed, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for id, options := range keyed {
		set, _ := options.(map[string]any)
		if err := refuseMissingOptions(JoinPath(path, id), set, known.Provider.Required[id]); err != nil {
			return err
		}
		if edge, named := set[edgeKey]; named {
			at := JoinPath(JoinPath(path, id), edgeKey)
			if err := refuseUnserved(at, id, "front deployments with", edge, known.Edges[id]); err != nil {
				return err
			}
			if err := checkSelector(at, edge, "edge", known.Edges[id], reflect.TypeFor[json.RawMessage]()); err != nil {
				return err
			}
		}
		if dns, named := set[dnsKey]; named {
			at := JoinPath(JoinPath(path, id), dnsKey)
			if err := refuseUnserved(at, id, "write hostname records with", dns, known.DNS[id]); err != nil {
				return err
			}
			if err := checkSelector(at, dns, "DNS service", known.DNS[id], reflect.TypeFor[DNSOptions]()); err != nil {
				return err
			}
		}
	}
	return nil
}

func refuseMissingOptions(path string, set map[string]any, required []ProviderOption) error {
	if set == nil {
		return nil
	}
	for _, option := range required {
		if set[option.Name] == nil {
			return fmt.Errorf("%s needs %q: %s", PathName(path), option.Name, strings.TrimSpace(option.Doc))
		}
	}
	return nil
}

func refuseUnserved(path, provider, serves string, value any, of selection) error {
	var id string
	switch typed := value.(type) {
	case string:
		id = typed
	case map[string]any:
		keys := keysOf(typed)
		if len(keys) != 1 {
			return nil
		}
		id = keys[0]
	default:
		return nil
	}
	if slices.Contains(of.IDs, id) {
		return nil
	}
	if len(of.IDs) == 0 {
		return fmt.Errorf("%s names %q, and %s can %s nothing — leave it out", PathName(path), id, provider, serves)
	}
	return fmt.Errorf("%s names %q, which %s cannot %s — name one of %s", PathName(path), id, provider, serves, strings.Join(of.IDs, ", "))
}

func (ProviderDescriptor) jsonSchema() object {
	options := object{
		"type": "object",
		"properties": object{
			edgeKey: describedAs(EdgeDescriptor{}.jsonSchema(), edgeDoc),
			dnsKey:  describedAs(DNSDescriptor{}.jsonSchema(), dnsDoc),
		},
	}
	return selectorSchema("ProviderDescriptor", options)
}

type EdgeDescriptor struct {
	Selector[json.RawMessage]
}

func (EdgeDescriptor) jsonSchema() object {
	return selectorSchema("EdgeDescriptor", schemaOf(reflect.TypeFor[json.RawMessage]()))
}

type DNSDescriptor struct {
	Selector[DNSOptions]
}

type DNSOptions struct {
	Zone string `json:"zone,omitempty" doc:"The zone the records are written into. Omit it and ocel picks the zone that covers the hostname."`
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
