package english

import (
	"fmt"
	"strings"
)

func And(items []string) string {
	return joined(items, "and")
}

func Or(items []string) string {
	return joined(items, "or")
}

func Count(n int, noun string) string {
	return fmt.Sprintf("%d %s", n, Number(n, noun, noun+"s"))
}

func Number(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func Quoted(values []string) []string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	return quoted
}

func joined(items []string, conjunction string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conjunction + " " + items[len(items)-1]
}
