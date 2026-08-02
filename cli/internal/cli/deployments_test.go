package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunDeploymentsLs_RendersPromotionsNewestFirstWithActiveMarker(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	if err := runDeploymentsLs(context.Background(), root, &stdout, &stderr); err != nil {
		t.Fatalf("runDeploymentsLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
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
}

// An operator auditing a promotion starts from what each app actually shipped,
// and a Deployment's identity is that: the build id plus the fingerprint of the
// values baked into it. Two promotions of one build differ only there, so
// rendering the bare build id would show them as identical.
func TestRunDeploymentsLs_ShowsEachAppsShippedIdentity(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	if err := runDeploymentsLs(context.Background(), root, &stdout, &stderr); err != nil {
		t.Fatalf("runDeploymentsLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, sub := range []string{"DEPLOYED", "admin=build-2", "web=build-2~fp2", "web=build-1"} {
		if !strings.Contains(out, sub) {
			t.Errorf("stdout = %q, want it to contain %q", out, sub)
		}
	}

	// The colored active cell is last, so every column before it stays aligned.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q, want a header and two rows", out)
	}
	if a, b := runeIndex(lines[0], "DEPLOYED"), runeIndex(lines[1], "admin="); a != b {
		t.Errorf("DEPLOYED column starts at %d in the header and %d in the row:\n%s", a, b, out)
	}

	waitForNoStaleSocket(t, sockPath)
}

// runeIndex is where substr starts in printed columns rather than in bytes:
// the table's own em-dash placeholder is one column and three bytes.
func runeIndex(line, substr string) int {
	at := strings.Index(line, substr)
	if at < 0 {
		return at
	}
	return len([]rune(line[:at]))
}

func TestRunDeploymentsLs_RefusesOnPreviewInfrastructure(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	err := runDeploymentsLs(context.Background(), root, &stdout, &stderr)
	if err == nil {
		t.Fatal("runDeploymentsLs err = nil, want a class-mismatch error")
	}
	if !strings.Contains(err.Error(), "ocel deploy can only run against production infrastructure") {
		t.Errorf("err = %v, want the concrete class-mismatch message", err)
	}
}

func TestRunDeploymentsPrune_ReportsReclaimedAndKeptPromotions(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	if err := runDeploymentsPrune(context.Background(), root, 10, &stdout, &stderr); err != nil {
		t.Fatalf("runDeploymentsPrune err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "Reclaimed 1 promotion(s): promo-1") {
		t.Errorf("stdout = %q, want it to report the reclaimed promotion", out)
	}
	if !strings.Contains(out, "Kept 1 promotion(s).") {
		t.Errorf("stdout = %q, want it to report the kept promotion count", out)
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploymentsPrune_RefusesOnPreviewInfrastructure(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	err := runDeploymentsPrune(context.Background(), root, 10, &stdout, &stderr)
	if err == nil {
		t.Fatal("runDeploymentsPrune err = nil, want a class-mismatch failure")
	}
	// The refusal is rendered through the deploy UI (like deploy/bootstrap) and
	// the command returns the sentinel exit error, so the concrete message lands
	// in the rendered output rather than the returned error.
	out := stdout.String()
	if !strings.Contains(out, "ocel deploy can only run against production infrastructure") {
		t.Errorf("stdout = %q, want the concrete class-mismatch message", out)
	}
	if strings.Contains(out, "Reclaimed") {
		t.Errorf("stdout = %q, want no prune to have been driven against preview infra", out)
	}
}
