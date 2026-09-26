package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestRunRollback(t *testing.T) {
	t.Run("with no argument it rolls back to the immediately previous promotion", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runRollback(context.Background(), deps, root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			`This will roll production of project "test-app" back to an earlier deployment`,
			"– live    promo-2",
			"– target  promo-1",
			"tag v1.0.0",
			"web=build-1",
			"Rolled back to promotion promo-1",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("--yes rolls back without asking", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runRollback(context.Background(), deps, root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if strings.Contains(out, "Roll production of") {
			t.Errorf("stdout = %q, want --yes to skip the confirmation", out)
		}
		if !strings.Contains(out, "Rolled back to promotion promo-1") {
			t.Errorf("stdout = %q, want --yes to roll back all the same", out)
		}
	})

	t.Run("--to rolls back to the named promotion once consented to", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runRollback(context.Background(), deps, root, rollbackOptions{to: "promo-1"}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, `Roll production of "test-app" back to promotion promo-1?`) {
			t.Errorf("stdout = %q, want the rollback to ask before it flips production", out)
		}
		if !strings.Contains(out, "Rolled back to promotion promo-1") {
			t.Errorf("stdout = %q, want it to report rolling back to promo-1", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("a declined confirmation rolls nothing back", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runRollback(context.Background(), deps, root, rollbackOptions{}, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Aborted.") {
			t.Errorf("stdout = %q, want a declined confirmation to say so", out)
		}
		if strings.Contains(out, "Rolled back") {
			t.Errorf("stdout = %q, want no rollback behind a declined confirmation", out)
		}
	})

	t.Run("--dry reads the history, prints the plan and rolls nothing back", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runRollback(context.Background(), deps, root, rollbackOptions{dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{"– live    promo-2", "– target  promo-1", "Run without --dry to roll back."} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "Rolled back") {
			t.Errorf("stdout = %q, want --dry to roll nothing back", out)
		}
	})

	t.Run("--tag rolls back to the tagged promotion and echoes the tag", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runRollback(context.Background(), deps, root, rollbackOptions{tag: "v1.0.0", yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Rolled back to promotion promo-1") {
			t.Errorf("stdout = %q, want it to report rolling back to promo-1", out)
		}
		if !strings.Contains(out, "tag v1.0.0") {
			t.Errorf("stdout = %q, want it to echo the target's tag", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("--to and --tag are mutually exclusive", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), deps, root, rollbackOptions{to: "promo-1", tag: "v1.0.0"}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want an error when both --to and --tag are set")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("err = %v, want it to report mutual exclusivity", err)
		}
	})

	t.Run("a tag no promotion has is refused before anything is rolled back", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), deps, root, rollbackOptions{tag: "v9.9.9", yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want an error for a tag nothing has")
		}
		for _, want := range []string{`"v9.9.9"`, "v1.0.0"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want it to name %q — what was asked for and what is there", stdout.String(), want)
			}
		}
		if strings.Contains(stdout.String(), "Rolled back") {
			t.Errorf("stdout = %q, want nothing rolled back", stdout.String())
		}
	})

	t.Run("an unlisted --to is refused before the rollback is asked for", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), deps, root, rollbackOptions{to: "no-such-promotion", yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want an error for an unknown promotion id")
		}
		for _, want := range []string{"no-such-promotion", "promo-2, promo-1"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want it to name %q — what was asked for and what is listed", stdout.String(), want)
			}
		}
		out := stdout.String()
		if strings.Contains(out, "This will roll production") || strings.Contains(out, "Rolled back") {
			t.Errorf("stdout = %q, want the refusal to come before the plan and the rollback", out)
		}
	})

	t.Run("it refuses on preview infrastructure", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), deps, root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want a class-mismatch error")
		}
		if !strings.Contains(stdout.String(), "this command needs production infrastructure") {
			t.Errorf("stdout = %q, want the concrete class-mismatch message", stdout.String())
		}
		if strings.Contains(stdout.String(), "Rolled back") {
			t.Errorf("stdout = %q, want no rollback to have been driven against preview infra", stdout.String())
		}
	})

	t.Run("it refuses when the infrastructure is absent", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "0")

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), deps, root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want a missing-infrastructure error")
		}
		if !strings.Contains(stdout.String(), "ocel bootstrap production") {
			t.Errorf("stdout = %q, want it to direct the user to `ocel bootstrap production`", stdout.String())
		}
		if strings.Contains(stdout.String(), "Rolled back") {
			t.Errorf("stdout = %q, want no rollback to have been driven", stdout.String())
		}
	})

	t.Run("without --yes it refuses without a terminal", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		err := runRollback(context.Background(), deps, root, rollbackOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("err = %v, want the no-TTY refusal to point at --yes", err)
		}
	})
}

func TestRollbackTarget(t *testing.T) {
	entry := func(id, tag string, active bool) *contractv1.PromotionHistoryEntry {
		return &contractv1.PromotionHistoryEntry{
			Promotion: &contractv1.Promotion{PromotionId: id, Tag: tag},
			Active:    active,
		}
	}

	t.Run("a tag on more than one promotion names none of them", func(t *testing.T) {
		history := []*contractv1.PromotionHistoryEntry{
			entry("p3", "release", true),
			entry("p2", "release", false),
			entry("p1", "", false),
		}
		_, err := rollbackTarget(history, "", "release")
		if err == nil {
			t.Fatal("rollbackTarget err = nil, want an ambiguous tag refused")
		}
		for _, want := range []string{`"release"`, "p3, p2", "--to"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("an empty history has nothing to roll back to", func(t *testing.T) {
		_, err := rollbackTarget(nil, "", "")
		if err == nil || !strings.Contains(err.Error(), "nothing to roll back to") {
			t.Errorf("err = %v, want an empty history refused", err)
		}
	})

	t.Run("the earliest promotion being live leaves nothing earlier", func(t *testing.T) {
		_, err := rollbackTarget([]*contractv1.PromotionHistoryEntry{entry("p1", "", true)}, "", "")
		if err == nil || !strings.Contains(err.Error(), "p1") {
			t.Errorf("err = %v, want the sole live promotion named as the earliest there is", err)
		}
	})

	t.Run("a history with nothing live is refused", func(t *testing.T) {
		history := []*contractv1.PromotionHistoryEntry{entry("p2", "", false), entry("p1", "", false)}
		_, err := rollbackTarget(history, "", "")
		if err == nil || !strings.Contains(err.Error(), "is live") {
			t.Errorf("err = %v, want a history with no live promotion refused", err)
		}
	})

	t.Run("the promotion before the live one is the default target", func(t *testing.T) {
		history := []*contractv1.PromotionHistoryEntry{entry("p3", "", false), entry("p2", "", true), entry("p1", "", false)}
		target, err := rollbackTarget(history, "", "")
		if err != nil {
			t.Fatalf("rollbackTarget err = %v", err)
		}
		if target.GetPromotionId() != "p1" {
			t.Errorf("target = %q, want the promotion before the live one", target.GetPromotionId())
		}
	})
}
