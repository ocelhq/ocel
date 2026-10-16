package spelling

import (
	"fmt"
	"strings"
)

const maxEdits = 2

func FindClosest(value string, candidates []string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", false
	}
	if within, ok := findLongestPrefix(value, candidates); ok {
		return within, true
	}
	closest, fewest := "", maxEdits+1
	for _, candidate := range candidates {
		if edits := countEdits(value, strings.ToLower(candidate)); edits < fewest {
			closest, fewest = candidate, edits
		}
	}
	return closest, closest != ""
}

func SuggestClosest(value string, candidates []string) string {
	if closest, ok := FindClosest(value, candidates); ok {
		return fmt.Sprintf(" Did you mean %q?", closest)
	}
	return ""
}

func findLongestPrefix(value string, candidates []string) (string, bool) {
	longest := ""
	for _, candidate := range candidates {
		lowered := strings.ToLower(candidate)
		if len(lowered) < len(value) && strings.HasPrefix(value, lowered) && len(candidate) > len(longest) {
			longest = candidate
		}
	}
	return longest, longest != ""
}

func countEdits(a, b string) int {
	before, previous, current := make([]int, len(b)+1), make([]int, len(b)+1), make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			substitution := 1
			if a[i-1] == b[j-1] {
				substitution = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+substitution)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				current[j] = min(current[j], before[j-2]+1)
			}
		}
		before, previous, current = previous, current, before
	}
	return previous[len(b)]
}
