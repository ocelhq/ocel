package certs

import (
	"strings"
	"testing"
	"time"
)

const boulderSaid = `{"level":"error","ts":1788128443.02,"logger":"tls.obtain","msg":"could not get certificate from issuer",` +
	`"identifier":"shop--pr-7--web.preview.acme.com","issuer":"acme-v02.api.letsencrypt.org-directory",` +
	`"error":"[shop--pr-7--web.preview.acme.com] Obtain: [shop--pr-7--web.preview.acme.com] creating new order: ` +
	`attempt 1: https://acme-v02.api.letsencrypt.org/acme/new-order: HTTP 429 urn:ietf:params:acme:error:rateLimited - ` +
	`Error creating new order :: too many certificates (50) already issued for \"acme.com\" in the last 168h0m0s, ` +
	`retry after 2026-09-05T12:00:00Z: see https://letsencrypt.org/docs/rate-limits/ (ca=https://acme-v02.api.letsencrypt.org/directory)"}`

func TestACertificateAuthoritysRateLimitIsTranslatedRatherThanRelayed(t *testing.T) {
	t.Parallel()

	limit, held := RateLimited(boulderSaid)
	if !held {
		t.Fatal("the CA's rate-limit response was not read as one, so the box would relay a line of acme jargon and say nothing about what to do next")
	}
	said := limit.Refusal("shop--pr-7--web.preview.acme.com").Error()

	for what, want := range map[string]string{
		"the registered domain the ceiling counts against": "acme.com",
		"the ceiling itself":            "50",
		"the window it counts over":     "7 days",
		"the time the ceiling resets":   "2026-09-05T12:00:00Z",
		"the command that frees one up": "ocel preview rm",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the refusal names no %s (%q):\n%s", what, want, said)
		}
	}
	for what, raw := range map[string]string{
		"the acme error urn":       "urn:ietf:params:acme:error",
		"the CA's own url":         "acme-v02.api.letsencrypt.org",
		"the obtain call stack":    "Obtain:",
		"the http status verbatim": "HTTP 429",
	} {
		if strings.Contains(said, raw) {
			t.Errorf("the refusal carries %s verbatim, which is the failure mode this translation exists to buy out of:\n%s", what, said)
		}
	}
}

func TestTheResetIsReadOffARetryAfterWhenTheDetailCarriesNoTimestamp(t *testing.T) {
	t.Parallel()

	said := `{"msg":"could not get certificate from issuer","error":"HTTP 429 urn:ietf:params:acme:error:rateLimited - ` +
		`too many certificates already issued for \"acme.com\" (Retry-After: 3600)"}`
	limit, held := RateLimited(said)
	if !held {
		t.Fatal("a rate-limit response carrying its reset as a Retry-After was not read as one")
	}
	if limit.Domain != "acme.com" {
		t.Errorf("the registered domain reads as %q, want acme.com", limit.Domain)
	}
	if limit.Ceiling != CertificatesPerDomain {
		t.Errorf("the ceiling reads as %d, want the documented %d when the response names none", limit.Ceiling, CertificatesPerDomain)
	}
	if limit.RetryAfter != time.Hour {
		t.Errorf("the reset reads as %v, want an hour", limit.RetryAfter)
	}
	if !strings.Contains(limit.Refusal("pr-7.preview.acme.com").Error(), "1h0m0s") {
		t.Errorf("the refusal names no window to wait:\n%s", limit.Refusal("pr-7.preview.acme.com"))
	}
}

const spokenAt = "2026-08-30T12:00:00.000000000Z"

func logLine(detail string) string {
	return spokenAt + ` {"level":"error","msg":"could not get certificate from issuer",` +
		`"error":"HTTP 429 urn:ietf:params:acme:error:rateLimited - ` + detail + `"}`
}

func spoken(t *testing.T) time.Time {
	t.Helper()

	at, err := time.Parse(time.RFC3339Nano, spokenAt)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestALimitTheCaNamedNoResetTimeForIsSpentOffTheLineItWasSaidOn(t *testing.T) {
	t.Parallel()

	limit, held := RateLimited(logLine(`too many certificates already issued for \"acme.com\" (Retry-After: 3600)`))
	if !held {
		t.Fatal("a rate-limit response carrying its reset as a Retry-After was not read as one")
	}
	if !limit.ResetAt.IsZero() {
		t.Fatalf("this line names no reset timestamp and one read as %v, so the window below is not the one under test", limit.ResetAt)
	}
	at := spoken(t)
	if limit.Spent(at.Add(59 * time.Minute)) {
		t.Error("the limit reads as spent inside the hour the CA asked for, so a refusal the box owes the user is dropped")
	}
	if !limit.Spent(at.Add(61 * time.Minute)) {
		t.Error("the limit is still unspent an hour after the CA said to retry, and `docker logs --tail 200` is re-read on every Certificate() call: one stale line would refuse every certification under this registered domain — production hostnames included — until it ages out of the tail")
	}
}

func TestALimitWithNoResetAtAllIsSpentOnceItsOwnWindowHasPassed(t *testing.T) {
	t.Parallel()

	limit, held := RateLimited(logLine(`too many certificates already issued for \"acme.com\"`))
	if !held {
		t.Fatal("a rate-limit response naming neither a reset time nor a Retry-After was not read as one")
	}
	at := spoken(t)
	if limit.Spent(at.Add(CertificateWindow - time.Hour)) {
		t.Error("the limit reads as spent inside the window it counts over")
	}
	if !limit.Spent(at.Add(CertificateWindow + time.Hour)) {
		t.Error("a limit the CA named no reset for never expires, so the box refuses its own TLS for as long as the line sits in the proxy's log tail")
	}
}

func TestALineCarryingNoTimestampBoundsNothingAndIsNotRefusedOn(t *testing.T) {
	t.Parallel()

	limit, held := RateLimited(`{"error":"urn:ietf:params:acme:error:rateLimited - too many certificates already issued for \"acme.com\" (Retry-After: 3600)"}`)
	if !held {
		t.Fatal("a rate-limit response with no docker timestamp ahead of it was not read as one")
	}
	if !limit.Spent(time.Now()) {
		t.Error("a line nothing can date is treated as a live limit, and a refusal with no expiry on it locks this box out of certifying anything at all")
	}
}

func TestARetryAfterTooLargeToBeADurationDoesNotWrapIntoAWindowAlreadyPast(t *testing.T) {
	t.Parallel()

	limit, held := RateLimited(logLine(`too many certificates already issued for \"acme.com\" (Retry-After: 1234567890123456789)`))
	if !held {
		t.Fatal("a rate-limit response carrying an absurd Retry-After was not read as one")
	}
	if limit.RetryAfter <= 0 || limit.RetryAfter > retryAfterCeiling {
		t.Errorf("the reset reads as %v, want it clamped to at most %v: %d seconds is more than a duration holds, and what it wraps to is a window already elapsed",
			limit.RetryAfter, retryAfterCeiling, int64(1234567890123456789))
	}
	if limit.Spent(spoken(t).Add(time.Second)) {
		t.Error("a Retry-After too large to hold in a duration overflows into a window already past, so the refusal it should have bought never fires")
	}
}

func TestTheTwoSecondaryCeilingsAreTranslatedRatherThanRelayed(t *testing.T) {
	t.Parallel()

	for what, reading := range map[string]struct {
		detail   string
		hostname string
		wants    []string
	}{
		"the failed-authorization ceiling": {
			`too many failed authorizations (5) for \"pr-7.preview.acme.com\" in the last 1h0m0s, retry after 2026-08-30T13:00:00Z: see https://letsencrypt.org/docs/rate-limits/`,
			"pr-7.preview.acme.com",
			[]string{"5", "1h0m0s", "2026-08-30T13:00:00Z", "/.well-known/acme-challenge/"},
		},
		"the new-order ceiling": {
			`too many new orders (300) from this account in the last 3h0m0s, retry after 2026-08-30T15:00:00Z: see https://letsencrypt.org/docs/rate-limits/`,
			"pr-7.preview.acme.com",
			[]string{"300", "3h0m0s", "2026-08-30T15:00:00Z", "ocel preview rm"},
		},
	} {
		limit, held := RateLimited(logLine(reading.detail))
		if !held {
			t.Errorf("%s was not read as a rate limit at all, so the response surfaces raw, which is the failure mode this translation exists to buy out of", what)
			continue
		}
		if !limit.Covers(reading.hostname) {
			t.Errorf("%s does not cover %s, so the refusal it carries is never raised", what, reading.hostname)
			continue
		}
		refusal := limit.Refusal(reading.hostname).Error()
		for _, want := range reading.wants {
			if !strings.Contains(refusal, want) {
				t.Errorf("the refusal for %s names no %q:\n%s", what, want, refusal)
			}
		}
		for _, raw := range []string{"urn:ietf:params:acme:error", "HTTP 429", "letsencrypt.org/docs/rate-limits"} {
			if strings.Contains(refusal, raw) {
				t.Errorf("the refusal for %s carries %q verbatim:\n%s", what, raw, refusal)
			}
		}
	}
}

func TestAnAccountWideCeilingCoversEveryHostnameAndAFailedAuthorizationCoversOne(t *testing.T) {
	t.Parallel()

	orders, held := RateLimited(logLine(`too many new orders (300) from this account in the last 3h0m0s`))
	if !held {
		t.Fatal("the new-order ceiling was not read as a rate limit")
	}
	if !orders.Covers("shop.example.com") {
		t.Error("the new-order ceiling covers no hostname, and it is counted per account: every name this box orders is behind it")
	}

	failed, held := RateLimited(logLine(`too many failed authorizations (5) for \"pr-7.preview.acme.com\" in the last 1h0m0s`))
	if !held {
		t.Fatal("the failed-authorization ceiling was not read as a rate limit")
	}
	if !failed.Covers("pr-7.preview.acme.com") {
		t.Error("the failed-authorization ceiling does not cover the identifier it names")
	}
	if failed.Covers("pr-9.preview.acme.com") {
		t.Error("the failed-authorization ceiling is counted per identifier, and covering a sibling hostname refuses a certification nothing is stopping")
	}
}

func TestALogWithNoRateLimitInItIsNotReadAsOne(t *testing.T) {
	t.Parallel()

	for what, said := range map[string]string{
		"an empty log":                "",
		"a proxy serving happily":     `{"level":"info","msg":"certificate obtained successfully","identifier":"shop.example.com"}`,
		"an unreachable CA":           `{"level":"error","msg":"could not get certificate from issuer","error":"dial tcp 127.0.0.1:9: connect: connection refused"}`,
		"a failed http-01 validation": `{"level":"error","msg":"validating authorization","error":"urn:ietf:params:acme:error:unauthorized - Invalid response from http://pr-7.preview.acme.com/.well-known/acme-challenge/x"}`,
	} {
		if limit, held := RateLimited(said); held {
			t.Errorf("%s was read as a rate-limit response naming %q, and a refusal telling the user to run `ocel preview rm` over an unrelated failure sends them to delete work that was not the problem", what, limit.Domain)
		}
	}
}
