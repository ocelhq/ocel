package ocel

import (
	"encoding/base32"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	maxChannelSegments   = 4
	maxChannelValueBytes = 30
	channelSegmentRule   = "letters, digits and -, at most 50 characters, starting and ending with a letter or digit"
)

var (
	channelSegment   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,48}[A-Za-z0-9])?$`)
	channelParameter = regexp.MustCompile(`^:[A-Za-z_][A-Za-z0-9_]*$`)
	lowerBase32      = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
)

type channelSegmentPart struct {
	literal   string
	parameter string
}

type channelPattern struct {
	written  string
	segments []channelSegmentPart
}

func parseChannelPattern(written string) (channelPattern, error) {
	if written == "" {
		return channelPattern{}, fmt.Errorf("the pattern is empty: write one to %d segments joined by /, each a literal or a :parameter", maxChannelSegments)
	}
	parts := strings.Split(written, "/")
	if len(parts) > maxChannelSegments {
		return channelPattern{}, fmt.Errorf("pattern %q has %d segments, and a channel pattern has at most %d", written, len(parts), maxChannelSegments)
	}
	pattern := channelPattern{written: written}
	named := map[string]bool{}
	for _, part := range parts {
		switch {
		case part == "":
			return channelPattern{}, fmt.Errorf("pattern %q has an empty segment: it neither starts nor ends with /, and no two / are adjacent", written)
		case strings.HasPrefix(part, ":"):
			if !channelParameter.MatchString(part) {
				return channelPattern{}, fmt.Errorf("pattern %q has segment %q, which is no parameter: a parameter is : and a name of letters, digits and _ that starts with a letter or _", written, part)
			}
			name := part[1:]
			if named[name] {
				return channelPattern{}, fmt.Errorf("pattern %q names parameter %q twice, so a channel could not say which value is which", written, name)
			}
			named[name] = true
			pattern.segments = append(pattern.segments, channelSegmentPart{parameter: name})
		case !channelSegment.MatchString(part):
			return channelPattern{}, fmt.Errorf("pattern %q has segment %q, which is no literal: a literal is %s", written, part, channelSegmentRule)
		default:
			pattern.segments = append(pattern.segments, channelSegmentPart{literal: part})
		}
	}
	return pattern, nil
}

func (p channelPattern) listParameters() []string {
	var names []string
	for _, segment := range p.segments {
		if segment.parameter != "" {
			names = append(names, segment.parameter)
		}
	}
	return names
}

func encodeChannelValue(value string) string {
	if channelSegment.MatchString(value) && !strings.HasPrefix(value, "0z") {
		return value
	}
	return "0z" + lowerBase32.EncodeToString([]byte(value))
}

func encodeWireChannel(namespace string, pattern channelPattern, params map[string]string, wildcard bool) (channel string, refused denialCode) {
	names := pattern.listParameters()
	for name := range params {
		if !slices.Contains(names, name) {
			return "", denialUnknownParam
		}
	}
	parts := []string{"", namespace}
	for i, segment := range pattern.segments {
		if segment.parameter == "" {
			parts = append(parts, segment.literal)
			continue
		}
		value, given := params[segment.parameter]
		switch {
		case !given:
			if !wildcard {
				return "", denialMissingParam
			}
			for _, later := range pattern.segments[i:] {
				if _, laterGiven := params[later.parameter]; later.parameter != "" && laterGiven {
					return "", denialMissingParam
				}
			}
			return strings.Join(append(parts, "*"), "/"), ""
		case value == "":
			return "", denialEmptyValue
		case len(value) > maxChannelValueBytes:
			return "", denialValueTooLong
		}
		parts = append(parts, encodeChannelValue(value))
	}
	return strings.Join(parts, "/"), ""
}
