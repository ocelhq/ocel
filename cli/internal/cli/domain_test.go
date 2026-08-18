package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
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

func TestRunDomain(t *testing.T) {
	t.Run("every subcommand refuses without --preview", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)

		var stdout, stderr bytes.Buffer
		runs := map[string]error{
			"use":     runDomainUse(context.Background(), d, root, "*.preview.acme.com", domainOptions{}, &stdout, &stderr),
			"ls":      runDomainLs(context.Background(), d, root, domainOptions{}, &stdout, &stderr),
			"release": runDomainRelease(context.Background(), d, root, domainOptions{}, &stdout, &stderr, strings.NewReader("")),
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
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainUse(context.Background(), d, root, "*.preview.acme.com", domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"USE DOMAIN class=CLASS_PREVIEW base=preview.acme.com", "Previews are served on *.preview.acme.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("use without a dns prints the record to add", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainUse(context.Background(), d, root, "*.preview.acme.com", domainOptions{preview: true}, &stdout, &stderr); err != nil {
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
		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("use with cloudflareDns writes the record", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  dns: { kind: "cloudflare" },
};
`)
		d := defaultDeps()
		setLoggedIn(&d)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainUse(context.Background(), d, root, "*.preview.acme.com", domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"dns=cloudflare", "Writing *.preview.acme.com AAAA 100::"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("use refuses an argument that is not a leading wildcard", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)

		var stdout, stderr bytes.Buffer
		err := runDomainUse(context.Background(), d, root, "preview.acme.com", domainOptions{preview: true}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runDomainUse err = nil, want a wildcard refusal")
		}
		if !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("err = %v, want it to name the wildcard requirement", err)
		}
	})

	t.Run("ls names the domain and the projects served on it", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")
		t.Setenv(fakeGlobalDomainEnvVar, "preview.acme.com")
		t.Setenv(fakeGlobalDomainAccountEnvVar, "cf-1")
		t.Setenv(fakeGlobalDomainProjectsEnvVar, "shop,blog")

		var stdout, stderr bytes.Buffer
		if err := runDomainLs(context.Background(), d, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"*.preview.acme.com", "cf-1", "1–1", "installed", "shop", "blog"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("ls says so when no global domain is configured", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDomainLs(context.Background(), d, root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"No global preview domain is configured", "ocel domain use"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("release lists the projects and releases with --yes", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")
		t.Setenv(fakeGlobalDomainEnvVar, "preview.acme.com")
		t.Setenv(fakeGlobalDomainProjectsEnvVar, "shop,blog")

		var stdout, stderr bytes.Buffer
		if err := runDomainRelease(context.Background(), d, root, domainOptions{preview: true, yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDomainRelease err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"shop", "blog", "RELEASE DOMAIN class=CLASS_PREVIEW", "Released *.preview.acme.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("release refuses non-interactively without --yes", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)

		var stdout, stderr bytes.Buffer
		err := runDomainRelease(context.Background(), d, root, domainOptions{preview: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDomainRelease err = nil, want it to refuse without a terminal")
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("err = %v, want it to point at --yes", err)
		}
	})
}
