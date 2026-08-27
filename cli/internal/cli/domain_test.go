package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestRequirePreviewClass(t *testing.T) {
	t.Parallel()

	if err := requirePreviewClass("ocel domain use", true); err != nil {
		t.Fatalf("requirePreviewClass(preview) = %v, want nil", err)
	}

	err := requirePreviewClass("ocel domain use", false)
	if err == nil {
		t.Fatal("requirePreviewClass(no --preview) = nil, want a refusal")
	}
	for _, want := range []string{"--preview", "preview-only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

func TestGlobalPreviewBaseDomain(t *testing.T) {
	t.Parallel()

	t.Run("a leading wildcard label yields the base domain", func(t *testing.T) {
		t.Parallel()

		for arg, want := range map[string]string{
			"*.preview.acme.com": "preview.acme.com",
			" *.ACME.com ":       "acme.com",
		} {
			got, err := globalPreviewBaseDomain(arg)
			if err != nil {
				t.Fatalf("globalPreviewBaseDomain(%q) err = %v", arg, err)
			}
			if got != want {
				t.Errorf("globalPreviewBaseDomain(%q) = %q, want %q", arg, got, want)
			}
		}
	})

	t.Run("anything but a single leading wildcard label is refused", func(t *testing.T) {
		t.Parallel()

		for _, arg := range []string{"acme.com", "*acme.com", "*.*.acme.com", "preview.*.acme.com", "*"} {
			if _, err := globalPreviewBaseDomain(arg); err == nil {
				t.Errorf("globalPreviewBaseDomain(%q) = nil error, want a refusal", arg)
			}
		}
	})
}

func writeProductionConfig(t *testing.T, root string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "shop.app.com" },
  dns: { kind: "cloudflare" },
};
`)
}

func quickDomainWait(t *testing.T) {
	t.Helper()
	orig := domainWait
	t.Cleanup(func() { domainWait = orig })
	domainWait = domainWaitSchedule{initialInterval: time.Millisecond, maxInterval: 2 * time.Millisecond, deadline: 10 * time.Second}
}

func TestRunDomainStatusJSON(t *testing.T) {
	root, sockPath := clitest.SetUpDeployFixture(t)
	writeProductionConfig(t, root)
	jsonOutput(t)
	deps := newDeps()
	clitest.SetLoggedIn(&deps)
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
	t.Setenv(clitest.FakeDomainCertEnvVar, "ISSUED arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234")
	t.Setenv(clitest.FakeDomainExpiresEnvVar, "1757000000")

	var stdout, stderr bytes.Buffer
	if err := runDomainStatus(context.Background(), deps, root, domainOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	report := asJSON(t, stdout.String())
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
		"certificate":       "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234",
		"certificateStatus": "ISSUED",
		"expiresAt":         "2025-09-04T15:33:20Z",
		"lastProbeAt":       "2025-08-18T06:53:20Z",
		"lastProbeOk":       true,
		"lastProbeEdge":     "cloudflare",
		"servingPointer":    "cloudflare",
	} {
		if host[field] != want {
			t.Errorf("json host %s = %v, want %v", field, host[field], want)
		}
	}
	written, _ := host["recordsWritten"].([]any)
	if len(written) != 1 || written[0] != "shop.app.com AAAA 100::" {
		t.Errorf("json recordsWritten = %v, want the record ocel wrote", host["recordsWritten"])
	}
	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestRunDomain(t *testing.T) {
	t.Run("every subcommand refuses without --preview", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		runs := map[string]error{
			"use":     runDomainUse(context.Background(), deps, root, "*.preview.acme.com", domainOptions{}, &stdout, &stderr),
			"ls":      runDomainLs(context.Background(), deps, root, domainOptions{}, &stdout, &stderr),
			"release": runDomainRelease(context.Background(), deps, root, domainOptions{}, &stdout, &stderr, strings.NewReader("")),
		}
		for name, err := range runs {
			if err == nil {
				t.Errorf("ocel domain %s without --preview = nil, want a refusal", name)
				continue
			}
			if !strings.Contains(err.Error(), "preview-only") {
				t.Errorf("ocel domain %s err = %v, want it to say global domains are preview-only", name, err)
			}
		}
	})

	t.Run("use claims the wildcard's base domain", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainUse(context.Background(), deps, root, "*.preview.acme.com", domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"USE DOMAIN tier=TIER_PREVIEW base=preview.acme.com", "Previews are served on *.preview.acme.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("use without a dns prints the record to add", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainUse(context.Background(), deps, root, "*.preview.acme.com", domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"dns=", "add a proxied (orange cloud) DNS record at *.preview.acme.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "Writing") {
			t.Errorf("stdout = %q, want no record written without a dns", out)
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("use with cloudflareDns writes the record", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  dns: { kind: "cloudflare" },
};
`)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainUse(context.Background(), deps, root, "*.preview.acme.com", domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"dns=cloudflare", "Writing *.preview.acme.com AAAA 100::"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("use refuses an argument that is not a leading wildcard", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		err := runDomainUse(context.Background(), deps, root, "preview.acme.com", domainOptions{preview: true}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runDomainUse err = nil, want a wildcard refusal")
		}
		if !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("err = %v, want it to name the wildcard requirement", err)
		}
	})

	t.Run("ls names the domain and the projects served on it", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")
		t.Setenv(clitest.FakeGlobalDomainEdgeScopeEnvVar, "cf-1")
		t.Setenv(clitest.FakeGlobalDomainProjectsEnvVar, "shop,blog")

		var stdout, stderr bytes.Buffer
		if err := runDomainLs(context.Background(), deps, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
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
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")
		t.Setenv(clitest.FakeGlobalDomainCertEnvVar, "ISSUED arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234")
		t.Setenv(clitest.FakeGlobalDomainRecordsEnvVar, "*.preview.acme.com AAAA 100::")
		t.Setenv(clitest.FakeGlobalDomainOwedEnvVar, "_ocel.preview.acme.com CNAME _target.acm-validations.aws")
		t.Setenv(clitest.FakeGlobalDomainProbeEnvVar, "1755500000 cloudflare")

		var stdout, stderr bytes.Buffer
		if err := runDomainLs(context.Background(), deps, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Certificate          ISSUED  arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234",
			"Records ocel wrote   *.preview.acme.com AAAA 100::",
			"Records you own      _ocel.preview.acme.com CNAME _target.acm-validations.aws",
			"Last probe           2025-08-18T06:53:20Z  x-ocel-edge: cloudflare",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("ls says an unprobed domain has never been probed", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")

		var stdout, stderr bytes.Buffer
		if err := runDomainLs(context.Background(), deps, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
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
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainLs(context.Background(), deps, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"No global preview domain is configured", "ocel domain use"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("release refuses while projects still hold previews on the wildcard", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")
		t.Setenv(clitest.FakeServedPreviewsEnvVar, "shop, blog")

		var stdout, stderr bytes.Buffer
		err := runDomainRelease(context.Background(), deps, root, domainOptions{preview: true, yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDomainRelease err = nil, want the release refused; stdout=%s", stdout.String())
		}
		for _, want := range []string{"shop", "blog", "ocel preview rm", "ocel destroy preview"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want the refusal to contain %q", stdout.String(), want)
			}
		}
		if strings.Contains(stdout.String(), "RELEASE DOMAIN") {
			t.Errorf("stdout = %q, want nothing released while previews are still served", stdout.String())
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("release plans, then releases with --yes once nothing is served", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")

		var stdout, stderr bytes.Buffer
		if err := runDomainRelease(context.Background(), deps, root, domainOptions{preview: true, yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDomainRelease err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"This will release *.preview.acme.com",
			"fronted by the cloudflare edge",
			"– preview entry worker *.preview.acme.com",
			"This cannot be undone.",
			"1 to delete, 1 unchanged.",
			"RELEASE DOMAIN tier=TIER_PREVIEW",
			"Released *.preview.acme.com",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "you created it yourself") {
			t.Errorf("stdout spent a row on a record nothing touches:\n%s", out)
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("add renders every step over the configured hosts", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: ["shop.app.com", "www.app.com"] },
  dns: { kind: "cloudflare" },
};
`)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainAdd(context.Background(), deps, root, "", &stdout, &stderr); err != nil {
			t.Fatalf("runDomainAdd err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"DOMAIN ADD slug=test-app hosts=shop.app.com,www.app.com dns=cloudflare edge=cloudfront",
			"Requesting a certificate for shop.app.com, www.app.com",
			"Binding shop.app.com to the cloudflare edge",
			"Writing shop.app.com AAAA 100::",
			"shop.app.com is served by the cloudflare edge",
			"Binding www.app.com to the cloudflare edge",
			"Serving shop.app.com, www.app.com",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("add with a host settles only that one", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: ["shop.app.com", "www.app.com"] },
};
`)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainAdd(context.Background(), deps, root, "www.app.com", &stdout, &stderr); err != nil {
			t.Fatalf("runDomainAdd err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"hosts=www.app.com", "add a proxied (orange cloud) DNS record at www.app.com", "Serving www.app.com"} {
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
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "shop.app.com" },
};
`)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainTimeoutEnvVar, "add a proxied (orange cloud) DNS record at shop.app.com")

		var stdout, stderr bytes.Buffer
		err := runDomainAdd(context.Background(), deps, root, "", &stdout, &stderr)
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
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "shop.app.com" },
};
`)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		err := runDomainAdd(context.Background(), deps, root, "other.app.com", &stdout, &stderr)
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
		deps := newDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		err := runDomainAdd(context.Background(), deps, root, "", &stdout, &stderr)
		if err == nil {
			t.Fatal("runDomainAdd err = nil, want a refusal with nothing declared")
		}
		if !strings.Contains(err.Error(), "domains.production") {
			t.Errorf("err = %v, want it to name what to declare", err)
		}
	})

	t.Run("rm carries the configured set so the provider knows what was dropped", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "shop.app.com" },
  dns: { kind: "cloudflare" },
};
`)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainRm(context.Background(), deps, root, "", &stdout, &stderr); err != nil {
			t.Fatalf("runDomainRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"DOMAIN RM slug=test-app host= configured=shop.app.com dns=cloudflare edge=cloudfront",
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
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainRm(context.Background(), deps, root, "old.app.com", &stdout, &stderr); err != nil {
			t.Fatalf("runDomainRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"Unbinding old.app.com from the cloudflare edge", "Removed old.app.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status shows the certificate, the records, the probe and what serves each host", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeProductionConfig(t, root)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainCertEnvVar, "ISSUED arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234")
		t.Setenv(clitest.FakeDomainExpiresEnvVar, "1757000000")
		t.Setenv(clitest.FakeGlobalDomainOwedEnvVar, "_ocel.shop.app.com CNAME _target.acm-validations.aws")

		var stdout, stderr bytes.Buffer
		if err := runDomainStatus(context.Background(), deps, root, domainOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"shop.app.com  READY",
			"Certificate          ISSUED  arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234",
			"Renewal              expires 2025-09-04T15:33:20Z",
			"Records ocel wrote   shop.app.com AAAA 100::",
			"Records you own      _ocel.shop.app.com CNAME _target.acm-validations.aws",
			"Last probe           2025-08-18T06:53:20Z  x-ocel-edge: cloudflare",
			"Served by            cloudflare",
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
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainReadyAfterEnvVar, "2")
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		if err := runDomainStatus(context.Background(), deps, root, domainOptions{wait: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus --wait err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "shop.app.com  READY") {
			t.Errorf("stdout = %q, want --wait to keep polling until the host is ready", out)
		}
		if strings.Contains(out, "PENDING") {
			t.Errorf("stdout = %q, want only the settled status rendered", out)
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status without --wait renders what is outstanding and does not poll", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeProductionConfig(t, root)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainReadyAfterEnvVar, "5")

		var stdout, stderr bytes.Buffer
		if err := runDomainStatus(context.Background(), deps, root, domainOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"shop.app.com  PENDING", "Outstanding", "does not answer as the cloudflare edge yet"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status says so when the project declares no production hostname", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainStatus(context.Background(), deps, root, domainOptions{}, &stdout, &stderr); err != nil {
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
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainReadyAfterEnvVar, "3")
		t.Setenv(clitest.FakeDomainFailUntilEnvVar, "3")
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		if err := runDomainStatus(context.Background(), deps, root, domainOptions{wait: true}, &stdout, &stderr); err != nil {
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
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainReadyAfterEnvVar, "99")
		t.Setenv(clitest.FakeDomainFailUntilEnvVar, "99")
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		err := runDomainStatus(context.Background(), deps, root, domainOptions{wait: true}, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "failed checks in a row") {
			t.Fatalf("runDomainStatus --wait err = %v, want it to give up naming the repeated failures", err)
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("status --wait fails fast when the project declares no production hostname", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		quickDomainWait(t)

		var stdout, stderr bytes.Buffer
		err := runDomainStatus(context.Background(), deps, root, domainOptions{wait: true}, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "nothing to wait for") {
			t.Fatalf("runDomainStatus --wait err = %v, want it to refuse at once with nothing declared", err)
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("release refuses non-interactively without --yes", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		err := runDomainRelease(context.Background(), deps, root, domainOptions{preview: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDomainRelease err = nil, want it to refuse without a terminal")
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("err = %v, want it to point at --yes", err)
		}
	})
}
