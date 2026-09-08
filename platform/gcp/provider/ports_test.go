package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func standing(t *testing.T) *gcp.Provider {
	t.Helper()
	return newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
}

func newProvider(t *testing.T, options gcp.Options) *gcp.Provider {
	t.Helper()
	p, err := gcp.NewProvider(context.Background(), options)
	if err != nil {
		t.Fatalf("NewProvider(%+v) = %v", options, err)
	}
	return p
}

func TestEveryPortThisPhaseHasNotBuiltSaysSoRatherThanReadingAsDone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := standing(t)
	ref := providerkit.StackRef{Project: "acme", Class: providerkit.ClassProduction}

	for name, refused := range map[string]error{
		"Releaser.Plan":        errorOf(p.Releases().Plan(ctx, providerkit.StackPlan{Ref: ref}, nil)),
		"Releaser.Provision":   errorOf(p.Releases().Provision(ctx, providerkit.StackPlan{Ref: ref}, nil)),
		"Releaser.PlanDestroy": errorOf(p.Releases().PlanDestroy(ctx, ref, nil)),
		"Releaser.Destroy":     p.Releases().Destroy(ctx, ref, nil),
	} {
		var refusal providerkit.Refusal
		if !errors.As(refused, &refusal) || refusal.Code != providerkit.CodeNotReady {
			t.Errorf("%s() = %v, want a %s refusal so a run stops here rather than continuing against nothing", name, refused, providerkit.CodeNotReady)
			continue
		}
		if !strings.HasPrefix(refusal.Message, "gcp: ") {
			t.Errorf("%s() refused with %q, want it to name the provider the refusal came from", name, refusal.Message)
		}
	}
}

func TestNoPortIsNilForTheKitToCallThrough(t *testing.T) {
	t.Parallel()

	p := standing(t)
	for name, port := range map[string]any{
		"Releases":    p.Releases(),
		"Artifacts":   p.Artifacts(),
		"Records":     p.Records(),
		"Sealer":      p.Sealer(),
		"Credentials": p.Credentials(),
		"Edges":       p.Edges(),
		"DNS":         p.DNS(),
	} {
		if port == nil {
			t.Errorf("%s() is nil, and the kit calls methods on it", name)
		}
	}
}

func TestAnArtifactThatNamesNoClassOrNoStoreIsTheCallersMistake(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := standing(t)
	classless := providerkit.ArtifactRef{Bucket: providerkit.StoreFunctions, Key: "bundle.zip"}
	storeless := providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: "somewhere-else", Key: "bundle.zip"}

	for name, refused := range map[string]error{
		"Put with no class":  p.Artifacts().Put(ctx, classless, bytes.NewReader(nil)),
		"Has with no class":  errorOf(p.Artifacts().Has(ctx, classless)),
		"Open with no class": errorOf(p.Artifacts().Open(ctx, classless)),
		"Put with no store":  p.Artifacts().Put(ctx, storeless, bytes.NewReader(nil)),
		"Has with no store":  errorOf(p.Artifacts().Has(ctx, storeless)),
		"Open with no store": errorOf(p.Artifacts().Open(ctx, storeless)),
	} {
		var refusal providerkit.Refusal
		if !errors.As(refused, &refusal) || refusal.Code != providerkit.CodeInvalid {
			t.Errorf("%s = %v, want an %s refusal", name, refused, providerkit.CodeInvalid)
		}
	}
}

func TestSealingAValueThatNamesNoClassIsTheCallersMistake(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := standing(t)

	for name, refused := range map[string]error{
		"Seal": errorOf(p.Sealer().Seal(ctx, providerkit.Coordinate{}, nil)),
		"Open": errorOf(p.Sealer().Open(ctx, providerkit.Coordinate{}, nil)),
	} {
		var refusal providerkit.Refusal
		if !errors.As(refused, &refusal) || refusal.Code != providerkit.CodeInvalid {
			t.Errorf("%s() at a coordinate naming no class = %v, want an %s refusal: each class is sealed under a key of its own, so a classless value names no key",
				name, refused, providerkit.CodeInvalid)
		}
	}
}

func TestServesNothingUntilAResourcePrimitiveExists(t *testing.T) {
	t.Parallel()

	p := standing(t)
	if got := p.Serves(); len(got) != 0 {
		t.Errorf("Serves() = %v, want nothing until this provider provisions links of its own", got)
	}
	want := []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer}
	got := p.Computes()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Computes() = %v, want %v with serverless first, which makes it the default", got, want)
	}
}

func errorOf[T any](_ T, err error) error { return err }

func TestTheCredentialsPortNamesTheRolesEachTierIsGranted(t *testing.T) {
	t.Parallel()

	credentials := standing(t).Credentials()
	for tier, named := range map[providerkit.CredentialTier][]string{
		providerkit.TierDeploy: {
			"roles/run.admin",
			"roles/storage.objectAdmin",
			"roles/artifactregistry.writer",
			"roles/iam.serviceAccountUser",
		},
		providerkit.TierBootstrap: {
			"roles/run.admin",
			"roles/storage.admin",
			"roles/artifactregistry.admin",
			"roles/iam.serviceAccountAdmin",
			"roles/cloudkms.admin",
		},
	} {
		document, err := credentials.Permissions(tier)
		if err != nil {
			t.Errorf("Permissions(%s) = %v, want the roles that tier is granted", tier, err)
			continue
		}
		if document.Heading == "" {
			t.Errorf("Permissions(%s) returned a document under no heading, so nothing says what the run is looking at", tier)
		}
		for _, role := range named {
			if !strings.Contains(document.Document, role) {
				t.Errorf("Permissions(%s) does not name %s, and a credential granted what it renders would fail on the resources that role covers", tier, role)
			}
		}
	}
}

func TestTheRolesRenderedForADeployAreTheOnesADeployUses(t *testing.T) {
	t.Parallel()

	document, err := standing(t).Credentials().Permissions(providerkit.TierDeploy)
	if err != nil {
		t.Fatalf("Permissions(deploy) = %v", err)
	}
	for role, why := range map[string]string{
		"roles/run.developer": "roles/run.admin covers every permission it holds, so granting it says something the grant beside it did not",
		"roles/storage.admin": "a deploy reads and writes objects in buckets the bootstrap already made, and never makes or deletes one",
	} {
		if strings.Contains(document.Document, role) {
			t.Errorf("Permissions(deploy) names %s: %s", role, why)
		}
	}
}

func TestACredentialTierNobodyDefinedIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	var refusal providerkit.Refusal
	_, err := standing(t).Credentials().Permissions(providerkit.CredentialTier("root"))
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Permissions(root) = %v, want an %s refusal", err, providerkit.CodeInvalid)
	}
}
