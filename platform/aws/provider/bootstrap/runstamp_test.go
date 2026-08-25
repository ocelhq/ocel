package bootstrap

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestRunStamps(t *testing.T) {
	t.Run("every stack it writes carries the schema, its own digest and the writer", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		req := everything()
		req.Writer = "1.9.0"
		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, req, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, name := range cfn.stacks() {
			stamp := cfn.stampOf(name)
			if stamp.Schema != RequiredSchema {
				t.Errorf("%s carries schema %d, want %d", name, stamp.Schema, RequiredSchema)
			}
			if want := TemplateDigest(cfn.template(name)); stamp.Digest != want {
				t.Errorf("%s carries digest %q, want the sha256 of its own body %q", name, stamp.Digest, want)
			}
			if stamp.WrittenBy != "1.9.0" {
				t.Errorf("%s was written by %q, want 1.9.0", name, stamp.WrittenBy)
			}
		}
		deployed, err := CheckDeployed(context.Background(), cfn, nil)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if stale := deployed.Stale(featureNames()); len(stale) != 0 {
			t.Errorf("a bootstrap this build just wrote reads as stale: %+v", stale)
		}
	})

	t.Run("a dev build stamps a version that never parses", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{})

		req := Request{Writer: "dev+cafebabe"}
		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, req, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		stamp := cfn.stampOf(StackName)
		if stamp.WrittenBy != "dev+cafebabe" {
			t.Errorf("written by %q, want dev+cafebabe", stamp.WrittenBy)
		}
		if providerkit.Writer(stamp.WrittenBy).Release() {
			t.Errorf("%q must never read as a release", stamp.WrittenBy)
		}
	})
}
