package origin

import "encoding/json"

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
