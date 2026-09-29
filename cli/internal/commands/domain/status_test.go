package domain

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func quickDomainWait(t *testing.T) {
	t.Helper()
	orig := domainWait
	t.Cleanup(func() { domainWait = orig })
	domainWait = domainWaitSchedule{initialInterval: time.Millisecond, maxInterval: 2 * time.Millisecond, deadline: 10 * time.Second}
}

func TestDomainStatusAsJSONIsOneDocumentPerHostname(t *testing.T) {
	root, sockPath := clitest.SetUpDeployFixture(t)
	writeProductionConfig(t, root)
	useJSONOutput(t)
	invocation := newTestInvocation()
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
	t.Setenv(clitest.FakeDomainCertEnvVar, "ISSUED fake-certificate/abcd-1234")
	t.Setenv(clitest.FakeDomainExpiresEnvVar, "1757000000")
	t.Setenv(clitest.FakeGlobalDomainManualRecordsEnvVar, "_ocel.shop.app.com CNAME _target.validations.fake.example")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := runDomainStatus(context.Background(), invocation, root, domainOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	report := clitest.DecodeJSON(t, stdout.String())
	if report["ready"] != true {
		t.Errorf("json = %v, want it to report the project ready", report)
	}
	if owned, _ := report["manualRecords"].([]any); len(owned) != 1 || owned[0] != "_ocel.shop.app.com CNAME _target.validations.fake.example" {
		t.Errorf("json manualRecords = %v, want the project's records the user has to write", report["manualRecords"])
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
		"certificate":       "fake-certificate/abcd-1234",
		"certificateStatus": "ISSUED",
		"expiresAt":         "2025-09-04T15:33:20Z",
		"lastProbeAt":       "2025-08-18T06:53:20Z",
		"lastProbeOk":       true,
		"servingPointer":    "relay",
	} {
		if host[field] != want {
			t.Errorf("json host %s = %v, want %v", field, host[field], want)
		}
	}
	written, _ := host["recordsWritten"].([]any)
	if len(written) != 1 || written[0] != "shop.app.com AAAA 100::" {
		t.Errorf("json recordsWritten = %v, want the record ocel wrote", host["recordsWritten"])
	}
	manual, _ := host["manualRecords"].([]any)
	if len(manual) != 1 || manual[0] != "_ocel.shop.app.com CNAME _target.validations.fake.example" {
		t.Errorf("json manualRecords = %v, want the record the user has to write", host["manualRecords"])
	}
	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestDomainStatusShowsEachHostnamesCertificateRecordsProbeAndWhatIsOutstanding(t *testing.T) {
	t.Run("status shows the certificate, the records, the probe and what serves each host", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeProductionConfig(t, root)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainCertEnvVar, "ISSUED fake-certificate/abcd-1234")
		t.Setenv(clitest.FakeDomainExpiresEnvVar, "1757000000")
		t.Setenv(clitest.FakeGlobalDomainManualRecordsEnvVar, "_ocel.shop.app.com CNAME _target.validations.fake.example")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, root, domainOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"shop.app.com  READY",
			"Certificate          ISSUED  fake-certificate/abcd-1234",
			"Renewal              expires 2025-09-04T15:33:20Z",
			"Records ocel wrote   shop.app.com AAAA 100::",
			"Records you own      _ocel.shop.app.com CNAME _target.validations.fake.example",
			"Last probe           2025-08-18T06:53:20Z  answered",
			"Served by            relay",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status --wait polls until every hostname is ready", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeProductionConfig(t, root)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainReadyAfterEnvVar, "2")
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, root, domainOptions{wait: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus --wait err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "shop.app.com  READY") {
			t.Errorf("stdout = %q, want --wait to keep polling until the host is ready", out)
		}
		if strings.Contains(out, "PENDING") {
			t.Errorf("stdout = %q, want only the ready status rendered", out)
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status without --wait renders what is outstanding and does not poll", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeProductionConfig(t, root)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainReadyAfterEnvVar, "5")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, root, domainOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"shop.app.com  PENDING", "Outstanding", "does not answer through the relay edge yet"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status says so when the project declares no production hostname", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, root, domainOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "declares no domains.production") {
			t.Errorf("stdout = %q, want it to say nothing is declared", stdout.String())
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status --wait rides out a provider that is briefly unreachable", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeProductionConfig(t, root)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainReadyAfterEnvVar, "3")
		t.Setenv(clitest.FakeDomainFailUntilEnvVar, "3")
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainStatus(context.Background(), invocation, root, domainOptions{wait: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus --wait err = %v, want a wait that outlasts a couple of failed checks; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "shop.app.com  READY") {
			t.Errorf("stdout = %q, want the wait to reach ready", stdout.String())
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status --wait gives up once the provider keeps failing", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeProductionConfig(t, root)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainReadyAfterEnvVar, "99")
		t.Setenv(clitest.FakeDomainFailUntilEnvVar, "99")
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		err := runDomainStatus(context.Background(), invocation, root, domainOptions{wait: true}, &stdout, &stderr)
		if err == nil || !strings.Contains(stderr.String(), "failed checks in a row") {
			t.Fatalf("runDomainStatus --wait err = %v; stderr=%s, want it to give up naming the repeated failures", err, stderr.String())
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status --wait fails fast when the project declares no production hostname", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		err := runDomainStatus(context.Background(), invocation, root, domainOptions{wait: true}, &stdout, &stderr)
		if err == nil || !strings.Contains(stderr.String(), "nothing to wait for") {
			t.Fatalf("runDomainStatus --wait err = %v; stderr=%s, want it to refuse at once with nothing declared", err, stderr.String())
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
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
