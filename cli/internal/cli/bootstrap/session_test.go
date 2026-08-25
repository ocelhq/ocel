package bootstrap

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestBootstrapCarriesAutoHeal(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"an unset switch leaves the account as it is", Options{Yes: true}, "features=isr force=false"},
		{"--auto-heal turns it on", Options{Yes: true, Healing: true, AutoHeal: true}, "features=isr force=false autoHeal=true"},
		{"--auto-heal=false takes it back", Options{Yes: true, Healing: true}, "features=isr force=false autoHeal=false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, journal, d := clitest.SetUpEdgeFixture(t, "")
			t.Setenv(clitest.FakeEnabledFeaturesEnvVar, "isr")

			var stdout, stderr bytes.Buffer
			if err := Run(context.Background(), d, root, environmentv1.Tier_TIER_PRODUCTION, tt.opts, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			got := clitest.ReadJournal(t, journal)
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("provider saw %v, want %q", got, tt.want)
			}
		})
	}
}

func TestRunBootstrapStatus(t *testing.T) {
	t.Run("it reports both classes", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		d := clitest.NewSession()
		clitest.SetLoggedIn(&d)
		clitest.StubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		if err := RunStatus(context.Background(), d, root, StatusOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runBootstrapStatus err = %v; stderr=%s", err, stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"production: schema 1", "ocel-bootstrap-isr", "ocel-bootstrap-image-optimization", "preview: not bootstrapped"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("--check fails on stale content", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeBootstrapEnvVar, "stale")
		d := clitest.NewSession()
		clitest.SetLoggedIn(&d)
		clitest.StubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		err := RunStatus(context.Background(), d, root, StatusOptions{Check: true}, &stdout, &stderr)
		if err == nil {
			t.Fatalf("--check passed a bootstrap carrying stale content; stdout=%s", stdout.String())
		}
		if !strings.Contains(err.Error(), "ocel-bootstrap-isr") {
			t.Errorf("err = %v, want it to name the stale stack", err)
		}
	})

	t.Run("--check passes a bootstrap this build wrote", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		d := clitest.NewSession()
		clitest.SetLoggedIn(&d)
		clitest.StubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		if err := RunStatus(context.Background(), d, root, StatusOptions{Check: true}, &stdout, &stderr); err != nil {
			t.Fatalf("--check = %v, want it to pass; stdout=%s", err, stdout.String())
		}
	})
}

func TestRunBootstrapDowngrade(t *testing.T) {
	t.Run("it warns and takes a confirmation before writing older content", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeBootstrapEnvVar, "downgrade")
		d := clitest.NewSession()
		clitest.SetLoggedIn(&d)
		clitest.StubAppFunctions(&d, nil)
		d.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		opts := Options{Features: "none", Declared: true}
		if err := Run(context.Background(), d, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "last written by 1.9.0") {
			t.Errorf("stdout never named the newer build that wrote the bootstrap; got:\n%s", out)
		}
		if !strings.Contains(out, "Aborted.") {
			t.Errorf("declining the downgrade still wrote; got:\n%s", out)
		}
	})
}
