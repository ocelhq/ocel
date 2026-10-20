package naming

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type TaggedName struct {
	Head     string
	Readable string
	Tag      string
}

func (n TaggedName) Fit(limit int, childSuffixes ...string) (string, error) {
	longest := longestChildSuffix(childSuffixes)
	room := limit - longest - len(n.Head) - len(n.Tag) - 2*len(WordSeparator)
	readable := strings.TrimRight(n.Readable[:max(0, min(len(n.Readable), room))], WordSeparator)
	name := strings.Join(nonEmpty([]string{n.Head, readable, n.Tag}), WordSeparator)
	if len(name)+longest > limit {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s does not fit %d characters with %d kept for what is appended to it, even with no readable part",
			name, limit, longest)
	}
	return name, nil
}
