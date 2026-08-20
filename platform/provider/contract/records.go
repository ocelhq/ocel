package provider

import (
	"context"
	"encoding/json"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Record struct {
	Deploy   State           `json:"deploy"`
	EdgeKind edge.Kind       `json:"edgeKind"`
	Edge     edge.StackState `json:"edge"`
	Hosts    Private         `json:"hosts,omitzero"`
}

type RecordStore interface {
	Read(ctx context.Context, slug string, class edge.Class) (Record, bool, error)
	Write(ctx context.Context, slug string, class edge.Class, record Record) error
	Delete(ctx context.Context, slug string, class edge.Class) error
	Slugs(ctx context.Context, class edge.Class) ([]string, error)
}

type Private struct {
	value any
	raw   json.RawMessage
}

func Own(value any) Private { return Private{value: value} }

func (p Private) IsZero() bool { return p.value == nil && len(p.raw) == 0 }

func (p Private) Into(target any) error {
	if p.value != nil {
		b, err := json.Marshal(p.value)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, target)
	}
	if len(p.raw) == 0 {
		return nil
	}
	return json.Unmarshal(p.raw, target)
}

func (p Private) MarshalJSON() ([]byte, error) {
	if p.value != nil {
		return json.Marshal(p.value)
	}
	if len(p.raw) == 0 {
		return []byte("null"), nil
	}
	return p.raw, nil
}

func (p *Private) UnmarshalJSON(raw []byte) error {
	p.value = nil
	p.raw = append(json.RawMessage(nil), raw...)
	return nil
}
