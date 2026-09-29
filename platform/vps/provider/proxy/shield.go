package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const originDigestChars = 12

func (s Spec) ShieldOf(hostname string) (Shield, bool) {
	if at := slices.IndexFunc(s.Shields, func(shield Shield) bool {
		return strings.EqualFold(strings.TrimSpace(shield.Hostname), hostname)
	}); at >= 0 {
		return s.Shields[at], true
	}
	if at := slices.IndexFunc(s.Shields, func(shield Shield) bool {
		return isOneLabelUnder(strings.TrimSpace(shield.Hostname), hostname)
	}); at >= 0 {
		return s.Shields[at], true
	}
	return Shield{}, false
}

func isOneLabelUnder(wildcard, hostname string) bool {
	base, wild := strings.CutPrefix(strings.ToLower(wildcard), "*.")
	label, under := strings.CutSuffix(strings.ToLower(hostname), "."+base)
	return wild && under && label != "" && !strings.Contains(label, ".")
}

func (p CertificatePair) Bundle() []byte {
	certificate := p.Certificate
	if !strings.HasSuffix(certificate, "\n") {
		certificate += "\n"
	}
	return []byte(certificate + p.Key)
}

func (s Shield) OriginFileName() string {
	digest := sha256.Sum256(s.OriginCertificate.Bundle())
	hostname := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s.Hostname)), "*", "_")
	return switchboard.OriginPrefix + hostname + "-" + hex.EncodeToString(digest[:])[:originDigestChars] + ".pem"
}
