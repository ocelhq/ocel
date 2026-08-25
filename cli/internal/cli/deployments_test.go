package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestRunDeploymentsLs(t *testing.T) {
	t.Run("it renders promotions newest first with the active marker", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubAppFunctions(&sess, nil)
		t.Setenv(clitest.FakeInfraClassEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPromotionsLs(context.Background(), sess, root, &stdout, &stderr); err != nil {
			t.Fatalf("runPromotionsLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, sub := range []string{"ID", "TAG", "CREATED", "STATUS", "promo-2", "promo-1", "v1.0.0", "active"} {
			if !strings.Contains(out, sub) {
				t.Errorf("stdout = %q, want it to contain %q", out, sub)
			}
		}

		promo2Idx := strings.Index(out, "promo-2")
		promo1Idx := strings.Index(out, "promo-1")
		if promo2Idx == -1 || promo1Idx == -1 || promo2Idx > promo1Idx {
			t.Errorf("stdout = %q, want promo-2 (newest) listed before promo-1", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("it shows each app's shipped identity under an aligned column", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubAppFunctions(&sess, nil)
		t.Setenv(clitest.FakeInfraClassEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPromotionsLs(context.Background(), sess, root, &stdout, &stderr); err != nil {
			t.Fatalf("runPromotionsLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, sub := range []string{"DEPLOYED", "admin=build-2", "web=build-2~fp2", "web=build-1"} {
			if !strings.Contains(out, sub) {
				t.Errorf("stdout = %q, want it to contain %q", out, sub)
			}
		}

		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("stdout = %q, want a header and two rows", out)
		}
		if a, b := runeIndex(lines[0], "DEPLOYED"), runeIndex(lines[1], "admin="); a != b {
			t.Errorf("DEPLOYED column starts at %d in the header and %d in the row:\n%s", a, b, out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("it refuses on preview infrastructure", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubAppFunctions(&sess, nil)
		t.Setenv(clitest.FakeInfraClassEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runPromotionsLs(context.Background(), sess, root, &stdout, &stderr)
		if err == nil {
			t.Fatal("runPromotionsLs err = nil, want a class-mismatch error")
		}
		if !strings.Contains(err.Error(), "ocel deploy can only run against production infrastructure") {
			t.Errorf("err = %v, want the concrete class-mismatch message", err)
		}
	})
}

func runeIndex(line, substr string) int {
	at := strings.Index(line, substr)
	if at < 0 {
		return at
	}
	return len([]rune(line[:at]))
}

func TestRunDeploymentsPrune(t *testing.T) {
	t.Run("it reports the reclaimed and the kept promotions", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubAppFunctions(&sess, nil)
		t.Setenv(clitest.FakeInfraClassEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runPromotionsPrune(context.Background(), sess, root, 10, &stdout, &stderr); err != nil {
			t.Fatalf("runPromotionsPrune err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Reclaimed 1 promotion(s): promo-1") {
			t.Errorf("stdout = %q, want it to report the reclaimed promotion", out)
		}
		if !strings.Contains(out, "Kept 1 promotion(s).") {
			t.Errorf("stdout = %q, want it to report the kept promotion count", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("it refuses on preview infrastructure", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		sess := newSession()
		clitest.SetLoggedIn(&sess)
		clitest.StubAppFunctions(&sess, nil)
		t.Setenv(clitest.FakeInfraClassEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runPromotionsPrune(context.Background(), sess, root, 10, &stdout, &stderr)
		if err == nil {
			t.Fatal("runPromotionsPrune err = nil, want a class-mismatch failure")
		}
		out := stdout.String()
		if !strings.Contains(out, "ocel deploy can only run against production infrastructure") {
			t.Errorf("stdout = %q, want the concrete class-mismatch message", out)
		}
		if strings.Contains(out, "Reclaimed") {
			t.Errorf("stdout = %q, want no prune to have been driven against preview infra", out)
		}
	})
}
