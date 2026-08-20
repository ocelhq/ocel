package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var errDescribeRefused = errors.New("AccessDenied: not authorized to perform acm:DescribeCertificate")

const (
	domainCertARN   = "arn:aws:acm:us-east-1:111122223333:certificate/live"
	domainAPIRegion = "eu-west-2"
	domainFront     = "d111111abcdef8.cloudfront.net"
)

func fakeIssuer(kind edge.Kind, api certs.ACMAPI) certs.Issuer {
	issuer := certs.IssuerFor(registeredEdge(kind), certs.Deps{AWS: aws.Config{Region: domainAPIRegion}})
	if issuer.API == nil {
		return certs.Issuer{}
	}
	issuer.API = api
	issuer.Wait = func(context.Context, time.Duration) error { return nil }
	issuer.Attempts, issuer.Every = 2, time.Millisecond
	return issuer
}

func certARNFor(kind edge.Kind) string {
	region := certs.RegionFor(registeredEdge(kind), domainAPIRegion)
	if region == "" {
		return ""
	}
	return "arn:aws:acm:" + region + ":111122223333:certificate/live"
}

func issuedProduction(arn string, hosts ...string) domains.Settlement {
	recorded := domains.Settlement{Certificate: certs.Certificate{
		ARN:     arn,
		Region:  certs.RegionOfARN(arn),
		Status:  certs.StatusIssued,
		Domains: hosts,
	}}
	for _, host := range hosts {
		recorded = recorded.WithHost(domains.Host{
			Hostname:    host,
			Certificate: arn,
			Records:     domains.Records{Written: []edge.Record{{Name: host, Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}}},
			Probe:       certs.Probe{At: time.Unix(1755500000, 0).UTC(), Edge: cloudflare.Kind, OK: true},
		})
	}
	return recorded
}

func servingStack(kind edge.Kind, hosts ...string) *boundStack {
	state := edge.StackState{Slug: domainSlug}
	if kind != cloudflare.Kind {
		state.Front = domainFront
	}
	for _, host := range hosts {
		state.Bind(host)
	}
	return &boundStack{name: string(kind), state: state}
}

func certifiedKinds() []edge.Kind {
	return []edge.Kind{cloudfront.Kind, apigateway.Kind}
}

func TestGetHostnameStatus(t *testing.T) {
	t.Parallel()

	for _, kind := range certifiedKinds() {
		t.Run("a settled host on the "+string(kind)+" edge reports its certificate, records, probe and what serves it", func(t *testing.T) {
			t.Parallel()

			arn := certARNFor(kind)
			api := newDomainACM()
			api.issue(arn, "shop.app.com")
			api.details[arn].NotAfter = aws.Time(time.Unix(1755500000, 0).Add(365 * 24 * time.Hour))
			api.details[arn].RenewalSummary = &acmtypes.RenewalSummary{RenewalStatus: acmtypes.RenewalStatusSuccess}

			f := newDomainFixture(domainFixtureOptions{
				kind:       kind,
				configured: []string{"shop.app.com"},
				stack:      servingStack(kind, "shop.app.com"),
				acm:        api,
				prior:      issuedProduction(arn, "shop.app.com"),
			})

			resp, err := f.session.status(t.Context())
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if !resp.GetReady() || len(resp.GetHostnames()) != 1 {
				t.Fatalf("status = %+v, want one ready host", resp)
			}
			host := resp.GetHostnames()[0]
			if host.GetHostname() != "shop.app.com" || !host.GetDeclared() || !host.GetReady() || host.GetPending() != "" {
				t.Errorf("host = %+v, want the declared host ready with nothing outstanding", host)
			}
			if host.GetCertificate().GetCertificateId() != arn || host.GetCertificate().GetCertificateStatus() != certs.StatusIssued {
				t.Errorf("host certificate = %q %q, want the issued one in %s", host.GetCertificate().GetCertificateId(), host.GetCertificate().GetCertificateStatus(), certs.RegionFor(registeredEdge(kind), domainAPIRegion))
			}
			if host.GetRenewalStatus() != string(acmtypes.RenewalStatusSuccess) || host.GetExpiresAt() == 0 || host.GetExpiringSoon() {
				t.Errorf("host renewal = %q, expires %d, soon %t; want a renewed certificate with a reported expiry", host.GetRenewalStatus(), host.GetExpiresAt(), host.GetExpiringSoon())
			}
			if !host.GetCertificate().GetLastProbeOk() || host.GetCertificate().GetLastProbeEdge() != string(kind) {
				t.Errorf("probe = %+v, want a fresh live probe against the edge that serves it", host)
			}
			if host.GetServingPointer() != string(kind) {
				t.Errorf("servingPointer = %q, want the edge role %q and never a vendor hostname", host.GetServingPointer(), kind)
			}
			if strings.Contains(host.GetServingPointer(), domainFront) {
				t.Errorf("servingPointer = %q, want no default vendor hostname printed", host.GetServingPointer())
			}
		})

		t.Run("a certificate near expiry on the "+string(kind)+" edge that ACM has not renewed is flagged", func(t *testing.T) {
			t.Parallel()

			arn := certARNFor(kind)
			api := newDomainACM()
			api.issue(arn, "shop.app.com")
			api.details[arn].NotAfter = aws.Time(time.Unix(1755500000, 0).Add(5 * 24 * time.Hour))

			f := newDomainFixture(domainFixtureOptions{
				kind:       kind,
				configured: []string{"shop.app.com"},
				stack:      servingStack(kind, "shop.app.com"),
				acm:        api,
				prior:      issuedProduction(arn, "shop.app.com"),
			})

			resp, err := f.session.status(t.Context())
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if !resp.GetHostnames()[0].GetExpiringSoon() {
				t.Errorf("host = %+v, want a certificate inside the renewal window flagged", resp.GetHostnames()[0])
			}
		})
	}

	t.Run("the cloudflare edge reports no certificate of its own", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			kind:       cloudflare.Kind,
			configured: []string{"shop.app.com"},
			stack:      servingStack(cloudflare.Kind, "shop.app.com"),
			prior:      issuedProduction(domainCertARN, "shop.app.com"),
		})

		resp, err := f.session.status(t.Context())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		host := resp.GetHostnames()[0]
		if host.GetCertificate().GetCertificateId() != "" || host.GetCertificate().GetCertificateStatus() != "" || host.GetExpiresAt() != 0 {
			t.Errorf("host = %+v, want no certificate reported for an edge that terminates TLS with its own", host)
		}
		if !host.GetReady() || host.GetServingPointer() != string(cloudflare.Kind) {
			t.Errorf("host = %+v, want the bound, answering host ready and served by the cloudflare role", host)
		}
	})

	t.Run("the fresh probe is reported and nothing is written back", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			kind:       cloudflare.Kind,
			configured: []string{"shop.app.com"},
			stack:      servingStack(cloudflare.Kind, "shop.app.com"),
			prior:      issuedProduction(domainCertARN, "shop.app.com"),
		})

		if _, err := f.session.status(t.Context()); err != nil {
			t.Fatalf("status: %v", err)
		}
		if len(f.ssm.params) != 0 {
			t.Errorf("ssm params = %v, want status to persist nothing it observed", f.ssm.params)
		}
	})

	t.Run("a probe that fails leaves the recorded state alone and says so", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			kind:       cloudflare.Kind,
			configured: []string{"shop.app.com"},
			stack:      servingStack(cloudflare.Kind, "shop.app.com"),
			prior:      issuedProduction(domainCertARN, "shop.app.com"),
			probe:      "someone-else",
		})

		resp, err := f.session.status(t.Context())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		host := resp.GetHostnames()[0]
		if resp.GetReady() || host.GetReady() || host.GetCertificate().GetLastProbeOk() {
			t.Errorf("status = %+v, want the host pending while nothing answers as this edge", resp)
		}
		if !strings.Contains(host.GetPending(), "does not answer") {
			t.Errorf("pending = %q, want it to name the probe", host.GetPending())
		}
		if len(f.ssm.params) != 0 {
			t.Errorf("ssm params = %v, want a failed probe never written back over what add recorded", f.ssm.params)
		}
	})

	t.Run("an unreadable certificate is surfaced, not reported as live", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.describeErr = errDescribeRefused
		f := newDomainFixture(domainFixtureOptions{
			kind:       cloudfront.Kind,
			configured: []string{"shop.app.com"},
			stack:      servingStack(cloudfront.Kind, "shop.app.com"),
			acm:        api,
			prior:      issuedProduction(certARNFor(cloudfront.Kind), "shop.app.com"),
		})

		if _, err := f.session.status(t.Context()); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
			t.Fatalf("status err = %v, want the ACM failure surfaced", err)
		}
	})

	t.Run("records ocel wrote and records the user owes are reported apart", func(t *testing.T) {
		t.Parallel()

		prior := issuedProduction(domainCertARN, "shop.app.com", "www.app.com")
		prior.Validation = domains.Records{
			Written: []edge.Record{{Name: "_acme.app.com", Type: edge.RecordTypeCNAME, Value: "validate.acm-validations.aws"}},
			Owed:    []edge.Record{{Name: "_acme2.app.com", Type: edge.RecordTypeCNAME, Value: "validate2.acm-validations.aws"}},
		}

		f := newDomainFixture(domainFixtureOptions{
			kind:       cloudflare.Kind,
			configured: []string{"shop.app.com", "www.app.com"},
			stack:      servingStack(cloudflare.Kind, "shop.app.com", "www.app.com"),
			prior:      prior,
		})

		resp, err := f.session.status(t.Context())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if !slices.Equal(resp.GetRecordsWritten(), []string{"_acme.app.com CNAME validate.acm-validations.aws"}) {
			t.Errorf("project recordsWritten = %v, want the validation record ocel wrote attributed once", resp.GetRecordsWritten())
		}
		if !slices.Equal(resp.GetRecordsOwed(), []string{"_acme2.app.com CNAME validate2.acm-validations.aws"}) {
			t.Errorf("project recordsOwed = %v, want the outstanding validation record attributed once", resp.GetRecordsOwed())
		}
		for _, host := range resp.GetHostnames() {
			if !slices.Equal(host.GetCertificate().GetRecordsWritten(), []string{host.GetHostname() + " AAAA 100::"}) {
				t.Errorf("%s recordsWritten = %v, want only its own record", host.GetHostname(), host.GetCertificate().GetRecordsWritten())
			}
			if len(host.GetCertificate().GetRecordsOwed()) != 0 {
				t.Errorf("%s recordsOwed = %v, want no project-scope record repeated per host", host.GetHostname(), host.GetCertificate().GetRecordsOwed())
			}
		}
	})

	t.Run("a host that is not bound is pending, with no probe of its own reported", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			kind:       cloudflare.Kind,
			configured: []string{"shop.app.com"},
			prior:      issuedProduction(domainCertARN, "shop.app.com"),
		})

		resp, err := f.session.status(t.Context())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if resp.GetReady() {
			t.Error("status ready = true, want the project pending while a declared host is unbound")
		}
		host := resp.GetHostnames()[0]
		if !strings.Contains(host.GetPending(), "not bound") {
			t.Errorf("pending = %q, want it to name the missing surface", host.GetPending())
		}
		if host.GetCertificate().GetLastProbeAt() != 0 || host.GetCertificate().GetLastProbeOk() {
			t.Errorf("host = %+v, want no probe reported when this run took none", host)
		}
	})

	t.Run("a host the config dropped is listed and points at rm", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{kind: cloudflare.Kind, prior: issuedProduction(domainCertARN, "old.app.com")})

		resp, err := f.session.status(t.Context())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if len(resp.GetHostnames()) != 1 || resp.GetHostnames()[0].GetDeclared() {
			t.Fatalf("hosts = %+v, want the undeclared host still listed", resp.GetHostnames())
		}
		if !strings.Contains(resp.GetHostnames()[0].GetPending(), "ocel domain rm") {
			t.Errorf("pending = %q, want it to point at rm", resp.GetHostnames()[0].GetPending())
		}
	})

	t.Run("an edge that published no front for a host it bound is surfaced", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.issue(certARNFor(apigateway.Kind), "shop.app.com")
		f := newDomainFixture(domainFixtureOptions{
			kind:       apigateway.Kind,
			configured: []string{"shop.app.com"},
			stack:      &boundStack{name: string(apigateway.Kind), state: boundTo(edge.StackState{Slug: domainSlug}, "shop.app.com")},
			acm:        api,
			prior:      issuedProduction(certARNFor(apigateway.Kind), "shop.app.com"),
		})

		if _, err := f.session.status(t.Context()); err == nil || !strings.Contains(err.Error(), "published no hostname") {
			t.Fatalf("status err = %v, want the missing front surfaced rather than an empty record list", err)
		}
	})

	t.Run("a declared host the deploy left pending is listed, not refused", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.issue(certARNFor(apigateway.Kind), "shop.app.com")
		f := newDomainFixture(domainFixtureOptions{
			kind:       apigateway.Kind,
			configured: []string{"shop.app.com"},
			stack:      &boundStack{name: string(apigateway.Kind), state: edge.StackState{Slug: domainSlug}},
			acm:        api,
			prior:      issuedProduction(certARNFor(apigateway.Kind), "shop.app.com"),
		})

		resp, err := f.session.status(t.Context())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if resp.GetReady() || len(resp.GetHostnames()) != 1 {
			t.Fatalf("status = %+v, want the declared host listed and the project pending", resp)
		}
		host := resp.GetHostnames()[0]
		if host.GetHostname() != "shop.app.com" || !host.GetDeclared() || host.GetReady() {
			t.Errorf("host = %+v, want the declared host reported pending", host)
		}
		if !strings.Contains(host.GetPending(), "not bound") || !strings.Contains(host.GetPending(), "ocel domain add") {
			t.Errorf("pending = %q, want it to name the command that binds the host", host.GetPending())
		}
		if len(host.GetCertificate().GetRecordsOwed()) != 0 {
			t.Errorf("recordsOwed = %v, want no record owed for a host nothing serves yet", host.GetCertificate().GetRecordsOwed())
		}
	})

	t.Run("a host the cloudflare edge has not bound still owes its proxied record", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			kind:       cloudflare.Kind,
			configured: []string{"shop.app.com"},
			stack:      &boundStack{name: string(cloudflare.Kind), state: edge.StackState{Slug: domainSlug}},
			prior:      domains.Settlement{},
		})

		resp, err := f.session.status(t.Context())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		host := resp.GetHostnames()[0]
		if !slices.Equal(host.GetCertificate().GetRecordsOwed(), []string{"shop.app.com AAAA 100::"}) {
			t.Errorf("recordsOwed = %v, want the proxied record that binds the host on cloudflare", host.GetCertificate().GetRecordsOwed())
		}
	})
}

func TestAddHostnameAcrossEdgeModes(t *testing.T) {
	t.Parallel()

	prior := issuedProduction(domainCertARN, "shop.app.com")
	prior = prior.WithHost(domains.Host{
		Hostname:    "shop.app.com",
		Certificate: domainCertARN,
		Probe:       certs.Probe{At: time.Unix(1, 0), Edge: cloudfront.Kind, OK: true},
	})
	prior.Certificate = certs.Certificate{}

	native := &boundStack{name: string(cloudfront.Kind), state: boundTo(edge.StackState{}, "shop.app.com")}
	writer := &domainWriter{}
	f := newDomainFixture(domainFixtureOptions{
		configured: []string{"shop.app.com"},
		writer:     writer,
		certified:  true,
		prior:      prior,
		priorEdge:  native,
	})

	if err := f.session.add(t.Context(), f.say); err != nil {
		t.Fatalf("add: %v", err)
	}

	wanted := []string{
		"request certificate for shop.app.com",
		"bind shop.app.com on cloudflare",
		"write shop.app.com AAAA 100::",
		"probe https://shop.app.com/",
		"unbind shop.app.com from cloudfront",
	}
	at := -1
	for _, want := range wanted {
		next := slices.IndexFunc(f.log.calls, func(call string) bool { return strings.Contains(call, want) })
		if next < 0 {
			t.Fatalf("calls = %v, want %q among them", f.log.calls, want)
		}
		if next <= at {
			t.Errorf("calls = %v, want %q after everything before it", f.log.calls, want)
		}
		at = next
	}
	if !slices.Equal(native.unbound, []string{"shop.app.com"}) {
		t.Errorf("unbound from the old edge = %v, want the moved hostname", native.unbound)
	}
	if !f.spoke("answers on both edges") || !f.spoke(edge.WriteTTL(writer).String()) {
		t.Errorf("said = %v, want the downtime printed as the TTL the dns writer serves the record with", f.said)
	}
}

func liveGate(t *testing.T, kind edge.Kind, api *domainACM, answering bool, prior domains.Settlement, bound ...string) domainGate {
	t.Helper()
	state := edge.StackState{Slug: domainSlug}
	for _, host := range bound {
		state.Bind(host)
	}
	seen := string(kind)
	if !answering {
		seen = "someone-else"
	}
	return domainGate{
		kind:          kind,
		servesUnbound: registeredEdge(kind).Facts().ServesUnbound,

		state:    state,
		recorded: prior,
		issuer:   fakeIssuer(kind, api),
		prober: certs.Prober{
			Get: func(context.Context, string) (http.Header, error) {
				header := http.Header{}
				header.Set(edge.HeaderEdge, seen)
				return header, nil
			},
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 1,
			Every:    time.Millisecond,
			Now:      func() time.Time { return time.Unix(1755500000, 0).UTC() },
			Jitter:   func() float64 { return 0.5 },
		},
		now: func() time.Time { return time.Unix(1755500000, 0).UTC() },
	}
}

func productionManifest(hosts ...string) *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{Domains: map[string]*deploymentsv1.DomainList{
		"production": {Hostnames: hosts},
	}}
}

func TestAdmitDomains(t *testing.T) {
	t.Parallel()

	t.Run("a production deploy without a declared hostname is refused", func(t *testing.T) {
		t.Parallel()

		_, err := admitDomains(t.Context(), domainGate{kind: cloudflare.Kind, servesUnbound: true}, deploymentsv1.Environment_CLASS_PRODUCTION, &deploymentsv1.Manifest{}, func(string) {})
		if err == nil {
			t.Fatal("admitDomains err = nil, want a refusal with nothing declared")
		}
		for _, want := range []string{"domains.production", "ocel domain add"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a preview deploy without a wildcard is refused", func(t *testing.T) {
		t.Parallel()

		_, err := admitDomains(t.Context(), domainGate{kind: cloudflare.Kind, servesUnbound: true}, deploymentsv1.Environment_CLASS_PREVIEW, &deploymentsv1.Manifest{}, func(string) {})
		if err == nil {
			t.Fatal("admitDomains err = nil, want a refusal with no preview wildcard")
		}
		for _, want := range []string{"domains.preview", "ocel domain use"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a preview deploy on the global wildcard is admitted", func(t *testing.T) {
		t.Parallel()

		gate := domainGate{kind: cloudflare.Kind, servesUnbound: true, previewOn: "preview.acme.com"}
		if _, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PREVIEW, &deploymentsv1.Manifest{}, func(string) {}); err != nil {
			t.Fatalf("admitDomains err = %v, want the global wildcard to be enough", err)
		}
	})

	t.Run("a preview deploy that declares its own wildcard is admitted", func(t *testing.T) {
		t.Parallel()

		manifest := &deploymentsv1.Manifest{Apps: []*deploymentsv1.ManifestApp{{
			Name:    "web",
			Domains: map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.acme.com"}}},
		}}}
		if _, err := admitDomains(t.Context(), domainGate{kind: cloudflare.Kind, servesUnbound: true}, deploymentsv1.Environment_CLASS_PREVIEW, manifest, func(string) {}); err != nil {
			t.Fatalf("admitDomains err = %v, want an app-declared wildcard to be enough", err)
		}
	})

	t.Run("the first production deploy is admitted, and withholds the vendor hostnames it would otherwise print", func(t *testing.T) {
		t.Parallel()

		gate := liveGate(t, cloudfront.Kind, newDomainACM(), false, domains.Settlement{})
		gate.state = edge.StackState{}
		admitted, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {})
		if err != nil {
			t.Fatalf("admitDomains err = %v, want the deploy that creates the stack admitted", err)
		}
		for _, want := range []string{"shop.app.com", "ocel domain add"} {
			if !strings.Contains(admitted.withheldURLs, want) {
				t.Errorf("withheldURLs = %q, want it to contain %q", admitted.withheldURLs, want)
			}
		}
	})

	t.Run("a settled production deploy prints its own addresses", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.issue(certARNFor(cloudfront.Kind), "shop.app.com")
		gate := liveGate(t, cloudfront.Kind, api, true, issuedProduction(certARNFor(cloudfront.Kind), "shop.app.com"), "shop.app.com")
		admitted, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {})
		if err != nil {
			t.Fatalf("admitDomains err = %v", err)
		}
		if admitted.withheldURLs != "" {
			t.Errorf("withheldURLs = %q, want nothing withheld once a hostname of the project's own serves it", admitted.withheldURLs)
		}
	})

	for _, kind := range certifiedKinds() {
		t.Run("a hostname with no certificate on the "+string(kind)+" edge is refused, naming the certificate", func(t *testing.T) {
			t.Parallel()

			gate := liveGate(t, kind, newDomainACM(), true, domains.Settlement{}, "shop.app.com")
			_, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {})
			if err == nil {
				t.Fatal("admitDomains err = nil, want a refusal without a certificate")
			}
			for _, want := range []string{"certificate", "ocel domain add"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to contain %q", err, want)
				}
			}
		})

		t.Run("a hostname the certificate does not cover on the "+string(kind)+" edge is refused", func(t *testing.T) {
			t.Parallel()

			arn := certARNFor(kind)
			api := newDomainACM()
			api.issue(arn, "www.app.com")
			gate := liveGate(t, kind, api, true, issuedProduction(arn, "shop.app.com"), "shop.app.com")
			_, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {})
			if err == nil {
				t.Fatal("admitDomains err = nil, want a refusal for a certificate that does not cover the host")
			}
			if !strings.Contains(err.Error(), "www.app.com") {
				t.Errorf("err = %v, want it to name what the certificate does cover", err)
			}
		})

		t.Run("a certificate on the "+string(kind)+" edge that ACM will not describe is refused", func(t *testing.T) {
			t.Parallel()

			api := newDomainACM()
			api.describeErr = errDescribeRefused
			gate := liveGate(t, kind, api, true, issuedProduction(certARNFor(kind), "shop.app.com"), "shop.app.com")
			_, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {})
			if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
				t.Fatalf("admitDomains err = %v, want the ACM failure surfaced rather than the recorded certificate trusted", err)
			}
		})

		t.Run("a certificate on the "+string(kind)+" edge in another region is refused before ACM is called", func(t *testing.T) {
			t.Parallel()

			api := newDomainACM()
			recorded := issuedProduction("arn:aws:acm:ap-south-1:111122223333:certificate/elsewhere", "shop.app.com")
			gate := liveGate(t, kind, api, true, recorded, "shop.app.com")
			_, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {})
			if err == nil {
				t.Fatal("admitDomains err = nil, want a refusal for a certificate in another region")
			}
			for _, want := range []string{"ap-south-1", certs.RegionFor(registeredEdge(kind), domainAPIRegion)} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name %q", err, want)
				}
			}
			if len(api.described) != 0 {
				t.Errorf("described = %v, want the region judged before ACM is asked", api.described)
			}
		})

		t.Run("a settled hostname on the "+string(kind)+" edge is admitted, and a certificate near expiry warns", func(t *testing.T) {
			t.Parallel()

			arn := certARNFor(kind)
			api := newDomainACM()
			api.issue(arn, "shop.app.com")
			api.details[arn].NotAfter = aws.Time(time.Unix(1755500000, 0).Add(9 * 24 * time.Hour))
			gate := liveGate(t, kind, api, true, issuedProduction(arn, "shop.app.com"), "shop.app.com")

			var warned []string
			if _, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(m string) { warned = append(warned, m) }); err != nil {
				t.Fatalf("admitDomains err = %v, want a settled hostname admitted", err)
			}
			if len(warned) != 1 || !strings.Contains(warned[0], "expires") {
				t.Fatalf("warnings = %v, want one warning about the coming expiry", warned)
			}
		})

		t.Run("a certificate on the "+string(kind)+" edge that ACM has renewed does not warn", func(t *testing.T) {
			t.Parallel()

			arn := certARNFor(kind)
			api := newDomainACM()
			api.issue(arn, "shop.app.com")
			api.details[arn].NotAfter = aws.Time(time.Unix(1755500000, 0).Add(9 * 24 * time.Hour))
			api.details[arn].RenewalSummary = &acmtypes.RenewalSummary{RenewalStatus: acmtypes.RenewalStatusSuccess}
			gate := liveGate(t, kind, api, true, issuedProduction(arn, "shop.app.com"), "shop.app.com")

			var warned []string
			if _, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(m string) { warned = append(warned, m) }); err != nil {
				t.Fatalf("admitDomains err = %v", err)
			}
			if len(warned) != 0 {
				t.Errorf("warnings = %v, want none for a certificate ACM has renewed", warned)
			}
		})
	}

	t.Run("hosts sharing one certificate are described once", func(t *testing.T) {
		t.Parallel()

		arn := certARNFor(cloudfront.Kind)
		api := newDomainACM()
		api.issue(arn, "shop.app.com", "www.app.com")
		gate := liveGate(t, cloudfront.Kind, api, true, issuedProduction(arn, "shop.app.com", "www.app.com"), "shop.app.com", "www.app.com")

		if _, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com", "www.app.com"), func(string) {}); err != nil {
			t.Fatalf("admitDomains err = %v", err)
		}
		if len(api.described) != 1 {
			t.Errorf("described = %v, want one describe for the certificate both hosts share", api.described)
		}
	})

	t.Run("the cloudflare edge asks ACM for nothing, and still demands the surface and a live probe", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		gate := liveGate(t, cloudflare.Kind, api, true, domains.Settlement{}, "shop.app.com")
		if _, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {}); err != nil {
			t.Fatalf("admitDomains err = %v, want a bound, answering host admitted without a certificate of ocel's own", err)
		}
		if len(api.described) != 0 {
			t.Errorf("described = %v, want no ACM call for an edge that terminates TLS with its own certificate", api.described)
		}

		unbound := liveGate(t, cloudflare.Kind, api, true, domains.Settlement{})
		if _, err := admitDomains(t.Context(), unbound, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {}); err == nil {
			t.Error("admitDomains err = nil, want the surface still demanded on cloudflare")
		}

		silent := liveGate(t, cloudflare.Kind, api, false, domains.Settlement{}, "shop.app.com")
		if _, err := admitDomains(t.Context(), silent, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {}); err == nil {
			t.Error("admitDomains err = nil, want a fresh probe still demanded on cloudflare")
		}

		pinned := liveGate(t, cloudflare.Kind, api, true, domains.Settlement{}, "shop.app.com")
		pinned.pins = map[string]string{"shop.app.com": domainCertARN}
		var warned []string
		if _, err := admitDomains(t.Context(), pinned, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(m string) { warned = append(warned, m) }); err != nil {
			t.Fatalf("admitDomains err = %v", err)
		}
		if len(warned) != 1 || !strings.Contains(warned[0], "ignored") {
			t.Errorf("warnings = %v, want the ignored certificate pin said out loud", warned)
		}
	})

	t.Run("a hostname with no edge surface is refused, naming the surface", func(t *testing.T) {
		t.Parallel()

		arn := certARNFor(cloudfront.Kind)
		api := newDomainACM()
		api.issue(arn, "shop.app.com")
		gate := liveGate(t, cloudfront.Kind, api, true, issuedProduction(arn, "shop.app.com"))
		_, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {})
		if err == nil {
			t.Fatal("admitDomains err = nil, want a refusal without an edge surface")
		}
		for _, want := range []string{"not bound to the cloudfront edge", "ocel domain add"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a hostname nothing answers for is refused, naming the record", func(t *testing.T) {
		t.Parallel()

		arn := certARNFor(cloudfront.Kind)
		api := newDomainACM()
		api.issue(arn, "shop.app.com")
		gate := liveGate(t, cloudfront.Kind, api, false, issuedProduction(arn, "shop.app.com"), "shop.app.com")
		_, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com"), func(string) {})
		if err == nil {
			t.Fatal("admitDomains err = nil, want a refusal while nothing answers")
		}
		for _, want := range []string{"DNS record", "ocel domain status --wait"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("every declared hostname is probed, and the first that fails names itself", func(t *testing.T) {
		t.Parallel()

		arn := certARNFor(cloudfront.Kind)
		api := newDomainACM()
		api.issue(arn, "shop.app.com", "www.app.com")
		gate := liveGate(t, cloudfront.Kind, api, false, issuedProduction(arn, "shop.app.com", "www.app.com"), "shop.app.com", "www.app.com")

		var probed sync.Map
		gate.prober.Get = func(_ context.Context, target string) (http.Header, error) {
			probed.Store(target, true)
			header := http.Header{}
			header.Set(edge.HeaderEdge, "someone-else")
			return header, nil
		}
		_, err := admitDomains(t.Context(), gate, deploymentsv1.Environment_CLASS_PRODUCTION, productionManifest("shop.app.com", "www.app.com"), func(string) {})
		if err == nil || !strings.Contains(err.Error(), "shop.app.com") {
			t.Fatalf("admitDomains err = %v, want the first declared host named", err)
		}
		for _, host := range []string{"https://shop.app.com/", "https://www.app.com/"} {
			if _, ok := probed.Load(host); !ok {
				t.Errorf("probed nothing for %s, want every declared host probed", host)
			}
		}
	})
}
