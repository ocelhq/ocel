package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRollback(t *testing.T) {
	t.Run("with no argument it rolls back to the immediately previous promotion", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "production")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runRollback(context.Background(), d, root, rollbackOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		if !strings.Contains(stdout.String(), "Rolled back to promotion promo-1") {
			t.Errorf("stdout = %q, want it to report rolling back to promo-1", stdout.String())
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("--to rolls back to the named promotion", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "production")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runRollback(context.Background(), d, root, rollbackOptions{to: "promo-2"}, &stdout, &stderr); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		if !strings.Contains(stdout.String(), "Rolled back to promotion promo-2") {
			t.Errorf("stdout = %q, want it to report rolling back to promo-2", stdout.String())
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("--tag rolls back to the tagged promotion and echoes the tag", func(t *testing.T) {
		root, sockPath := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "production")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runRollback(context.Background(), d, root, rollbackOptions{tag: "v1.0.0"}, &stdout, &stderr); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Rolled back to promotion promo-1") {
			t.Errorf("stdout = %q, want it to report rolling back to promo-1", out)
		}
		if !strings.Contains(out, "tag v1.0.0") {
			t.Errorf("stdout = %q, want it to echo the target's tag", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("--to and --tag are mutually exclusive", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), d, root, rollbackOptions{to: "promo-1", tag: "v1.0.0"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runRollback err = nil, want an error when both --to and --tag are set")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("err = %v, want it to report mutual exclusivity", err)
		}
	})

	t.Run("an invalid tag errors", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), d, root, rollbackOptions{tag: "feature/x"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runRollback err = nil, want an error for an invalid tag")
		}
		if !strings.Contains(err.Error(), "invalid character") {
			t.Errorf("err = %v, want it to explain the invalid character", err)
		}
	})

	t.Run("an unknown --to names the promotion it could not find", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "production")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), d, root, rollbackOptions{to: "no-such-promotion"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runRollback err = nil, want an error for an unknown promotion id")
		}
		if !strings.Contains(err.Error(), "no-such-promotion") {
			t.Errorf("err = %v, want it to name the unknown promotion id", err)
		}
	})

	t.Run("it refuses on preview infrastructure", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), d, root, rollbackOptions{}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runRollback err = nil, want a class-mismatch error")
		}
		if !strings.Contains(err.Error(), "ocel deploy can only run against production infrastructure") {
			t.Errorf("err = %v, want the concrete class-mismatch message", err)
		}
		if strings.Contains(stdout.String(), "Rolled back") {
			t.Errorf("stdout = %q, want no rollback to have been driven against preview infra", stdout.String())
		}
	})

	t.Run("it refuses when the infrastructure is absent", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "production")
		t.Setenv(fakeInfraPresentEnvVar, "0")

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), d, root, rollbackOptions{}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runRollback err = nil, want a missing-infrastructure error")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("err = %v, want it to direct the user to `ocel bootstrap`", err)
		}
		if strings.Contains(stdout.String(), "Rolled back") {
			t.Errorf("stdout = %q, want no rollback to have been driven", stdout.String())
		}
	})
}
