package live

import (
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
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
	Name    string           `json:"name"`
	Key     string           `json:"key"`
	Type    linksv1.LinkType `json:"type"`
	Granted int64            `json:"granted,omitempty"`
}

func (l Link) MarshalJSON() ([]byte, error) {
	type wire Link
	return json.Marshal(struct {
		wire
		Type string `json:"type"`
	}{wire(l), l.Type.String()})
}

func (l *Link) UnmarshalJSON(data []byte) error {
	type wire Link
	var decoded struct {
		wire
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value, ok := linksv1.LinkType_value[decoded.Type]
	if !ok {
		return fmt.Errorf("link %s names type %q, which no link type is called", decoded.Name, decoded.Type)
	}
	*l = Link(decoded.wire)
	l.Type = linksv1.LinkType(value)
	return nil
}

var ErrDrift = errors.New("link record drift")

func Conform(links []Link, values map[string]string) error {
	for _, l := range links {
		raw, ok := values[l.Key]
		if !ok {
			return fmt.Errorf("%w: link %s published no record under %s, which this deployment reads as a %s", ErrDrift, l.Name, l.Key, l.Type)
		}
		if err := l.conform(raw); err != nil {
			return err
		}
	}
	return nil
}

func (l Link) conform(raw string) error {
	link := &linksv1.Link{}
	if err := protojson.Unmarshal([]byte(raw), link); err != nil {
		return fmt.Errorf("%w: link %s published something under %s that is not a link record at all, and this deployment was built to read a %s", ErrDrift, l.Name, l.Key, l.Type)
	}
	published := naming.LinkTypeOf(link)
	if published == linksv1.LinkType_LINK_TYPE_UNSPECIFIED {
		return fmt.Errorf("%w: link %s published a record under %s that carries no properties, and this deployment was built to read a %s", ErrDrift, l.Name, l.Key, l.Type)
	}
	if published != l.Type {
		return fmt.Errorf("%w: link %s publishes a %s record under %s, and this deployment was built to read a %s", ErrDrift, l.Name, published, l.Key, l.Type)
	}
	return nil
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
