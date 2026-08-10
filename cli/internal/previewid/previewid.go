package previewid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

const SourceGit = "git"

const hashLen = 8

const maxLabelLen = 63

const maxKeyLen = maxLabelLen

const maxBaseLen = maxKeyLen - 1 - hashLen

type Identity struct {
	Key    string
	Label  string
	Source string
}

func Resolve(ref string, prNumber string) (Identity, error) {
	if ref == "" {
		return Identity{}, fmt.Errorf("previewid: empty ref")
	}

	base := sanitize(ref)

	switch {
	case base == "":
		base = "env"
	case !(base[0] >= 'a' && base[0] <= 'z'):
		base = "env-" + base
	}
	if len(base) > maxBaseLen {
		base = strings.Trim(base[:maxBaseLen], "-")
	}

	sum := sha256.Sum256([]byte(ref))
	hash := hex.EncodeToString(sum[:])[:hashLen]

	key := base + "-" + hash

	label := ""
	if prNumber != "" {
		label = "pr-" + prNumber
	}

	return Identity{Key: key, Label: label, Source: SourceGit}, nil
}

func sanitize(ref string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(ref) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

var dnsLabelPattern = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

const appSeparator = "--"

func ValidateLabel(s string) error {
	if !dnsLabelPattern.MatchString(s) {
		return fmt.Errorf("invalid preview name %q: use a DNS-label-safe name (lowercase letters, digits and hyphens)", s)
	}
	if strings.Contains(s, appSeparator) {
		return fmt.Errorf("invalid preview name %q: %q separates the preview from the app in the hostname it is served on (\"<preview>%s<app>\"), so a name may not contain it — use a single hyphen", s, appSeparator, appSeparator)
	}
	if len(s) > maxKeyLen {
		return fmt.Errorf("preview name %q is too long: the limit is %d characters, the DNS label it is served on", s, maxKeyLen)
	}
	return nil
}
