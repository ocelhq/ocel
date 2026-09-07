package env

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestWritingWithoutTheVarsKey(t *testing.T) {
	t.Run("a write with no key to seal under names the bootstrap that adds one", func(t *testing.T) {
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), clitest.NewDeps(), root, "LOG_LEVEL", "debug", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatalf("runEnvSet err = nil, want it refused for want of a key; stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		if want := "vars-key"; !strings.Contains(err.Error(), want) {
			t.Errorf("runEnvSet err = %v, want it to name %s", err, want)
		}
		if want := "ocel bootstrap production --features"; !strings.Contains(err.Error(), want) {
			t.Errorf("runEnvSet err = %v, want it to name `%s`", err, want)
		}
	})

	t.Run("a read asks for no key at all", func(t *testing.T) {
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runEnvLs(context.Background(), clitest.NewDeps(), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvLs err = %v, want a read to go through a bootstrap with no key; stderr=%s", err, stderr.String())
		}
	})

	t.Run("a write goes through where the key stands", func(t *testing.T) {
		t.Setenv(clitest.FakeBootstrapEnvVar, "vars-key")
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runEnvSet(context.Background(), clitest.NewDeps(), root, "LOG_LEVEL", "debug", envOptions{}, nil, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSet err = %v, want the write to land; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Set LOG_LEVEL") {
			t.Errorf("stdout = %q, want the write reported", stdout.String())
		}
	})

	t.Run("a write asks for the key and for nothing else the bootstrap lacks", func(t *testing.T) {
		t.Setenv(clitest.FakeBootstrapEnvVar, "missing")
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), clitest.NewDeps(), root, "LOG_LEVEL", "debug", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatalf("runEnvSet err = nil, want it refused for want of a key; stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		if want := "ocel bootstrap production --features vars-key"; !strings.Contains(err.Error(), want) {
			t.Errorf("runEnvSet err = %v, want it to name `%s` alone", err, want)
		}
		if strings.Contains(err.Error(), "image-optimization") {
			t.Errorf("runEnvSet err = %v, want a write to ask for the key it seals under, not for what a deploy would need", err)
		}
	})

	t.Run("removing a value seals nothing, so it asks for no key", func(t *testing.T) {
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runEnvRm(context.Background(), clitest.NewDeps(), root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRm err = %v, want a removal to go through a bootstrap with no key; stderr=%s", err, stderr.String())
		}
	})

	t.Run("pointing a value at another seals nothing, so it asks for no key", func(t *testing.T) {
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		ref := envRefOptions{project: "platform"}
		if err := runEnvRef(context.Background(), clitest.NewDeps(), root, "STRIPE_API_KEY", envOptions{}, ref, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRef err = %v, want a reference to go through a bootstrap with no key; stderr=%s", err, stderr.String())
		}
	})

	t.Run("a terminal is offered the key and the bootstrap runs where it is taken", func(t *testing.T) {
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		journal := filepath.Join(t.TempDir(), "edge.journal")
		t.Setenv(clitest.FakeEdgeJournalEnvVar, journal)
		root := setUpEnvFixture(t)
		deps := clitest.NewDeps()
		deps.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), deps, root, "LOG_LEVEL", "debug", envOptions{}, strings.NewReader("y\n"), &stdout, &stderr)
		if err != nil {
			t.Fatalf("runEnvSet err = %v, want the offer taken and the write landed; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if want := "Run `ocel bootstrap production --features vars-key` now?"; !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want the offer put to whoever is at the terminal", stderr.String())
		}
		if strings.Contains(stdout.String(), "Run `ocel bootstrap") {
			t.Errorf("stdout = %q, want the offer kept off the stream a script reads", stdout.String())
		}
		written, err := os.ReadFile(journal)
		if err != nil {
			t.Fatalf("the bootstrap the offer accepted never reached the provider: %v", err)
		}
		if !strings.Contains(string(written), "features=vars-key") {
			t.Errorf("the provider was asked for %q, want the key alone", strings.TrimSpace(string(written)))
		}
	})
}

func TestAProviderWithoutTheVarsKeyFeature(t *testing.T) {
	t.Run("a write against a catalogue that never lists the key goes straight through", func(t *testing.T) {
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		t.Setenv(clitest.FakeCatalogueEnvVar, clitest.FakeCatalogueNone)
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runEnvSet(context.Background(), clitest.NewDeps(), root, "LOG_LEVEL", "debug", envOptions{}, nil, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSet err = %v, want a provider that has no such feature never asked for it; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Set LOG_LEVEL") {
			t.Errorf("stdout = %q, want the write reported", stdout.String())
		}
	})

	t.Run("a terminal is offered nothing and the provider is left unbootstrapped", func(t *testing.T) {
		t.Setenv(clitest.FakeBootstrapEnvVar, "current")
		t.Setenv(clitest.FakeCatalogueEnvVar, clitest.FakeCatalogueNone)
		journal := filepath.Join(t.TempDir(), "edge.journal")
		t.Setenv(clitest.FakeEdgeJournalEnvVar, journal)
		root := setUpEnvFixture(t)
		deps := clitest.NewDeps()
		deps.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		if err := runEnvSet(context.Background(), deps, root, "LOG_LEVEL", "debug", envOptions{}, strings.NewReader("y\n"), &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSet err = %v, want the write to land unbidden; stderr=%s", err, stderr.String())
		}
		if strings.Contains(stderr.String(), "Run `ocel bootstrap") {
			t.Errorf("stderr = %q, want no offer of a feature this provider has no name for", stderr.String())
		}
		if _, err := os.Stat(journal); !os.IsNotExist(err) {
			t.Errorf("the provider was bootstrapped for a feature it does not carry: %v", err)
		}
	})
}
