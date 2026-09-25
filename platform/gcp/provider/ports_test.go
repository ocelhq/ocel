package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func standing(t *testing.T) *gcp.Provider {
	t.Helper()
	return newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
}

func newProvider(t *testing.T, options gcp.Options) *gcp.Provider {
	t.Helper()
	p, err := gcp.NewProvider(options)
	if err != nil {
		t.Fatalf("NewProvider(%+v) = %v", options, err)
	}
	return p
}

func names(t *testing.T, p *gcp.Provider) gcp.Names {
	t.Helper()
	held, err := p.Names(context.Background())
	if err != nil {
		t.Fatalf("Names() = %v", err)
	}
	return held
}

func TestTheAlbEdgeIsRegisteredAndOpensWithTheProvidersOwnPorts(t *testing.T) {
	t.Parallel()

	p := standing(t)
	registry := p.Edges()
	if got := p.Facts().Edges; !slices.Contains(got, alb.Kind) {
		t.Fatalf("Facts().Edges = %v, want the %q edge among them: a config that names it would be refused", got, alb.Kind)
	}
	front, err := registry.Open(alb.Kind)
	if err != nil {
		t.Fatalf("Open(%q) = %v", alb.Kind, err)
	}
	if front.Kind() != alb.Kind {
		t.Errorf("Open(%q) opened the %q edge", alb.Kind, front.Kind())
	}
	if !front.Facts().InvalidatesByCacheTag {
		t.Error("Facts() says the alb edge does not invalidate by cache tag, and Cloud CDN invalidates by Cache-Tag")
	}
	if front.Facts().AddressesItself {
		t.Error("Facts() says the alb edge addresses itself, and a deploy would then never be asked for the hostname it fronts")
	}
	if !front.Facts().RoutesPreviewsByLabel {
		t.Error("Facts() says the alb edge does not route previews by label, and the url mask on its preview neg hands Cloud Run " +
			"the hostname's first label as the service name, so a deploy that is not named it answers nothing")
	}
	for _, need := range []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming} {
		if !edge.Supports(front, need) {
			t.Errorf("Supported() does not name %q, and the load balancer with Cloud CDN in front of Cloud Run serves it", need)
		}
	}
}

func TestAnEdgeThisProviderCannotFrontWithIsRefusedWithThePriceOfTheOneThatCan(t *testing.T) {
	t.Parallel()

	var refusal providerkit.Refusal
	_, err := standing(t).Edges().Open(edge.Kind("firebase"))
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Open(firebase) = %v, want an %s refusal", err, providerkit.CodeInvalid)
	}
	for _, said := range []string{string(alb.Kind), "$18"} {
		if !strings.Contains(refusal.Message, said) {
			t.Errorf("the refusal reads %q, want it to name %q so the reader knows what to name instead and what it costs", refusal.Message, said)
		}
	}
}

func TestNamingTheCloudflareEdgeIsRefusedWhenTheBootstrapIsOpenedAndSaysWhy(t *testing.T) {
	t.Parallel()

	p := standing(t)
	if slices.Contains(p.Facts().Edges, cloudflare.Kind) {
		t.Errorf("Facts().Edges = %v, and an edge this provider builds no program for is one every deploy through it is refused on", p.Facts().Edges)
	}
	var refusal providerkit.Refusal
	_, err := p.Bootstrap(cloudflare.Kind)
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Bootstrap(%q) = %v, want an %s refusal before a token is spent standing anything up", cloudflare.Kind, err, providerkit.CodeInvalid)
	}
	for _, said := range []string{"program", string(direct.Kind), string(alb.Kind)} {
		if !strings.Contains(refusal.Message, said) {
			t.Errorf("the refusal reads %q, want it to say %q: the reader learns why it is refused and what to name instead", refusal.Message, said)
		}
	}
	if _, err := p.Edges().Open(cloudflare.Kind); !errors.As(err, &refusal) {
		t.Errorf("Open(%q) = %v, want the same refusal on the deploy path", cloudflare.Kind, err)
	}
}

func TestAFrontedServiceStopsAnsweringOnItsOwnCloudRunUrl(t *testing.T) {
	t.Parallel()

	front, err := standing(t).Edges().Open(alb.Kind)
	if err != nil {
		t.Fatalf("Open(%q) = %v", alb.Kind, err)
	}
	direct, err := standing(t).Edges().Open(direct.Kind)
	if err != nil {
		t.Fatalf("Open(direct) = %v", err)
	}
	if !front.Facts().ShieldsOrigin {
		t.Errorf("the %q edge does not declare that it shields the origin, and the ingress a release takes is read from that fact "+
			"rather than from which edge it is", alb.Kind)
	}
	if direct.Facts().ShieldsOrigin {
		t.Error("the direct edge declares that it shields the origin, and the url Cloud Run gives each service is the whole of what serves it")
	}
	if got := gcp.IngressFor(edge.Facts{ShieldsOrigin: true}); got != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" {
		t.Errorf("a service behind an edge that shields the origin takes ingress %q, want the load balancer only: the run.app url would "+
			"otherwise answer past the front's certificate, cache and host rules", got)
	}
	if got := gcp.IngressFor(edge.Facts{}); got != "INGRESS_TRAFFIC_ALL" {
		t.Errorf("a service under an edge that shields nothing takes ingress %q, want every caller", got)
	}
}

func TestNoPortIsNilForTheKitToCallThrough(t *testing.T) {
	t.Parallel()

	p := standing(t)
	for name, port := range map[string]any{
		"Stacks":      p.Stacks(),
		"Artifacts":   p.Artifacts(),
		"Records":     p.Records(),
		"Cipher":      p.Cipher(),
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
		"Seal": errorOf(p.Cipher().Seal(ctx, providerkit.SealScope{}, nil)),
		"Open": errorOf(p.Cipher().Open(ctx, providerkit.SealScope{}, nil)),
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
	if got := p.Facts().Bindings; len(got) != 0 {
		t.Errorf("Serves() = %v, want nothing until this provider provisions bindings of its own", got)
	}
	want := []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer}
	got := p.Facts().Computes
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
