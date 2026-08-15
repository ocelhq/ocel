package live

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
)

const FilePath = ".ocel/variables.live.json"

type Manifest struct {
	Slug        string `json:"slug"`
	Table       string `json:"table"`
	KeyARN      string `json:"keyArn"`
	Class       string `json:"class"`
	Environment string `json:"environment,omitempty"`
	Keys        []Key  `json:"keys"`
	Links       []Link `json:"links,omitempty"`
}

type Key struct {
	Key    string `json:"key"`
	Folder string `json:"folder,omitempty"`
}

type Link struct {
	Name       string   `json:"name"`
	Key        string   `json:"key"`
	Type       string   `json:"type"`
	Properties []string `json:"properties,omitempty"`
}

type Record struct {
	Type       string            `json:"type"`
	Properties map[string]string `json:"properties"`
}

var ErrDrift = errors.New("link record drift")

func EncodeRecord(r Record) string {
	if r.Properties == nil {
		r.Properties = map[string]string{}
	}
	encoded, _ := json.Marshal(r)
	return string(encoded)
}

func Conform(links []Link, values map[string]string) (map[string]string, error) {
	if len(links) == 0 {
		return values, nil
	}
	out := maps.Clone(values)
	for _, l := range links {
		raw, ok := values[l.Key]
		if !ok {
			return nil, fmt.Errorf("%w: link %s published no record under %s, which this deployment reads as a %s", ErrDrift, l.Name, l.Key, l.Type)
		}
		properties, err := l.conform(raw)
		if err != nil {
			return nil, err
		}
		out[l.Key] = properties
	}
	return out, nil
}

func (l Link) conform(raw string) (string, error) {
	var r Record
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return "", fmt.Errorf("%w: link %s published something under %s that is not a link record at all, and this deployment was built to read a %s", ErrDrift, l.Name, l.Key, l.Type)
	}
	if r.Type == "" {
		return "", fmt.Errorf("%w: link %s published a record under %s that names no type, and this deployment was built to read a %s", ErrDrift, l.Name, l.Key, l.Type)
	}
	if r.Type != l.Type {
		return "", fmt.Errorf("%w: link %s publishes a %s record under %s, and this deployment was built to read a %s", ErrDrift, l.Name, r.Type, l.Key, l.Type)
	}
	for _, want := range l.Properties {
		if _, ok := r.Properties[want]; !ok {
			return "", fmt.Errorf("%w: link %s's %s record carries no %q under %s, and this deployment was built to read %s", ErrDrift, l.Name, r.Type, want, l.Key, strings.Join(l.Properties, ", "))
		}
	}
	properties := r.Properties
	if properties == nil {
		properties = map[string]string{}
	}
	encoded, _ := json.Marshal(properties)
	return string(encoded), nil
}

func Render(m Manifest) ([]byte, error) {
	if len(m.Keys) == 0 && len(m.Links) == 0 {
		return nil, nil
	}
	for _, component := range []struct{ name, value string }{
		{"project slug", m.Slug},
		{"variable table", m.Table},
		{"variable key", m.KeyARN},
		{"environment class", m.Class},
	} {
		if component.value == "" {
			return nil, fmt.Errorf("the live-value manifest names %d keys but no %s", len(m.Keys)+len(m.Links), component.name)
		}
	}
	return json.Marshal(m)
}

func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", FilePath, err)
	}
	return m, nil
}
