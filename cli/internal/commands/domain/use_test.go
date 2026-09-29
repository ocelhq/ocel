package domain

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestDomainUseServesEveryProjectsPreviewsOnTheWildcard(t *testing.T) {
	t.Run("use claims the wildcard's base domain", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainUse(context.Background(), invocation, root, "*.preview.acme.com", domainOptions{preview: true}, &stdout, &stderr); err != nil {
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
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainUse(context.Background(), invocation, root, "*.preview.acme.com", domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"dns=", "add a DNS record at *.preview.acme.com proxied through the edge"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "Writing") {
			t.Errorf("stdout = %q, want no record written without a dns", out)
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("use with a dns writes the record", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  dns: "zone",
};
`)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainUse(context.Background(), invocation, root, "*.preview.acme.com", domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"dns=zone", "Writing *.preview.acme.com AAAA 100::"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("use refuses an argument that is not a leading wildcard", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainUse(context.Background(), invocation, root, "preview.acme.com", domainOptions{preview: true}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runDomainUse err = nil, want a wildcard refusal")
		}
		if !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("err = %v, want it to name the wildcard requirement", err)
		}
	})
}
