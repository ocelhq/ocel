package properties

import "strings"

func ZoneRegion(zone string) string {
	if at := strings.LastIndex(zone, "-"); at > 0 && len(zone)-at == 2 && isLetter(zone[at+1]) {
		return zone[:at]
	}
	if n := len(zone); n > 1 && isLetter(zone[n-1]) && isDigit(zone[n-2]) {
		return zone[:n-1]
	}
	return zone
}

func LocationRegion(location string) (string, bool) {
	if !strings.ContainsAny(location, "0123456789") {
		return "", false
	}
	return ZoneRegion(location), true
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' }

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
