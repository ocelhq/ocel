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
	for _, sub := range []string{"promo-2", "promo-1", "*"} {
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
