package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func substrate(tier environmentv1.Tier, stacks ...*contractv1.SubstrateStack) *contractv1.SubstrateStatus {
	return &contractv1.SubstrateStatus{
		Tier:           tier,
		Present:        true,
		Schema:         1,
		RequiredSchema: 1,
		Writer:         "1.4.0",
		Stacks:         stacks,
	}
}

func TestRenderSubstrateStatus(t *testing.T) {
	t.Run("it reports every column for every stack", func(t *testing.T) {
		status := substrate(environmentv1.Tier_TIER_PRODUCTION,
			&contractv1.SubstrateStack{Name: "ocel-bootstrap", Present: true, Schema: 1, DigestCurrent: true, WrittenBy: "1.4.0"},
			&contractv1.SubstrateStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Schema: 1, WrittenBy: "1.3.0"},
		)
		status.AutoHeal = true

		var out bytes.Buffer
		renderSubstrateStatus(&out, status)

		for _, want := range []string{"production", "ocel-bootstrap-isr", "isr", "ok", "stale", "1.3.0", "on"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("report missing %q; got:\n%s", want, out.String())
			}
		}
	})

	t.Run("an absent substrate says so and nothing more", func(t *testing.T) {
		var out bytes.Buffer
		renderSubstrateStatus(&out, &contractv1.SubstrateStatus{Tier: environmentv1.Tier_TIER_PREVIEW, RequiredSchema: 1})

		if got := out.String(); got != "preview: not bootstrapped\n" {
			t.Errorf("report = %q, want the one line that says it is not there", got)
		}
	})

	t.Run("it names the side that has to move", func(t *testing.T) {
		behind := substrate(environmentv1.Tier_TIER_PRODUCTION,
			&contractv1.SubstrateStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true})
		behind.Schema, behind.RequiredSchema = 1, 2
		ahead := substrate(environmentv1.Tier_TIER_PRODUCTION,
			&contractv1.SubstrateStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true})
		ahead.Schema, ahead.RequiredSchema = 3, 2

		if got := substrateProblem(behind); !strings.Contains(got, "ocel bootstrap") {
			t.Errorf("problem = %q, want it to point at bootstrap", got)
		}
		if got := substrateProblem(ahead); !strings.Contains(got, "upgrade the Ocel CLI") {
			t.Errorf("problem = %q, want it to point at the CLI", got)
		}
	})
}

func TestSubstrateCheck(t *testing.T) {
	current := substrate(environmentv1.Tier_TIER_PRODUCTION,
		&contractv1.SubstrateStack{Name: "ocel-bootstrap", Present: true, Schema: 1, DigestCurrent: true})
	stale := substrate(environmentv1.Tier_TIER_PREVIEW,
		&contractv1.SubstrateStack{Name: "ocel-bootstrap-preview", Present: true, Schema: 1})
	behind := substrate(environmentv1.Tier_TIER_PRODUCTION,
		&contractv1.SubstrateStack{Name: "ocel-bootstrap", Present: true, Schema: 1, DigestCurrent: true})
	behind.RequiredSchema = 2

	for _, tc := range []struct {
		name     string
		statuses []*contractv1.SubstrateStatus
		wantErr  bool
	}{
		{"a substrate this build wrote passes", []*contractv1.SubstrateStatus{current}, false},
		{"an absent substrate passes", []*contractv1.SubstrateStatus{{Tier: environmentv1.Tier_TIER_PREVIEW, RequiredSchema: 1}}, false},
		{"stale content fails", []*contractv1.SubstrateStatus{current, stale}, true},
		{"an older schema fails", []*contractv1.SubstrateStatus{behind}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := substrateCheck(tc.statuses)
			if tc.wantErr && err == nil {
				t.Fatal("substrateCheck passed a substrate the CLI cannot use as it stands")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("substrateCheck = %v, want nil", err)
			}
		})
	}
}

func TestRunBootstrapStatus(t *testing.T) {
	t.Run("it reports both classes", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		t.Setenv(fakeSubstrateEnvVar, "current")
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		if err := runBootstrapStatus(context.Background(), d, root, bootstrapStatusOptions{}, &stdout, &stderr); err != nil {
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
		root, _ := setUpDeployFixture(t)
		t.Setenv(fakeSubstrateEnvVar, "stale")
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		err := runBootstrapStatus(context.Background(), d, root, bootstrapStatusOptions{check: true}, &stdout, &stderr)
		if err == nil {
			t.Fatalf("--check passed a substrate carrying stale content; stdout=%s", stdout.String())
		}
		if !strings.Contains(err.Error(), "ocel-bootstrap-isr") {
			t.Errorf("err = %v, want it to name the stale stack", err)
		}
	})

	t.Run("--check passes a substrate this build wrote", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		t.Setenv(fakeSubstrateEnvVar, "current")
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)

		var stdout, stderr bytes.Buffer
		if err := runBootstrapStatus(context.Background(), d, root, bootstrapStatusOptions{check: true}, &stdout, &stderr); err != nil {
			t.Fatalf("--check = %v, want it to pass; stdout=%s", err, stdout.String())
		}
	})
}

func TestRunBootstrapDowngrade(t *testing.T) {
	t.Run("it warns and takes a confirmation before writing older content", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		t.Setenv(fakeSubstrateEnvVar, "downgrade")
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		d.stdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		opts := bootstrapOptions{features: "none", declared: true}
		if err := runBootstrap(context.Background(), d, root, opts, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "last written by 1.9.0") {
			t.Errorf("stdout never named the newer build that wrote the substrate; got:\n%s", out)
		}
		if !strings.Contains(out, "Aborted.") {
			t.Errorf("declining the downgrade still wrote; got:\n%s", out)
		}
	})
}
