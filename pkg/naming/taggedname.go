package naming

import "strings"

type TaggedName struct {
	Head     string
	Readable string
	Tag      string
}

func (n TaggedName) Fit(limit int, childSuffixes ...string) string {
	room := limit - longestChildSuffix(childSuffixes) - len(n.Head) - len(n.Tag) - 2*len(WordSeparator)
	readable := strings.TrimRight(n.Readable[:max(0, min(len(n.Readable), room))], WordSeparator)
	return strings.Join(nonEmpty([]string{n.Head, readable, n.Tag}), WordSeparator)
}
