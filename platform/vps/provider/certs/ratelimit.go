package certs

import (
	"fmt"
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

const retryAfterCeiling = 30 * 24 * time.Hour

type Counted string

const (
	CountedCertificates   Counted = "certificates"
	CountedAuthorizations Counted = "authorizations"
	CountedOrders         Counted = "orders"
)

type RateLimit struct {
	Counts     Counted
	Domain     string
	Ceiling    int
	Window     time.Duration
	Observed   time.Time
	ResetAt    time.Time
	RetryAfter time.Duration
}

var ceilings = []struct {
	counts  Counted
	named   *regexp.Regexp
	ceiling int
	window  time.Duration
}{
	{CountedCertificates,
		regexp.MustCompile(`(?i)too many certificates(?: \((?P<ceiling>\d+)\))? already issued for "(?P<domain>[^"]+)"`),
		CertificatesPerDomain, CertificateWindow},
	{CountedAuthorizations,
		regexp.MustCompile(`(?i)too many failed authorizations(?: \((?P<ceiling>\d+)\))?(?: recently)?(?: for "(?P<domain>[^"]+)")?`),
		5, time.Hour},
	{CountedOrders,
		regexp.MustCompile(`(?i)too many new orders(?: \((?P<ceiling>\d+)\))?`),
		300, 3 * time.Hour},
}

var (
	resetAtBy    = regexp.MustCompile(`(?i)retry after (\d{4}-\d{2}-\d{2}T[0-9:.]+(?:Z|[+-]\d{2}:\d{2}))`)
	retryInBy    = regexp.MustCompile(`(?i)retry-after"?\s*[:=]\s*"?(\d+)`)
	rateLimitURN = "urn:ietf:params:acme:error:ratelimited"
)

func RateLimited(said string) (RateLimit, bool) {
	for _, line := range strings.Split(said, "\n") {
		if limit, held := rateLimitedOn(line); held {
			return limit, true
		}
	}
	return RateLimit{}, false
}

func rateLimitedOn(line string) (RateLimit, bool) {
	observed, said := timestamped(line)
	plain := strings.ReplaceAll(said, `\"`, `"`)
	if !strings.Contains(strings.ToLower(plain), rateLimitURN) {
		return RateLimit{}, false
	}
	for _, ceiling := range ceilings {
		named := ceiling.named.FindStringSubmatch(plain)
		if named == nil {
			continue
		}
		limit := RateLimit{
			Counts:   ceiling.counts,
			Domain:   group(ceiling.named, named, "domain"),
			Ceiling:  ceiling.ceiling,
			Window:   ceiling.window,
			Observed: observed,
		}
		if stated, err := strconv.Atoi(group(ceiling.named, named, "ceiling")); err == nil {
			limit.Ceiling = stated
		}
		if at := resetAtBy.FindStringSubmatch(plain); at != nil {
			if reset, err := time.Parse(time.RFC3339, at[1]); err == nil {
				limit.ResetAt = reset
			}
		}
		if in := retryInBy.FindStringSubmatch(plain); in != nil {
			limit.RetryAfter = waited(in[1])
		}
		return limit, true
	}
	return RateLimit{}, false
}

func group(named *regexp.Regexp, matched []string, field string) string {
	at := named.SubexpIndex(field)
	if at < 0 || at >= len(matched) {
		return ""
	}
	return matched[at]
}

func timestamped(line string) (time.Time, string) {
	at, said, cut := strings.Cut(strings.TrimSpace(line), " ")
	if !cut {
		return time.Time{}, line
	}
	spoken, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, line
	}
	return spoken, said
}

func waited(seconds string) time.Duration {
	held, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil || held > int64(retryAfterCeiling/time.Second) {
		return retryAfterCeiling
	}
	if held < 0 {
		return 0
	}
	return time.Duration(held) * time.Second
}

func (r RateLimit) Covers(hostname string) bool {
	named, under := strings.ToLower(hostname), strings.ToLower(r.Domain)
	switch {
	case r.Counts == "":
		return false
	case under == "":
		return true
	case r.Counts == CountedAuthorizations:
		return named == under
	default:
		return named == under || strings.HasSuffix(named, "."+under)
	}
}

func (r RateLimit) Spent(now time.Time) bool {
	switch {
	case !r.ResetAt.IsZero():
		return now.After(r.ResetAt)
	case r.Observed.IsZero():
		return true
	case r.RetryAfter > 0:
		return now.After(r.Observed.Add(r.RetryAfter))
	default:
		return now.After(r.Observed.Add(r.Window))
	}
}

func (r RateLimit) Refusal(hostname string) error {
	return providerkit.Refuse(providerkit.CodeBusy,
		"the certificate authority will not issue for %s yet: %s and %s. %s",
		hostname, r.counted(), r.resets(), r.advice())
}

func (r RateLimit) counted() string {
	switch r.Counts {
	case CountedAuthorizations:
		return fmt.Sprintf("%s has failed its %d validation attempts for the last %s",
			r.named(), r.Ceiling, spelledWindow(r.Window))
	case CountedOrders:
		return fmt.Sprintf("this box's acme account has placed its %d new orders for the last %s",
			r.Ceiling, spelledWindow(r.Window))
	default:
		return fmt.Sprintf("%s has had its %d certificates for the last %s",
			r.named(), r.Ceiling, spelledWindow(r.Window))
	}
}

func (r RateLimit) named() string {
	if r.Domain == "" {
		return "this hostname"
	}
	return r.Domain
}

func (r RateLimit) advice() string {
	switch r.Counts {
	case CountedAuthorizations:
		return "A failed validation is the challenge itself failing rather than a ceiling on how many names this box may hold: the hostname has to resolve to this box and reach it on port 80 before any attempt can succeed. Check the record you pointed at this box, and that nothing in front of it answers /.well-known/acme-challenge/ itself."
	case CountedOrders:
		return "Every hostname this box certifies is ordered from the one account, preview and production alike, so this ceiling is box-wide rather than per project and an order counts against it whether it succeeds or fails. Take previews you are done with down with `ocel preview rm`, and order nothing new while the window refills."
	default:
		return fmt.Sprintf("Every preview hostname on this box counts against %s, and so does every production one, so the ceiling is new branches times apps per project per week rather than deploys. Take previews you are done with down with `ocel preview rm` — that frees the names but not the count, which refills on its own.",
			r.named())
	}
}

func (r RateLimit) resets() string {
	switch {
	case !r.ResetAt.IsZero():
		return "the next one can be ordered after " + r.ResetAt.UTC().Format(time.RFC3339)
	case r.RetryAfter > 0:
		return "the next one can be ordered in " + r.RetryAfter.String()
	default:
		return "it named no time it resets, so nothing here can be ordered until " + spelledWindow(r.Window) + " after the attempt that was refused"
	}
}

func spelledWindow(window time.Duration) string {
	if window >= 24*time.Hour {
		return strconv.Itoa(int(window/(24*time.Hour))) + " days"
	}
	return window.String()
}
