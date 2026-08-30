package certs

import (
	"crypto/x509"
	"encoding/pem"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	ProxyScheme = "proxy:"
	PinScheme   = "pem:"
)

const (
	ProxyRenewal = "the proxy on this box obtained it over http-01 and renews it; ocel issues and renews nothing here"
	PinRenewal   = "you placed it on this box and you renew it; ocel issues and renews nothing here"
)

const RenewalWindow = 30 * 24 * time.Hour

func ProxyHandle(hostname string) string { return ProxyScheme + hostname }

func PinHandle(path string) string { return PinScheme + path }

func Serving(id string) (string, bool) { return strings.CutPrefix(id, ProxyScheme) }

func Pinned(id string) (string, bool) { return strings.CutPrefix(id, PinScheme) }

func Renewal(id string) string {
	if _, pinned := Pinned(id); pinned {
		return PinRenewal
	}
	return ProxyRenewal
}

type Leaf struct {
	Domains  []string
	NotAfter time.Time
}

func Parse(what string, block []byte) (Leaf, error) {
	for rest := block; len(rest) > 0; {
		var found *pem.Block
		if found, rest = pem.Decode(rest); found == nil {
			break
		}
		if found.Type != "CERTIFICATE" {
			continue
		}
		parsed, err := x509.ParseCertificate(found.Bytes)
		if err != nil {
			return Leaf{}, providerkit.Refuse(providerkit.CodeInvalid,
				"%s holds a certificate block this host cannot read as one: %v", what, err)
		}
		return Leaf{Domains: names(parsed), NotAfter: parsed.NotAfter}, nil
	}
	return Leaf{}, providerkit.Refuse(providerkit.CodeInvalid,
		"%s carries no pem certificate block, and ocel reads the certificate to report on it and never the key beside it", what)
}

func names(parsed *x509.Certificate) []string {
	found := slices.Clone(parsed.DNSNames)
	if len(found) == 0 && parsed.Subject.CommonName != "" {
		found = []string{parsed.Subject.CommonName}
	}
	slices.Sort(found)
	return slices.Compact(found)
}

func (l Leaf) Covers(hostname string) bool {
	return slices.ContainsFunc(l.Domains, func(name string) bool { return covers(name, hostname) })
}

func covers(name, hostname string) bool {
	if strings.EqualFold(name, hostname) {
		return true
	}
	under, wild := strings.CutPrefix(name, "*.")
	if !wild {
		return false
	}
	label, rest, split := strings.Cut(hostname, ".")
	return split && label != "" && label != "*" && strings.EqualFold(under, rest)
}

func (l Leaf) Expired(now time.Time) bool {
	return !l.NotAfter.IsZero() && !now.Before(l.NotAfter)
}

func (l Leaf) ExpiringSoon(now time.Time) bool {
	return !l.NotAfter.IsZero() && now.Add(RenewalWindow).After(l.NotAfter)
}

func (l Leaf) ExpiresAt() int64 {
	if l.NotAfter.IsZero() {
		return 0
	}
	return l.NotAfter.Unix()
}

func Verify(path, hostname string, leaf Leaf, now time.Time) error {
	switch {
	case !leaf.Covers(hostname):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the certificate pinned for %s at %s covers %s and not %s, and this box serves a hostname only under a certificate that names it",
			hostname, path, strings.Join(leaf.Domains, ", "), hostname)
	case leaf.Expired(now):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the certificate pinned for %s at %s expired on %s, and nothing on this box renews it: you placed it and you replace it",
			hostname, path, leaf.NotAfter.UTC().Format(time.RFC3339))
	default:
		return nil
	}
}
