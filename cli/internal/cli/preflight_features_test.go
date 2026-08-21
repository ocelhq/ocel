package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestACommandThatReadsTheSubstrateNamesTheFeatureItLacks(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	d := defaultDeps()
	setLoggedIn(&d)
	stubAppFunctions(&d, nil)
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")
	t.Setenv(fakeSubstrateEnvVar, "missing")

	var stdout, stderr bytes.Buffer
	err := runDeploymentsLs(context.Background(), d, root, &stdout, &stderr)
	if err == nil {
		t.Fatal("a command reading a substrate that lacks a feature this project needs ran on regardless")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap --features image-optimization,isr") {
		t.Errorf("refusal = %q, want the literal command to run", err)
	}
}

func TestEnvAsksTheSubstrateOnlyWhetherThisCLICanSpeakToIt(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	d := defaultDeps()
	setLoggedIn(&d)
	stubAppFunctions(&d, nil)
	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")
	t.Setenv(fakeSubstrateEnvVar, "missing")

	var stdout, stderr bytes.Buffer
	if err := runEnvLs(context.Background(), d, root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("a variable this substrate holds was refused over a feature no variable needs: %v", err)
	}
}
