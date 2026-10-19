package shellword

import (
	"regexp"
	"strings"
)

var safe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=-]+$`)

func Quote(word string) string {
	if safe.MatchString(word) {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}
