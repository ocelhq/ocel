package gateway

import (
	"regexp"
	"strings"
)

var channelSegment = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,48}[A-Za-z0-9])?$`)

const maxChannelSegments = 5

func readPublishNamespace(channel string) (string, bool) {
	segments, isRooted := splitChannel(channel)
	if !isRooted || !areChannelSegments(segments) {
		return "", false
	}
	return segments[0], true
}

func readSubscribeNamespace(channel string) (string, bool) {
	segments, isRooted := splitChannel(channel)
	if !isRooted {
		return "", false
	}
	if segments[len(segments)-1] == "*" {
		segments = segments[:len(segments)-1]
	}
	if !areChannelSegments(segments) {
		return "", false
	}
	return segments[0], true
}

func splitChannel(channel string) ([]string, bool) {
	rest, isRooted := strings.CutPrefix(channel, "/")
	if !isRooted {
		return nil, false
	}
	segments := strings.Split(rest, "/")
	if len(segments) < 2 || len(segments) > maxChannelSegments {
		return nil, false
	}
	return segments, true
}

func areChannelSegments(segments []string) bool {
	for _, segment := range segments {
		if !channelSegment.MatchString(segment) {
			return false
		}
	}
	return true
}
