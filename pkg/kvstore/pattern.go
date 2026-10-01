package kvstore

import (
	"fmt"
	"regexp"
	"strings"
)

type Pattern struct {
	segments []segment
}

type segment struct {
	literal   string
	parameter string
}

var (
	literalSegment   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	parameterSegment = regexp.MustCompile(`^:[A-Za-z_][A-Za-z0-9_]*$`)
)

func ParsePattern(written string) (Pattern, error) {
	if written == "" {
		return Pattern{}, fmt.Errorf("the pattern is empty: write one or more segments joined by /, each a literal or a :parameter")
	}
	if strings.ContainsAny(written, "{}") {
		return Pattern{}, fmt.Errorf("pattern %q holds { or }, which a store reserves as the hash tag syntax", written)
	}
	var pattern Pattern
	named := map[string]bool{}
	for _, part := range strings.Split(written, "/") {
		switch {
		case part == "":
			return Pattern{}, fmt.Errorf("pattern %q has an empty segment: it neither starts nor ends with /, and no two / are adjacent", written)
		case strings.HasPrefix(part, ":"):
			if !parameterSegment.MatchString(part) {
				return Pattern{}, fmt.Errorf("pattern %q has segment %q, which is no parameter: a parameter is : and a name of letters, digits and _ that starts with a letter or _", written, part)
			}
			name := part[1:]
			if named[name] {
				return Pattern{}, fmt.Errorf("pattern %q names parameter %q twice, so a key could not say which value is which", written, name)
			}
			named[name] = true
			pattern.segments = append(pattern.segments, segment{parameter: name})
		case !literalSegment.MatchString(part):
			return Pattern{}, fmt.Errorf("pattern %q has segment %q, which is no literal: a literal is letters, digits, ., _ and -", written, part)
		default:
			pattern.segments = append(pattern.segments, segment{literal: part})
		}
	}
	return pattern, nil
}

func (p Pattern) Overlaps(other Pattern) bool {
	if len(p.segments) != len(other.segments) {
		return false
	}
	for i, mine := range p.segments {
		theirs := other.segments[i]
		if mine.parameter == "" && theirs.parameter == "" && mine.literal != theirs.literal {
			return false
		}
	}
	return true
}
