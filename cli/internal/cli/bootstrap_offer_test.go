package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func bootstrapOf(stacks ...*contractv1.BootstrapStack) *contractv1.BootstrapStatus {
	return &contractv1.BootstrapStatus{
		Tier:           environmentv1.Tier_TIER_PRODUCTION,
		Present:        true,
		Schema:         1,
		RequiredSchema: 1,
		Stacks:         stacks,
	}
}

func TestPlanBootstrap(t *testing.T) {
	core := &contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true}

	tests := []struct {
		name     string
		status   *contractv1.BootstrapStatus
		missing  []string
		stale    []string
		features []string
	}{
		{
			name:   "a bootstrap nothing has been deployed into asks for nothing",
			status: &contractv1.BootstrapStatus{Tier: environmentv1.Tier_TIER_PRODUCTION},
		},
		{
			name: "everything this project needs is there and current",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			),
			features: []string{"isr"},
		},
		{
			name: "a required feature that is not there is added",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
			),
			missing:  []string{"image-optimization"},
			features: []string{"image-optimization", "isr"},
		},
		{
			name: "a required feature that has fallen behind is refreshed",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
			),
			stale:    []string{"ocel-bootstrap-isr"},
			features: []string{"isr"},
		},
		{
			name: "the core falling behind is a refresh of its own",
			status: bootstrapOf(
				&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, Required: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			),
			stale:    []string{"ocel-bootstrap"},
			features: []string{"isr"},
		},
		{
			name: "a feature no project here needs is neither added nor refreshed",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization"},
			),
			features: []string{"isr"},
		},
		{
			name: "one set covers both what is missing and what is behind",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
			),
			missing:  []string{"image-optimization"},
			stale:    []string{"ocel-bootstrap-isr"},
			features: []string{"image-optimization", "isr"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planBootstrap(tt.status)
			if !slices.Equal(plan.missing, tt.missing) {
				t.Errorf("missing = %v, want %v", plan.missing, tt.missing)
			}
			if !slices.Equal(plan.stale, tt.stale) {
				t.Errorf("stale = %v, want %v", plan.stale, tt.stale)
			}
			if !slices.Equal(plan.features, tt.features) {
				t.Errorf("features = %v, want %v", plan.features, tt.features)
			}
			if plan.empty() != (len(tt.missing) == 0 && len(tt.stale) == 0) {
				t.Errorf("empty() = %t for %v/%v", plan.empty(), plan.missing, plan.stale)
			}
		})
	}
}

type lineByLine struct{ lines []string }

func (r *lineByLine) Read(p []byte) (int, error) {
	if len(r.lines) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.lines[0])
	r.lines = r.lines[1:]
	return n, nil
}

func TestOfferBootstrapWithoutATerminal(t *testing.T) {
	core := &contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true}

	t.Run("a missing feature stops the run and names the command", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
		)
		var out bytes.Buffer
		err := offerBootstrap(context.Background(), nil, status, environmentv1.Tier_TIER_PRODUCTION, false, &out, strings.NewReader(""))
		if err == nil {
			t.Fatal("a deploy against a bootstrap missing a feature it needs was allowed through")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap --features image-optimization,isr") {
			t.Errorf("refusal = %q, want the literal command to run", err)
		}
	})

	t.Run("a stale stack warns and lets the deploy through", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
		)
		var out bytes.Buffer
		if err := offerBootstrap(context.Background(), nil, status, environmentv1.Tier_TIER_PREVIEW, false, &out, strings.NewReader("")); err != nil {
			t.Fatalf("a bootstrap that is merely behind stopped the deploy: %v", err)
		}
		for _, want := range []string{"ocel-bootstrap-isr", "ocel bootstrap --preview --features isr"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("stdout = %q, want it to carry %q", out.String(), want)
			}
		}
	})

	t.Run("a bootstrap that carries what this project needs says nothing", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
		)
		var out bytes.Buffer
		if err := offerBootstrap(context.Background(), nil, status, environmentv1.Tier_TIER_PRODUCTION, false, &out, strings.NewReader("")); err != nil {
			t.Fatalf("offerBootstrap err = %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want nothing said about a bootstrap that is what it should be", out.String())
		}
	})
}

func TestOfferBootstrapDeclined(t *testing.T) {
	status := bootstrapOf(
		&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true},
		&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Required: true},
	)

	var out bytes.Buffer
	err := offerBootstrap(context.Background(), nil, status, environmentv1.Tier_TIER_PRODUCTION, true, &out, strings.NewReader("n\n"))
	if err == nil {
		t.Fatal("declining the offer let a deploy run against a bootstrap without the feature it needs")
	}
	if !strings.Contains(out.String(), "add isr") {
		t.Errorf("stdout = %q, want the offer to name what it would add", out.String())
	}
}

func TestDeployOffersToBootstrapTheExactSet(t *testing.T) {
	root, journal, d := setUpEdgeFixture(t, "")
	t.Setenv(fakeBootstrapEnvVar, "missing")
	d.stdinIsTerminal = func(io.Reader) bool { return true }

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), d, root, deployOptions{}, &stdout, &stderr, &lineByLine{lines: []string{"y\n", "y\n"}}); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := readJournal(t, journal)
	if len(got) != 2 {
		t.Fatalf("the provider was reached %d times, want a bootstrap and then the deploy: %v", len(got), got)
	}
	if got[0] != "features=image-optimization,isr force=false" {
		t.Errorf("bootstrap ran with %q, want the set already there plus what is missing", got[0])
	}
	if !strings.Contains(stdout.String(), "add image-optimization") {
		t.Errorf("stdout = %q, want the offer to name what it would add", stdout.String())
	}
}

func TestDeployYesNeverStopsToAsk(t *testing.T) {
	root, journal, d := setUpEdgeFixture(t, "")
	t.Setenv(fakeBootstrapEnvVar, "missing")
	d.stdinIsTerminal = func(io.Reader) bool { return true }

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("an unattended deploy against a bootstrap missing a feature it needs was allowed through")
	}
	if !strings.Contains(stdout.String(), "Run `ocel bootstrap --features image-optimization,isr` and try again") {
		t.Errorf("stdout = %q, want the literal command to run", stdout.String())
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Errorf("the provider was reached; --yes answers questions about the deploy, it does not order a bootstrap")
	}
}

func TestBootstrapCarriesAutoHeal(t *testing.T) {
	tests := []struct {
		name string
		opts bootstrapOptions
		want string
	}{
		{"an unset switch leaves the account as it is", bootstrapOptions{yes: true}, "features=isr force=false"},
		{"--auto-heal turns it on", bootstrapOptions{yes: true, healing: true, autoHeal: true}, "features=isr force=false autoHeal=true"},
		{"--auto-heal=false takes it back", bootstrapOptions{yes: true, healing: true}, "features=isr force=false autoHeal=false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, journal, d := setUpEdgeFixture(t, "")
			t.Setenv(fakeEnabledFeaturesEnvVar, "isr")

			var stdout, stderr bytes.Buffer
			if err := runBootstrap(context.Background(), d, root, tt.opts, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			got := readJournal(t, journal)
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("provider saw %v, want %q", got, tt.want)
			}
		})
	}
}
