package vps_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func refusedBy(said string) string {
	return `{"level":"error","logger":"tls.obtain","msg":"could not get certificate from issuer",` +
		`"error":"[pr-7.preview.acme.com] Obtain: creating new order: attempt 1: ` +
		`https://acme-v02.api.letsencrypt.org/acme/new-order: HTTP 429 urn:ietf:params:acme:error:rateLimited - ` +
		`Error creating new order :: too many certificates (50) already issued for \"acme.com\" in the last 168h0m0s, ` +
		`retry after ` + said + `: see https://letsencrypt.org/docs/rate-limits/"}`
}

func boxWhoseProxyWasRefused(said string) *box {
	machine := &box{}
	machine.refuses = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker logs") && strings.Contains(command, host.ProxyContainer) {
			return session.Result{Stdout: said}, true
		}
		return session.Result{}, false
	}
	return machine
}

func certifying(machine *box) *vps.Provider {
	return vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
}

func certificateRefusal(t *testing.T, p *vps.Provider, hostname string) error {
	t.Helper()

	_, err := p.Certificate(context.Background(), providerkit.CertificateRequest{
		Kind: boxedge.Kind, Hostname: hostname, Report: edge.DiscardReporter(),
	})
	return err
}

func TestABoxWhoseCaSaidNoNamesTheCeilingRatherThanRelayingTheAcmeError(t *testing.T) {
	t.Parallel()

	reset := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	err := certificateRefusal(t, certifying(boxWhoseProxyWasRefused(refusedBy(reset))), "pr-9.preview.acme.com")
	if err == nil {
		t.Fatal("the box certified a hostname its own proxy is rate-limited out of ordering for, so the deploy finishes green and the preview never serves over https")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Fatalf("the refusal is %v, want %s: the ceiling refills on its own, so this is a wait rather than a mistake", err, providerkit.CodeBusy)
	}
	said := err.Error()
	for what, want := range map[string]string{
		"the registered domain the ceiling counts against": "acme.com",
		"the ceiling":          "50",
		"the time it resets":   reset,
		"what frees a name up": "ocel preview rm",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the refusal names no %s (%q):\n%s", what, want, said)
		}
	}
	if strings.Contains(said, "urn:ietf:params:acme:error") || strings.Contains(said, "Obtain:") {
		t.Errorf("the raw acme error is what the user sees:\n%s", said)
	}
}

func TestARateLimitOnAnotherDomainIsNotThisHostnamesProblem(t *testing.T) {
	t.Parallel()

	reset := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	p := certifying(boxWhoseProxyWasRefused(refusedBy(reset)))
	if err := certificateRefusal(t, p, "shop.example.com"); err != nil {
		t.Errorf("certifying a hostname under a registered domain the ceiling says nothing about = %v: a refusal here sends the user to delete previews that were never the problem", err)
	}
}

func TestACeilingTheCaSaysHasResetIsNotRefusedAgain(t *testing.T) {
	t.Parallel()

	passed := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	p := certifying(boxWhoseProxyWasRefused(refusedBy(passed)))
	if err := certificateRefusal(t, p, "pr-9.preview.acme.com"); err != nil {
		t.Errorf("certifying after the reset the CA named = %v: the proxy's log keeps what it said for as long as the container stands, so a refusal that never expires locks the box out of its own previews", err)
	}
}

func TestAProxyWithNothingToSayCertifiesAsItAlwaysDid(t *testing.T) {
	t.Parallel()

	machine := boxWhoseProxyWasRefused(`{"level":"info","msg":"certificate obtained successfully"}`)
	cert, err := certifying(machine).Certificate(context.Background(), providerkit.CertificateRequest{
		Kind: boxedge.Kind, Hostname: "pr-9.preview.acme.com", Report: edge.DiscardReporter(),
	})
	if err != nil {
		t.Fatalf("Certificate() = %v", err)
	}
	if !cert.Held() {
		t.Errorf("Certificate() = %+v, want the handle the proxy obtains and renews under", cert)
	}
}

func TestABoxWhoseEngineCannotBeReachedIsNotReadAsABoxWithNothingToSay(t *testing.T) {
	t.Parallel()

	machine := &box{}
	machine.refuses = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker version") {
			return session.Result{Code: 125, Stderr: "Cannot connect to the Docker daemon"}, true
		}
		return session.Result{}, false
	}
	spoken := &saying{Reporter: edge.DiscardReporter()}
	if _, err := certifying(machine).Certificate(context.Background(), providerkit.CertificateRequest{
		Kind: boxedge.Kind, Hostname: "pr-9.preview.acme.com", Report: spoken,
	}); err != nil {
		t.Fatalf("Certificate() = %v: minting the handle the proxy renews under asks the box for nothing, and the ceiling read is best effort beside it", err)
	}
	if !strings.Contains(strings.Join(spoken.said, "\n"), "did not answer") {
		t.Errorf("a box whose engine answered nothing said %v, and a read that never happened is reported as a proxy with nothing to say: the two are the same silence and only one of them means the CA is not refusing", spoken.said)
	}
}

type saying struct {
	edge.Reporter
	said []string
}

func (s *saying) Say(message string) { s.said = append(s.said, message) }
