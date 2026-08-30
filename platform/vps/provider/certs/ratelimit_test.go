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
