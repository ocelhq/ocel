package ports

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const cloudflareKind edge.Kind = "cloudflare"

func TestRemovals(t *testing.T) {
	t.Parallel()

	deployed := bootstrap.Deployed{
		Present:        true,
		StateBucket:    "ocel-state",
		ArtifactBucket: "ocel-artifacts",
		AssetBucket:    "ocel-assets",
		StateTable:     "ocel-state-table",
		VarsTable:      "ocel-vars",
		Features:       bootstrap.FeatureSet{bootstrap.FeatureISR: true, bootstrap.FeatureCloudflareEdge: true},
	}

	t.Run("it lists every surface the teardown touches", func(t *testing.T) {
		t.Parallel()

		surfaces, err := removals(bootstrap.ClassProduction, cloudflareKind, deployed, false)
		if err != nil {
			t.Fatalf("removals: %v", err)
		}
		for _, want := range []string{
			"cloudflare",
			"ocel-state",
			"ocel-artifacts",
			"ocel-assets",
			"ocel-state-table",
			"ocel-vars",
			bootstrap.EdgeUserName,
			bootstrap.StackName,
			bootstrap.EdgeCredentialsParamName,
			bootstrap.PassphraseParamName,
		} {
			if surfaceNamed(surfaces, want) == nil {
				t.Errorf("plan is missing a surface named %q; got %s", want, surfaceNames(surfaces))
			}
		}
		for _, surface := range surfaces {
			if surface.Kind == "" || surface.Reason == "" {
				t.Errorf("surface %+v carries no kind or reason", surface)
			}
			if !edge.ValidSurfaceAction(surface.Action) {
				t.Errorf("surface %q carries action %q, which is none the plan knows", surface.Name, surface.Action)
			}
		}
		for _, bucket := range []string{"ocel-state", "ocel-artifacts", "ocel-assets"} {
			if !surfaceNamed(surfaces, bucket).Slow {
				t.Errorf("bucket %s must be flagged slow: it is emptied object by object", bucket)
			}
		}
		if got := surfaceNamed(surfaces, bootstrap.PassphraseParamName); got.Action != edge.SurfaceDelete {
			t.Errorf("passphrase action = %v, want it deleted when no sibling bootstrap holds it", got.Action)
		}
		if surfaceNamed(surfaces, bootstrap.PreviewDomainParamName) != nil {
			t.Error("the production plan must not name the preview domain parameter")
		}
	})

	t.Run("a bootstrapped sibling keeps the passphrase, with the reason", func(t *testing.T) {
		t.Parallel()

		surfaces, err := removals(bootstrap.ClassPreview, cloudflareKind, deployed, true)
		if err != nil {
			t.Fatalf("removals: %v", err)
		}
		kept := surfaceNamed(surfaces, bootstrap.PassphraseParamName)
		if kept.Action != edge.SurfaceKeep {
			t.Fatalf("passphrase action = %v, want it kept", kept.Action)
		}
		if !strings.Contains(kept.Reason, bootstrap.ClassProduction) {
			t.Errorf("reason = %q, want it to name the bootstrap still holding it", kept.Reason)
		}
		if surfaceNamed(surfaces, bootstrap.PreviewDomainParamName) == nil {
			t.Error("the preview plan must name the preview domain parameter")
		}
	})

	t.Run("no cloudflare edge is no edge reader to delete", func(t *testing.T) {
		t.Parallel()

		bare := deployed
		bare.Features = bootstrap.FeatureSet{bootstrap.FeatureISR: true}
		surfaces, err := removals(bootstrap.ClassProduction, cloudflareKind, bare, false)
		if err != nil {
			t.Fatalf("removals: %v", err)
		}
		if surfaceNamed(surfaces, bootstrap.EdgeUserName) != nil {
			t.Errorf("plan names an edge reader this bootstrap never stood up; got %s", surfaceNames(surfaces))
		}
		if surfaceNamed(surfaces, bootstrap.FeatureStackName(bootstrap.FeatureCloudflareEdge, bootstrap.ClassProduction)) != nil {
			t.Errorf("plan names a feature stack this bootstrap does not carry; got %s", surfaceNames(surfaces))
		}
	})

	t.Run("a bucket the stack never reported is not planned blank", func(t *testing.T) {
		t.Parallel()

		partial := deployed
		partial.ArtifactBucket = ""
		surfaces, err := removals(bootstrap.ClassProduction, cloudflareKind, partial, false)
		if err != nil {
			t.Fatalf("removals: %v", err)
		}
		for _, surface := range surfaces {
			if surface.Name == "" {
				t.Errorf("plan carries a nameless surface %+v; got %s", surface, surfaceNames(surfaces))
			}
		}
	})

	t.Run("an absent bootstrap still plans its leftovers", func(t *testing.T) {
		t.Parallel()

		surfaces, err := removals(bootstrap.ClassProduction, cloudflareKind, bootstrap.Deployed{}, false)
		if err != nil {
			t.Fatalf("removals: %v", err)
		}
		if surfaceNamed(surfaces, bootstrap.StackName) != nil {
			t.Error("no stack is deployed, so none is planned for deletion")
		}
		if surfaceNamed(surfaces, bootstrap.EdgeCredentialsParamName) == nil {
			t.Error("the parameters the bootstrap left behind are still planned")
		}
	})
}

func surfaceNamed(surfaces []edge.Surface, name string) *edge.Surface {
	for i, surface := range surfaces {
		if surface.Name == name {
			return &surfaces[i]
		}
	}
	return nil
}

func surfaceNames(surfaces []edge.Surface) string {
	names := make([]string, 0, len(surfaces))
	for _, surface := range surfaces {
		names = append(names, surface.Name)
	}
	return strings.Join(names, ", ")
}
