package securitygroup

import "strings"

const (
	MaxDescriptionLen = 255

	descriptionPunctuation = " ._-:/()#,@[]+=&;{}!$*"
)

func FitDescription(described string) string {
	fitted := strings.Map(func(r rune) rune {
		if isDescriptionCharacter(r) {
			return r
		}
		return -1
	}, described)
	if len(fitted) > MaxDescriptionLen {
		fitted = fitted[:MaxDescriptionLen]
	}
	return fitted
}

func isDescriptionCharacter(r rune) bool {
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune(descriptionPunctuation, r)
}
