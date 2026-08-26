package host

import (
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	StateApplying = "applying"
	StateComplete = "complete"
)

type Stamp struct {
	Schema  int               `json:"schema"`
	State   string            `json:"state"`
	Writer  string            `json:"writer"`
	Seal    Seal              `json:"seal"`
	Digests map[string]string `json:"digests"`
}

func (s Stamp) item(class providerkit.Class) (Item, error) {
	written, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return Item{}, err
	}
	return Item{
		Kind:    KindFile,
		Name:    StampPath(class),
		Mode:    0o644,
		Owner:   rootOwner,
		Content: append(written, '\n'),
	}, nil
}

func (s Stamp) records(items []Item) bool {
	for _, item := range items {
		if s.Digests[item.ID()] != item.Digest() {
			return false
		}
	}
	return len(s.Digests) == len(items)
}
