package properties

import "unicode"

func Snake(s string) string {
	runes := []rune(s)
	out := make([]rune, 0, len(runes)+4)
	for i, r := range runes {
		if !unicode.IsUpper(r) {
			out = append(out, r)
			continue
		}
		if i > 0 {
			before := runes[i-1]
			afterLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(before) || unicode.IsDigit(before) || (unicode.IsUpper(before) && afterLower) {
				out = append(out, '_')
			}
		}
		out = append(out, unicode.ToLower(r))
	}
	return string(out)
}
