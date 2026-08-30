package certs

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	CertificatesPerDomain = 50
	CertificateWindow     = 7 * 24 * time.Hour
)

type RateLimit struct {
	Domain     string
	Ceiling    int
	ResetAt    time.Time
	RetryAfter time.Duration
}

var (
	limitedBy    = regexp.MustCompile(`(?i)too many certificates(?: \((\d+)\))? already issued for "([^"]+)"`)
	resetAtBy    = regexp.MustCompile(`(?i)retry after (\d{4}-\d{2}-\d{2}T[0-9:.]+(?:Z|[+-]\d{2}:\d{2}))`)
	retryInBy    = regexp.MustCompile(`(?i)retry-after"?\s*[:=]\s*"?(\d+)`)
	rateLimitURN = "urn:ietf:params:acme:error:ratelimited"
)

func RateLimited(said string) (RateLimit, bool) {
	plain := strings.ReplaceAll(said, `\"`, `"`)
	if !strings.Contains(strings.ToLower(plain), rateLimitURN) {
		return RateLimit{}, false
	}
	named := limitedBy.FindStringSubmatch(plain)
	if named == nil {
		return RateLimit{}, false
	}
	limit := RateLimit{Domain: named[2], Ceiling: CertificatesPerDomain}
	if named[1] != "" {
		if ceiling, err := strconv.Atoi(named[1]); err == nil {
			limit.Ceiling = ceiling
		}
	}
	if at := resetAtBy.FindStringSubmatch(plain); at != nil {
		if reset, err := time.Parse(time.RFC3339, at[1]); err == nil {
			limit.ResetAt = reset
		}
	}
	if in := retryInBy.FindStringSubmatch(plain); in != nil {
		if seconds, err := strconv.Atoi(in[1]); err == nil {
			limit.RetryAfter = time.Duration(seconds) * time.Second
		}
	}
	return limit, true
}

func (r RateLimit) Covers(hostname string) bool {
	named, under := strings.ToLower(hostname), strings.ToLower(r.Domain)
	return under != "" && (named == under || strings.HasSuffix(named, "."+under))
}

func (r RateLimit) Spent(now time.Time) bool {
	return !r.ResetAt.IsZero() && now.After(r.ResetAt)
}

func (r RateLimit) Refusal(hostname string) error {
	return providerkit.Refuse(providerkit.CodeBusy,
		"the certificate authority will not issue for %s yet: %s has had its %d certificates for the last %s and %s. Every preview hostname on this box counts against %s, and so does every production one, so the ceiling is new branches times apps per project per week rather than deploys. Take previews you are done with down with `ocel preview rm` — that frees the names but not the count, which refills on its own.",
		hostname, r.Domain, r.Ceiling, spelledWindow(CertificateWindow), r.resets(), r.Domain)
}

func (r RateLimit) resets() string {
	switch {
	case !r.ResetAt.IsZero():
		return "the next one can be ordered after " + r.ResetAt.UTC().Format(time.RFC3339)
	case r.RetryAfter > 0:
		return "the next one can be ordered in " + r.RetryAfter.String()
	default:
		return "it named no time it resets, which it refills at roughly one certificate every 202 minutes"
	}
}

func spelledWindow(window time.Duration) string {
	return strconv.Itoa(int(window/(24*time.Hour))) + " days"
}
