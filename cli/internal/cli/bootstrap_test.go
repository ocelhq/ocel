package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBootstrapDestroy(t *testing.T) {
	t.Run("it renders the plan, kept items included, and takes the class name", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		d.stdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		opts := bootstrapOptions{destroy: true, preview: true}
		if err := runBootstrap(context.Background(), d, root, opts, &stdout, &stderr, strings.NewReader("preview\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			"fronted by the cloudfront edge",
			"delete edge bootstrap cloudfront",
			"delete bucket ocel-state-preview",
			"(this one is slow)",
			"Left in place:",
			"keep parameter /ocel/pulumi/passphrase — the production bootstrap still stands",
			"Type the class name (preview) to confirm:",
			"TEARDOWN tier=TIER_PREVIEW",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Index(out, "keep parameter") < strings.Index(out, "This cannot be undone.") {
			t.Errorf("kept items must follow the doomed ones; got:\n%s", out)
		}
	})

	t.Run("a phrase that is not the class name aborts", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		d.stdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		opts := bootstrapOptions{destroy: true}
		if err := runBootstrap(context.Background(), d, root, opts, &stdout, &stderr, strings.NewReader("preview\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s", err, stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Aborted.") {
			t.Errorf("stdout = %q, want the teardown aborted", out)
		}
		if strings.Contains(out, "TEARDOWN") {
			t.Errorf("stdout = %q, want no teardown after an unconfirmed phrase", out)
		}
	})

	t.Run("--yes skips the phrase and the terminal requirement", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		opts := bootstrapOptions{destroy: true, yes: true}
		if err := runBootstrap(context.Background(), d, root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "Type the class name") {
			t.Errorf("stdout = %q, want --yes to skip the typed phrase", out)
		}
		if !strings.Contains(out, "TEARDOWN tier=TIER_PRODUCTION") {
			t.Errorf("stdout = %q, want the production teardown", out)
		}
	})

	t.Run("the bypass env skips the phrase and says so", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(destroyBypassEnv, "production")

		var stdout, stderr bytes.Buffer
		opts := bootstrapOptions{destroy: true}
		if err := runBootstrap(context.Background(), d, root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "Type the class name") {
			t.Errorf("stdout = %q, want the bypass to skip the typed phrase", stdout.String())
		}
		if !strings.Contains(stderr.String(), destroyBypassEnv) {
			t.Errorf("stderr = %q, want it to name %s so an unconfirmed teardown is never silent", stderr.String(), destroyBypassEnv)
		}
	})

	t.Run("a bypass naming the other bootstrap is refused, not ignored", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(destroyBypassEnv, "preview")

		var stdout, stderr bytes.Buffer
		opts := bootstrapOptions{destroy: true}
		err := runBootstrap(context.Background(), d, root, opts, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want the mismatched-bypass refusal")
		}
		for _, want := range []string{destroyBypassEnv, "preview", "production"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("without a terminal, a phrase it cannot ask for is a refusal", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		opts := bootstrapOptions{destroy: true}
		err := runBootstrap(context.Background(), d, root, opts, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want the no-terminal refusal")
		}
		for _, want := range []string{"interactive terminal", "--yes", destroyBypassEnv} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})
}

func TestRunBootstrap(t *testing.T) {
	t.Parallel()

	t.Run("a missing config errors before any spawn", func(t *testing.T) {
		t.Parallel()

		err := runBootstrap(context.Background(), defaultDeps(), t.TempDir(), bootstrapOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %v, want it to hint at `ocel init`", err)
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := runBootstrap(context.Background(), defaultDeps(), root, bootstrapOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
	})
}

func TestRunBootstrapPrintPolicy(t *testing.T) {
	t.Run("it writes the document the provider renders for the tier", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		opts := bootstrapOptions{printPolicy: "deploy"}
		if err := runBootstrap(context.Background(), d, root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "CREDENTIAL_TIER_DEPLOY") {
			t.Errorf("stdout = %q, want the deploy tier's document", stdout.String())
		}
	})

	t.Run("a tier that is neither names the two that are", func(t *testing.T) {
		err := runBootstrap(context.Background(), defaultDeps(), t.TempDir(), bootstrapOptions{printPolicy: "admin"}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
		for _, want := range []string{"bootstrap", "deploy", `"admin"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %s", err, want)
			}
		}
	})

	t.Run("it refuses to print and destroy in one run", func(t *testing.T) {
		err := runBootstrap(context.Background(), defaultDeps(), t.TempDir(), bootstrapOptions{printPolicy: "deploy", destroy: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "one or the other") {
			t.Fatalf("runBootstrap err = %v, want it to refuse both at once", err)
		}
	})
}
