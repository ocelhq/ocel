package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRollback_NoArg_RollsBackToImmediatelyPreviousPromotion(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	if err := runRollback(context.Background(), root, rollbackOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), "Rolled back to promotion promo-1") {
		t.Errorf("stdout = %q, want it to report rolling back to promo-1", stdout.String())
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunRollback_To_RollsBackToNamedPromotion(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	if err := runRollback(context.Background(), root, rollbackOptions{to: "promo-2"}, &stdout, &stderr); err != nil {
		t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), "Rolled back to promotion promo-2") {
		t.Errorf("stdout = %q, want it to report rolling back to promo-2", stdout.String())
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunRollback_UnknownTo_ErrorsClearly(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	err := runRollback(context.Background(), root, rollbackOptions{to: "no-such-promotion"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runRollback err = nil, want an error for an unknown promotion id")
	}
	if !strings.Contains(err.Error(), "no-such-promotion") {
		t.Errorf("err = %v, want it to name the unknown promotion id", err)
	}
}

func TestRunRollback_RefusesOnPreviewInfrastructure(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	err := runRollback(context.Background(), root, rollbackOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runRollback err = nil, want a class-mismatch error")
	}
	if !strings.Contains(err.Error(), "ocel deploy can only run against production infrastructure") {
		t.Errorf("err = %v, want the concrete class-mismatch message", err)
	}
	if strings.Contains(stdout.String(), "Rolled back") {
		t.Errorf("stdout = %q, want no rollback to have been driven against preview infra", stdout.String())
	}
}

func TestRunRollback_RefusesWhenInfraAbsent(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "0")

	var stdout, stderr bytes.Buffer
	err := runRollback(context.Background(), root, rollbackOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runRollback err = nil, want a missing-infrastructure error")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("err = %v, want it to direct the user to `ocel bootstrap`", err)
	}
	if strings.Contains(stdout.String(), "Rolled back") {
		t.Errorf("stdout = %q, want no rollback to have been driven", stdout.String())
	}
}
