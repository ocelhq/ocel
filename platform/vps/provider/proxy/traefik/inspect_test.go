package traefik_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	bound   = "shop.example.com"
	preview = "pr-12-web-abcdefghijklmnopp3347l26.preview.example.com"
	probed  = "ocel-edge-probe.preview.example.com"
)

func serving() proxy.Spec {
	return proxy.Spec{Hostnames: []string{probed, preview, bound}, PreviewBase: "preview.example.com"}
}

func sum(t *testing.T, front traefik.Traefik, spec proxy.Spec) string {
	t.Helper()
	rendered, err := front.Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	summed := sha256.Sum256(rendered)
	return hex.EncodeToString(summed[:])
}

func healthy(t *testing.T) (*box, traefik.Traefik) {
	t.Helper()
	machine := coolifyBox(t)
	machine.spec = serving()
	machine.board = "network=coolify network=ocel "
	machine.routes = map[string][]string{bound: {"switchboard"}, preview: {"switchboard"}, probed: {"switchboard"}}
	machine.leaves = map[string][]byte{
		bound:   certificate(t, bound),
		preview: certificate(t, "*.preview.example.com"),
		probed:  certificate(t, "*.preview.example.com"),
	}
	front := coolifys(machine)
	front.PreviewResolver = "cloudflare"
	machine.placed = map[string]string{coolifyDynamic + "/ocel.yml": sum(t, front, serving())}
	return machine, front
}

func inspected(t *testing.T, front traefik.Traefik) map[string]provider.HostCheck {
	t.Helper()
	checks, err := front.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	bySubject := map[string]provider.HostCheck{}
	for _, check := range checks {
		if _, twice := bySubject[check.Subject]; twice {
			t.Errorf("Inspect() checks %s twice", check.Subject)
		}
		bySubject[check.Subject] = check
	}
	return bySubject
}

func TestABoxYourTraefikRoutesWholePassesEveryCheck(t *testing.T) {
	t.Parallel()

	_, front := healthy(t)
	checks := inspected(t, front)
	for _, subject := range []string{
		"ocel-switchboard on coolify",
		coolifyDynamic + "/ocel.yml",
		bound, preview, probed,
	} {
		if check, ok := checks[subject]; !ok || check.Verdict != provider.HostPass {
			t.Errorf("%s = %+v, want it checked and passed", subject, check)
		}
	}
	for subject, check := range checks {
		if check.Verdict != provider.HostPass {
			t.Errorf("%s = %v: %s, want every check passed", subject, check.Verdict, check.Finding)
		}
	}
}

func TestAnOcelYmlYourHostToolRewroteOrDeletedFailsItsCheck(t *testing.T) {
	t.Parallel()

	for what, placed := range map[string]string{"rewritten": "0123", "deleted": ""} {
		machine, front := healthy(t)
		machine.placed[coolifyDynamic+"/ocel.yml"] = placed
		check := inspected(t, front)[coolifyDynamic+"/ocel.yml"]
		if check.Verdict != provider.HostFail || check.Fix == "" {
			t.Errorf("ocel.yml %s = %+v, want it failed with a fix", what, check)
		}
	}
}

func TestASwitchboardOffYourTraefiksNetworkFailsItsCheck(t *testing.T) {
	t.Parallel()

	machine, front := healthy(t)
	machine.board = "network=ocel "
	if check := inspected(t, front)["ocel-switchboard on coolify"]; check.Verdict != provider.HostFail || !strings.Contains(check.Fix, "ocel bootstrap") {
		t.Errorf("ocel-switchboard on coolify = %+v, want it failed and sent to bootstrap", check)
	}
}

func TestAHostnameYourTraefikStopsRoutingFailsNamingTheFileThatBrokeIt(t *testing.T) {
	t.Parallel()

	machine, front := healthy(t)
	machine.routes[bound] = []string{""}
	machine.failures = map[string]string{bound: bound + " answered nothing at 127.0.0.1:443: EOF"}
	machine.beside = append(machine.beside, besideIn(t, "dokploy")...)
	machine.beside[len(machine.beside)-1].Content = []byte("http:\n  routers: [")
	check := inspected(t, front)[bound]
	if check.Verdict != provider.HostFail {
		t.Fatalf("%s = %+v, want it failed", bound, check)
	}
	for _, mention := range []string{"answered nothing", coolifyDynamic + "/middlewares.yml", "does not parse"} {
		if !strings.Contains(check.Finding+check.Fix, mention) {
			t.Errorf("%s = %+v, want it to name %s", bound, check, mention)
		}
	}
}

func TestARouterOfYoursAddedAfterTheBindThatOcelNowOutranksFailsNamingIt(t *testing.T) {
	t.Parallel()

	machine, front := healthy(t)
	machine.spec.Hostnames = append(machine.spec.Hostnames, "web.p1305.test")
	machine.routes["web.p1305.test"] = []string{"switchboard"}
	machine.leaves["web.p1305.test"] = certificate(t, "web.p1305.test")
	machine.placed[coolifyDynamic+"/ocel.yml"] = sum(t, front, machine.spec)
	checks := inspected(t, front)
	var outranked []provider.HostCheck
	for _, check := range checks {
		if check.Verdict == provider.HostFail && strings.Contains(check.Finding, "web.p1305.test") {
			outranked = append(outranked, check)
		}
	}
	if len(outranked) != 2 {
		t.Fatalf("Inspect() = %+v, want the https and the http router of the Coolify app each named as outranked", checks)
	}
	for _, check := range outranked {
		if !strings.Contains(check.Finding, "container z1jkcokc10dwzdk8i5huimhi-122346088210") || !strings.Contains(check.Fix, "ocel domain rm web.p1305.test") {
			t.Errorf("%s = %+v, want it named with where it is defined and how to hand the hostname back", check.Subject, check)
		}
	}
}

func TestAFileOfYoursOcelCannotReadForRoutersFailsNamingIt(t *testing.T) {
	t.Parallel()

	machine, front := healthy(t)
	machine.beside = append(machine.beside, switchboard.SiblingFile{Name: "broken.yml", Content: []byte("http:\n  routers:\n    web:\n      rule: Host(`a.example.com`)\n     service: web\n")})
	check, ok := inspected(t, front)["routers in "+coolifyDynamic+"/broken.yml"]
	if !ok || check.Verdict != provider.HostFail || check.Fix == "" {
		t.Errorf("Inspect() checked broken.yml as %+v, want it failed with a fix: ocel cannot see which hostnames its routers take", check)
	}
}

func TestABoundHostnameYourTraefikHoldsNoCertificateForNeedsItsDNSFixed(t *testing.T) {
	t.Parallel()

	machine, front := healthy(t)
	machine.leaves[bound] = certificate(t, "TRAEFIK DEFAULT CERT")
	check := inspected(t, front)[bound+" certificate"]
	if check.Verdict != provider.HostNeedsAction || !strings.Contains(check.Finding, "next configuration change") || !strings.Contains(check.Fix, "DNS") {
		t.Errorf("%s certificate = %+v, want it to say Traefik retries only on its next change, and that the fix is DNS", bound, check)
	}
}

func TestYourTraefikRenewsEachCertificateThroughTheResolverThatOrderedIt(t *testing.T) {
	t.Parallel()

	_, front := healthy(t)
	for hostname, resolver := range map[string]string{bound: "letsencrypt", preview: "cloudflare", probed: "cloudflare"} {
		certificate, err := front.Certificate(context.Background(), hostname)
		if err != nil {
			t.Fatalf("Certificate(%s) = %v", hostname, err)
		}
		if want := "your Traefik renews it through " + resolver + ", for as long as its acme.json holds it"; certificate.Renewal != want {
			t.Errorf("Certificate(%s).Renewal = %q, want %q", hostname, certificate.Renewal, want)
		}
		if certificate.Trouble != nil {
			t.Errorf("Certificate(%s).Trouble = %v, want none for a leaf that covers it", hostname, certificate.Trouble)
		}
	}
}

func TestACertificateYourTraefikHasNotIssuedIsTroubleOnlyDNSFixes(t *testing.T) {
	t.Parallel()

	machine, front := healthy(t)
	delete(machine.leaves, bound)
	certificate, err := front.Certificate(context.Background(), bound)
	if err != nil {
		t.Fatalf("Certificate() = %v", err)
	}
	var refused refusal.Refusal
	if !errors.As(certificate.Trouble, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(refused.Message, "no backoff") {
		t.Errorf("Certificate().Trouble = %v, want it to say Traefik orders again only on its next change, with no backoff", certificate.Trouble)
	}
}
