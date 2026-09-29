package domain

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestDomainListWithPreviewShowsTheGlobalDomainAndTheProjectsOnIt(t *testing.T) {
	t.Run("ls names the domain and the projects served on it", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")
		t.Setenv(clitest.FakeGlobalDomainEdgeScopeEnvVar, "cf-1")
		t.Setenv(clitest.FakeGlobalDomainProjectsEnvVar, "shop,blog")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainList(context.Background(), invocation, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"*.preview.acme.com", "cf-1", "1–1", "installed", "shop", "blog"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("ls shows the certificate, the records and the last probe", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")
		t.Setenv(clitest.FakeGlobalDomainCertEnvVar, "ISSUED fake-certificate/abcd-1234")
		t.Setenv(clitest.FakeGlobalDomainRecordsEnvVar, "*.preview.acme.com AAAA 100::")
		t.Setenv(clitest.FakeGlobalDomainManualRecordsEnvVar, "_ocel.preview.acme.com CNAME _target.validations.fake.example")
		t.Setenv(clitest.FakeGlobalDomainProbeEnvVar, "1755500000")
		t.Setenv(clitest.FakeGlobalDomainRenewalEnvVar, "you placed it on this box and you renew it")
		t.Setenv(clitest.FakeGlobalDomainExpiresEnvVar, "1755500000")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainList(context.Background(), invocation, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Certificate          ISSUED  fake-certificate/abcd-1234",
			"Renewal              expires 2025-08-18T06:53:20Z, you placed it on this box and you renew it — EXPIRING SOON",
			"Records ocel wrote   *.preview.acme.com AAAA 100::",
			"Records you own      _ocel.preview.acme.com CNAME _target.validations.fake.example",
			"Last probe           2025-08-18T06:53:20Z  answered",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("ls says an unprobed domain has never been probed", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainList(context.Background(), invocation, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Last probe           never") {
			t.Errorf("stdout = %q, want it to say the domain has never been probed", out)
		}
		if strings.Contains(out, "Certificate") {
			t.Errorf("stdout = %q, want no certificate line when none is recorded", out)
		}
		for _, want := range []string{"Records ocel wrote   none", "Records you own      none outstanding"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("ls says so when no global domain is configured", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainList(context.Background(), invocation, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"No global preview domain is configured", "ocel domain use"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})
}

func TestDomainAddProvisionsOnlyTheHostnamesTheConfigDeclares(t *testing.T) {
	t.Run("add renders every step over the configured hosts", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: ["shop.app.com", "www.app.com"] },
  dns: "zone",
};
`)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainAdd(context.Background(), invocation, root, "", &stdout, &stderr); err != nil {
			t.Fatalf("runDomainAdd err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"DOMAIN ADD slug=test-app hosts=shop.app.com,www.app.com dns=zone edge=direct",
			"Requesting a certificate for shop.app.com, www.app.com",
			"Binding shop.app.com to the relay edge",
			"Writing shop.app.com AAAA 100::",
			"shop.app.com is served through the relay edge",
			"Binding www.app.com to the relay edge",
			"Serving shop.app.com, www.app.com",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("add with a host attaches only that one", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: ["shop.app.com", "www.app.com"] },
};
`)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainAdd(context.Background(), invocation, root, "www.app.com", &stdout, &stderr); err != nil {
			t.Fatalf("runDomainAdd err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"hosts=www.app.com", "add a DNS record at www.app.com proxied through the edge", "Serving www.app.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "shop.app.com") {
			t.Errorf("stdout = %q, want the host that was not named left out", out)
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("add exits non-zero when a wait times out, naming what is outstanding", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: "shop.app.com" },
};
`)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainTimeoutEnvVar, "add a DNS record at shop.app.com proxied through the edge")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainAdd(context.Background(), invocation, root, "", &stdout, &stderr)
		if err == nil {
			t.Fatalf("runDomainAdd err = nil, want the timeout surfaced; stdout=%s", stdout.String())
		}
		rendered := stdout.String() + stderr.String()
		for _, want := range []string{"gave up after", "still outstanding", "shop.app.com"} {
			if !strings.Contains(rendered, want) {
				t.Errorf("rendered output = %q, want it to contain %q", rendered, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("add refuses a host the config does not declare", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: "shop.app.com" },
};
`)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainAdd(context.Background(), invocation, root, "other.app.com", &stdout, &stderr)
		if err == nil {
			t.Fatal("runDomainAdd err = nil, want a refusal: no command edits the config")
		}
		rendered := stdout.String() + stderr.String()
		for _, want := range []string{"domains.production", "shop.app.com", "other.app.com"} {
			if !strings.Contains(rendered, want) {
				t.Errorf("rendered output = %q, want it to contain %q", rendered, want)
			}
		}
	})

	t.Run("add refuses a project that declares no production hostname", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainAdd(context.Background(), invocation, root, "", &stdout, &stderr)
		if err == nil {
			t.Fatal("runDomainAdd err = nil, want a refusal with nothing declared")
		}
		if !strings.Contains(err.Error(), "domains.production") {
			t.Errorf("err = %v, want it to name what to declare", err)
		}
	})
}

func TestDomainRemoveUnbindsWhatTheConfigNoLongerDeclares(t *testing.T) {
	t.Run("rm sends the configured set so the provider knows what was dropped", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: "shop.app.com" },
  dns: "zone",
};
`)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainRemove(context.Background(), invocation, root, "", domainOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDomainRemove err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"DOMAIN RM slug=test-app host= configured=shop.app.com dns=zone edge=direct",
			"Removed every hostname this project no longer declares",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("rm with a host unbinds it", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainRemove(context.Background(), invocation, root, "old.app.com", domainOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDomainRemove err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"Unbinding old.app.com from the relay edge", "Removed old.app.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("rm refuses without a terminal or --yes and removes nothing", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainRemove(context.Background(), invocation, root, "old.app.com", domainOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "pass --yes") {
			t.Fatalf("runDomainRemove without a terminal err = %v, want a refusal naming --yes", err)
		}
		if strings.Contains(stdout.String(), "Unbinding") {
			t.Errorf("stdout = %q, want nothing unbound without consent", stdout.String())
		}
	})

	t.Run("rm behind a declined confirmation removes nothing", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		invocation.StdinIsTerminal = func(io.Reader) bool { return true }
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainRemove(context.Background(), invocation, root, "old.app.com", domainOptions{}, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runDomainRemove err = %v; stdout=%s", err, stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Not confirmed, so this run changes nothing") {
			t.Errorf("stdout = %q, want a declined confirmation to say so", out)
		}
		if strings.Contains(out, "Unbinding") || strings.Contains(out, "Removed old.app.com") {
			t.Errorf("stdout = %q, want nothing removed behind a declined confirmation", out)
		}
	})
}

func TestDomainListListsThisProjectsOwnHostnamesWithoutPreview(t *testing.T) {
	root, sockPath := clitest.SetUpDeployFixture(t)
	writeProductionConfig(t, root)
	invocation := newTestInvocation()
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
	t.Setenv(clitest.FakeDomainCertEnvVar, "ISSUED proxy:shop.app.com")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := runDomainList(context.Background(), invocation, root, domainOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"shop.app.com", "READY"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to name %q: `ocel domain ls` lists what this project has bound", out, want)
		}
	}
	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestDomainListReadsStateWhileStatusChecksTheEdgeLive(t *testing.T) {
	root, sockPath := clitest.SetUpDeployFixture(t)
	writeProductionConfig(t, root)
	journal := filepath.Join(t.TempDir(), "hostname.journal")
	t.Setenv(clitest.FakeHostnameJournalEnvVar, journal)
	invocation := newTestInvocation()
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := runDomainList(context.Background(), invocation, root, domainOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if asked := readJournal(t, journal); len(asked) != 1 || !strings.Contains(asked[0], "probe=false") {
		t.Errorf("`ocel domain ls` asked %q, want it to read state: a probe reaches every declared hostname over the network on a timeout each, so listing waits as long as the project has hostnames", asked)
	}

	stdout.Reset()
	stderr.Reset()
	if err := runDomainStatus(context.Background(), invocation, root, domainOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	asked := readJournal(t, journal)
	if len(asked) != 2 || !strings.Contains(asked[1], "probe=true") {
		t.Errorf("`ocel domain status` asked %q, want it to check the edge live: it is the acceptance test for a bind, and a hostname that stopped answering looks served in the record until something asks", asked)
	}
	clitest.WaitForNoStaleSocket(t, sockPath)
}

func readJournal(t *testing.T, path string) []string {
	t.Helper()
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the journal the fake provider wrote: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(read)), "\n")
}
