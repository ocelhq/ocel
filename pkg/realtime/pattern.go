package realtime

import (
	"fmt"
	"regexp"
	"strings"
)

const MaxSegments = 4

type Pattern struct {
	segments []segment
}

type segment struct {
	literal   string
	parameter string
}

var (
	channelSegment   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,48}[A-Za-z0-9])?$`)
	parameterSegment = regexp.MustCompile(`^:[A-Za-z_][A-Za-z0-9_]*$`)
)

const segmentRule = "letters, digits and -, at most 50 characters, starting and ending with a letter or digit"

func ParsePattern(written string) (Pattern, error) {
	if written == "" {
		return Pattern{}, fmt.Errorf("the pattern is empty: write one to %d segments joined by /, each a literal or a :parameter", MaxSegments)
	}
	parts := strings.Split(written, "/")
	if len(parts) > MaxSegments {
		return Pattern{}, fmt.Errorf("pattern %q has %d segments, and a channel pattern has at most %d", written, len(parts), MaxSegments)
	}
	var pattern Pattern
	named := map[string]bool{}
	for _, part := range parts {
		switch {
		case part == "":
			return Pattern{}, fmt.Errorf("pattern %q has an empty segment: it neither starts nor ends with /, and no two / are adjacent", written)
		case strings.HasPrefix(part, ":"):
			if !parameterSegment.MatchString(part) {
				return Pattern{}, fmt.Errorf("pattern %q has segment %q, which is no parameter: a parameter is : and a name of letters, digits and _ that starts with a letter or _", written, part)
			}
			name := part[1:]
			if named[name] {
				return Pattern{}, fmt.Errorf("pattern %q names parameter %q twice, so a channel could not say which value is which", written, name)
			}
			named[name] = true
			pattern.segments = append(pattern.segments, segment{parameter: name})
		case !channelSegment.MatchString(part):
			return Pattern{}, fmt.Errorf("pattern %q has segment %q, which is no literal: a literal is %s", written, part, segmentRule)
		default:
			pattern.segments = append(pattern.segments, segment{literal: part})
		}
	}
	return pattern, nil
}

func (p Pattern) HasParameters() bool {
	for _, s := range p.segments {
		if s.parameter != "" {
			return true
		}
	}
	return false
}

func (p Pattern) Overlaps(other Pattern) bool {
	return len(p.segments) == len(other.segments) && prefixesOverlap(p.segments, other.segments)
}

func (p Pattern) WildcardCovers(other Pattern) bool {
	for i, s := range p.segments {
		if s.parameter == "" {
			continue
		}
		if len(other.segments) > i && prefixesOverlap(p.segments[:i], other.segments[:i]) {
			return true
		}
	}
	return false
}

func prefixesOverlap(mine, theirs []segment) bool {
	for i := range mine {
		if mine[i].parameter == "" && theirs[i].parameter == "" && mine[i].literal != theirs[i].literal {
			return false
		}
	}
	return true
}
