package target

import (
	"fmt"
	"regexp"
	"strings"
)

const shaped = "a target's parts carry lower-case letters, digits, dot, underscore, colon and dash, and nothing else"

var segmentShape = regexp.MustCompile(`^[a-z0-9._:-]+$`)

func Fingerprint(vendor string, parts ...string) (string, error) {
	if vendor == "" {
		return "", fmt.Errorf("a target names the vendor whose account it fingerprints")
	}
	if !segmentShape.MatchString(vendor) {
		return "", fmt.Errorf("%q is not a vendor a target can name: %s", vendor, shaped)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("a %s target names at least one part of the account it fingerprints", vendor)
	}
	for at, part := range parts {
		if part == "" {
			return "", fmt.Errorf("part %d of this %s target is empty", at+1, vendor)
		}
		if !segmentShape.MatchString(part) {
			return "", fmt.Errorf("%q cannot stand in a %s target: %s", part, vendor, shaped)
		}
	}
	return vendor + "/" + strings.Join(parts, "/"), nil
}
