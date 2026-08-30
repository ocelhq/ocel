package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	FieldSeparator = "--"
	WordSeparator  = "-"
	PathSeparator  = "/"
	KeySeparator   = "#"

	truncationMarker = "-x"
	truncationDigits = 8
	truncationLen    = len(truncationMarker) + truncationDigits
)

var (
	ingestPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)
)

func IsRepositorySegment(value string) bool {
	return repositoryPattern.MatchString(value)
}

func RepositorySegment(repository string) string {
	return repository[strings.LastIndex(repository, PathSeparator)+1:]
}

func DigestTag(digest string) string {
	return strings.Replace(digest, ":", WordSeparator, 1)
}

func Validate(field, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("%s is required", field)
	case !ingestPattern.MatchString(value):
		return fmt.Errorf("%s %q must be lowercase letters, digits and single hyphens, starting and ending with a letter or digit", field, value)
	case strings.Contains(value, FieldSeparator):
		return fmt.Errorf("%s %q contains %q, which separates fields", field, value, FieldSeparator)
	}
	return nil
}

func Sanitize(value string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(value) {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			r = '-'
		}
		if r == '-' {
			if prevDash || b.Len() == 0 {
				continue
			}
			prevDash = true
			b.WriteRune('-')
			continue
		}
		prevDash = false
		b.WriteRune(r)
	}
	out := strings.TrimRight(b.String(), WordSeparator)
	if out == "" {
		return "x"
	}
	return out
}

func SanitizeAlpha(value string) string {
	out := Sanitize(value)
	if first := out[0]; first < 'a' || first > 'z' {
		return "a" + out
	}
	return out
}

func Underscore(value string) string {
	return strings.ReplaceAll(Sanitize(value), WordSeparator, "_")
}

type Segment struct {
	Value    string
	Priority int
}

func Fixed(value string) Segment {
	return Segment{Value: value, Priority: fixedPriority}
}

func Compressible(value string) Segment {
	return Segment{Value: value, Priority: 0}
}

const fixedPriority = 1 << 20

func Join(separator string, values ...string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		parts = append(parts, Sanitize(v))
	}
	return strings.Join(parts, separator)
}

func Fit(max int, separator string, segments ...Segment) string {
	values := make([]string, len(segments))
	for i, s := range segments {
		if s.Value == "" {
			continue
		}
		values[i] = Sanitize(s.Value)
	}
	full := strings.Join(nonEmpty(values), separator)
	if len(full) <= max {
		return full
	}

	marker := truncationMarker + digest(full)
	budget := max - len(marker)
	for _, i := range trimOrder(segments) {
		if length(values, separator) <= budget {
			break
		}
		over := length(values, separator) - budget
		keep := len(values[i]) - over
		if keep < 1 {
			keep = 0
		}
		values[i] = strings.TrimRight(values[i][:keep], WordSeparator)
	}
	trimmed := strings.Join(nonEmpty(values), separator)
	if len(trimmed) > budget {
		if budget < 0 {
			budget = 0
		}
		trimmed = strings.TrimRight(trimmed[:budget], WordSeparator)
	}
	return trimmed + marker
}

func trimOrder(segments []Segment) []int {
	order := make([]int, len(segments))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a], order[b]
		if segments[x].Priority != segments[y].Priority {
			return segments[x].Priority < segments[y].Priority
		}
		return x > y
	})
	return order
}

func length(values []string, separator string) int {
	return len(strings.Join(nonEmpty(values), separator))
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:truncationDigits]
}
