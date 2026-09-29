package domain

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

func quickDomainWait(t *testing.T) {
	t.Helper()
	orig := domainWait
	t.Cleanup(func() { domainWait = orig })
	domainWait = domainWaitSchedule{initialInterval: time.Millisecond, maxInterval: 2 * time.Millisecond, deadline: 10 * time.Second}
}

var errUnreachable = errors.New("the probe got no answer from shop.app.com")

func issuedCertificate(project clitest.FakeProject) {
	requireValidation(project, "shop.app.com")
	project.Provider.ReportCertificate(provider.CertificateHealth{
		Terminates: true,
		Issued:     true,
		Covers:     true,
		Status:     "ISSUED",
		Domains:    []string{"shop.app.com"},
		ExpiresAt:  1757000000,
	})
}

func TestDomainStatusAsJSONIsOneDocumentPerHostname(t *testing.T) {
	project := deployedProject(t, productionConfig("zone", "shop.app.com"))
	issuedCertificate(project)
	attach(t, project)
	useJSONOutput(t)
	invocation := newTestInvocation()

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := runDomainStatus(context.Background(), invocation, project.Root, domainOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	report := clitest.DecodeJSON(t, stdout.String())
	if report["ready"] != true {
		t.Errorf("json = %v, want it to report the project ready", report)
	}
	hosts, ok := report["hosts"].([]any)
	if !ok || len(hosts) != 1 {
		t.Fatalf("json hosts = %v, want one host", report["hosts"])
	}
	host, _ := hosts[0].(map[string]any)
	for field, want := range map[string]any{
		"hostname":          "shop.app.com",
		"declared":          true,
		"ready":             true,
		"certificate":       "issued-for-shop.app.com",
		"certificateStatus": "ISSUED",
		"expiresAt":         "2025-09-04T15:33:20Z",
		"lastProbeOk":       true,
		"servingPointer":    "relay",
	} {
		if host[field] != want {
			t.Errorf("json host %s = %v, want %v", field, host[field], want)
		}
	}
	if probed, _ := host["lastProbeAt"].(string); probed == "" {
		t.Errorf("json lastProbeAt = %v, want when the probe ran", host["lastProbeAt"])
	}
	written, _ := host["recordsWritten"].([]any)
	for _, want := range []string{"_ocel.shop.app.com CNAME _target.validations.fake.invalid", "shop.app.com CNAME test-app.relay.fake.invalid"} {
		if !slices.Contains(written, any(want)) {
			t.Errorf("json recordsWritten = %v, want it to hold %q", written, want)
		}
	}
}

func TestDomainStatusAsJSONCarriesTheProjectsValidationRecords(t *testing.T) {
	var out bytes.Buffer
	if err := writeDomainStatusJSON(&out, &contractv1.GetHostnameStatusResponse{
		Ready:         true,
		ManualRecords: []string{"_ocel.shop.app.com CNAME _target.validations.fake.invalid"},
		Hostnames:     []*contractv1.ProductionHostname{{Hostname: "shop.app.com", Declared: true, Ready: true}},
	}); err != nil {
		t.Fatal(err)
	}
	report := clitest.DecodeJSON(t, out.String())
	if owned, _ := report["manualRecords"].([]any); len(owned) != 1 || owned[0] != "_ocel.shop.app.com CNAME _target.validations.fake.invalid" {
		t.Errorf("json manualRecords = %v, want the project's records the user has to write", report["manualRecords"])
	}
}

func statusReads(t *testing.T, project clitest.FakeProject) int {
	t.Helper()
	return len(clitest.RequestsTo[*contractv1.HostnameRequest](t, project.Requests, contractv1connect.ProviderServiceGetHostnameStatusProcedure))
}

func TestDomainStatusShowsEachHostnamesCertificateRecordsProbeAndWhatIsOutstanding(t *testing.T) {
	t.Run("status shows the certificate, the records, the probe and what serves each host", func(t *testing.T) {
		project := deployedProject(t, productionConfig("", "shop.app.com"))
		issuedCertificate(project)
		attach(t, project)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, project.Root, domainOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"shop.app.com  READY",
			"Certificate          ISSUED  issued-for-shop.app.com",
			"Renewal              expires 2025-09-04T15:33:20Z",
			"Records ocel wrote   none — nothing here writes DNS",
			"Records you own      shop.app.com CNAME test-app.relay.fake.invalid",
			"_ocel.shop.app.com CNAME _target.validations.fake.invalid",
			"answered",
			"Served by            relay",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
	})

	t.Run("status --wait polls until every hostname is ready", func(t *testing.T) {
		project := deployedProject(t, productionConfig("zone", "shop.app.com"))
		attach(t, project)
		project.Provider.QueueProbeFailures("shop.app.com", errUnreachable, errUnreachable)
		invocation := newTestInvocation()
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, project.Root, domainOptions{wait: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus --wait err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "shop.app.com  READY") {
			t.Errorf("stdout = %q, want --wait to keep polling until the host is ready", out)
		}
		if strings.Contains(out, "PENDING") {
			t.Errorf("stdout = %q, want only the ready status rendered", out)
		}
		if read := statusReads(t, project); read != 3 {
			t.Errorf("--wait read the status %d times, want it read until the third answered", read)
		}
	})

	t.Run("status without --wait renders what is outstanding and does not poll", func(t *testing.T) {
		project := deployedProject(t, productionConfig("zone", "shop.app.com"))
		attach(t, project)
		project.Provider.QueueProbeFailures("shop.app.com", errUnreachable)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, project.Root, domainOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"shop.app.com  PENDING", "Outstanding", "does not answer through the relay edge yet", errUnreachable.Error()} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if read := statusReads(t, project); read != 1 {
			t.Errorf("status read the status %d times, want once without --wait", read)
		}
	})

	t.Run("status says so when the project declares no production hostname", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, project.Root, domainOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "declares no domains.production") {
			t.Errorf("stdout = %q, want it to say nothing is declared", stdout.String())
		}
	})

	t.Run("status --wait rides out a provider that is briefly errUnreachable", func(t *testing.T) {
		project := deployedProject(t, productionConfig("zone", "shop.app.com"))
		attach(t, project)
		project.Provider.QueueProbeFailures("shop.app.com", errUnreachable)
		failed := errors.New("the certificate service did not answer")
		project.Provider.QueueInspectionFailures(nil, failed, failed, failed)
		invocation := newTestInvocation()
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, project.Root, domainOptions{wait: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus --wait err = %v, want a wait that outlasts a couple of failed checks; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "shop.app.com  READY") {
			t.Errorf("stdout = %q, want the wait to reach ready", stdout.String())
		}
	})

	t.Run("status --wait gives up once the provider keeps failing", func(t *testing.T) {
		project := deployedProject(t, productionConfig("zone", "shop.app.com"))
		attach(t, project)
		project.Provider.QueueProbeFailures("shop.app.com", errUnreachable)
		failed := errors.New("the certificate service did not answer")
		project.Provider.QueueInspectionFailures(nil, failed, failed, failed, failed)
		invocation := newTestInvocation()
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		err := runDomainStatus(context.Background(), invocation, project.Root, domainOptions{wait: true}, &stdout, &stderr)
		if err == nil || !strings.Contains(stderr.String(), "failed checks in a row") {
			t.Fatalf("runDomainStatus --wait err = %v; stderr=%s, want it to give up naming the repeated failures", err, stderr.String())
		}
	})

	t.Run("status --wait fails fast when the project declares no production hostname", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		invocation := newTestInvocation()
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		err := runDomainStatus(context.Background(), invocation, project.Root, domainOptions{wait: true}, &stdout, &stderr)
		if err == nil || !strings.Contains(stderr.String(), "nothing to wait for") {
			t.Fatalf("runDomainStatus --wait err = %v; stderr=%s, want it to refuse at once with nothing declared", err, stderr.String())
		}
	})
}

func TestDomainStatusNamesWhoRenewsACertificateOcelDoesNotOwn(t *testing.T) {
	t.Parallel()

	for what, host := range map[string]*contractv1.ProductionHostname{
		"one the box's proxy obtained": {
			Hostname: "shop.app.com",
			Declared: true,
			Ready:    true,
			Certificate: &contractv1.CertificateState{
				CertificateId:     "proxy:shop.app.com",
				CertificateStatus: "SERVING",
			},
			RenewalStatus: "the proxy on this box obtained it over http-01 and renews it; ocel issues and renews nothing here",
		},
		"one the operator pinned": {
			Hostname: "pr-7.preview.app.com",
			Declared: true,
			Ready:    true,
			Certificate: &contractv1.CertificateState{
				CertificateId:     "pem:/etc/ocel/preview/certs/wildcard",
				CertificateStatus: "PINNED",
			},
			RenewalStatus: "you placed it on this box and you renew it; ocel issues and renews nothing here",
			ExpiresAt:     1757000000,
			ExpiringSoon:  true,
		},
	} {
		var out bytes.Buffer
		renderDomainStatus(&out, &contractv1.GetHostnameStatusResponse{
			Hostnames: []*contractv1.ProductionHostname{host},
		}, "ocel.config.ts")
		rendered := out.String()

		if !strings.Contains(rendered, "Certificate") || !strings.Contains(rendered, host.GetCertificate().GetCertificateId()) {
			t.Errorf("status for %s prints no certificate line:\n%s", what, rendered)
		}
		if !strings.Contains(rendered, "Renewal") || !strings.Contains(rendered, host.GetRenewalStatus()) {
			t.Errorf("status for %s prints no renewal line naming who renews it, which reads as no tls rather than tls you do not manage:\n%s", what, rendered)
		}
		if strings.Contains(rendered, "ocel renews") || strings.Contains(rendered, "ocel will renew") {
			t.Errorf("status for %s claims ocel renews something:\n%s", what, rendered)
		}
	}
}

func TestDomainStatusSaysNothingAboutExpiryForACertificateSomethingElseRenews(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderDomainStatus(&out, &contractv1.GetHostnameStatusResponse{
		Hostnames: []*contractv1.ProductionHostname{{
			Hostname:      "shop.app.com",
			Declared:      true,
			Ready:         true,
			Certificate:   &contractv1.CertificateState{CertificateId: "proxy:shop.app.com", CertificateStatus: "SERVING"},
			RenewalStatus: "the proxy on this box obtained it over http-01 and renews it; ocel issues and renews nothing here",
		}},
	}, "ocel.config.ts")

	if !strings.Contains(out.String(), "no expiry reported") {
		t.Errorf("status reports an expiry for a certificate the proxy renews, and the number is decorative wherever renewal is healthy:\n%s", out.String())
	}
	if strings.Contains(out.String(), "EXPIRING SOON") {
		t.Errorf("status warns about a certificate something else renews:\n%s", out.String())
	}
}
