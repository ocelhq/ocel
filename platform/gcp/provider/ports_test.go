package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func testProvider(t *testing.T) *gcp.Provider {
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
	derived, err := p.Names(context.Background())
	if err != nil {
		t.Fatalf("Names() = %v", err)
	}
	return derived
}

func TestTheAlbEdgeIsRegisteredAndOpensWithTheProvidersOwnPorts(t *testing.T) {
	t.Parallel()

	p := testProvider(t)
	registry := p.Edges()
	if got := p.Facts().Edges; !slices.Contains(got, alb.Kind) {
		t.Fatalf("Facts().Edges = %v, want the %q edge among them: a config that names it would be refused", got, alb.Kind)
	}
	front, err := registry.Open(alb.Kind, nil)
	if err != nil {
		t.Fatalf("Open(%q) = %v", alb.Kind, err)
	}
	if front.Kind() != alb.Kind {
		t.Errorf("Open(%q) opened the %q edge", alb.Kind, front.Kind())
	}
	if !front.Facts().InvalidatesByCacheTag {
		t.Error("Facts() says the alb edge does not invalidate by cache tag, and Cloud CDN invalidates by Cache-Tag")
	}
	routes, err := p.Routers().Open(router.Kind(alb.Kind))
	if err != nil {
		t.Fatalf("Routers().Open(%q) = %v", alb.Kind, err)
	}
	if routes.Facts().AddressesItself {
		t.Error("Facts() says the alb router addresses itself, and a deploy would then never be asked for the hostname it fronts")
	}
	if !routes.Facts().RoutesPreviewsByLabel {
		t.Error("Facts() says the alb edge does not route previews by label, and the url mask on its preview neg hands Cloud Run " +
			"the hostname's first label as the service name, so a deploy that is not named it answers nothing")
	}
	if routes.Facts().ServesPreviewDeployments {
		t.Error("Facts() says the alb edge serves each preview deployment on its own hostname, and the url mask maps a label to " +
			"the one Cloud Run service of that name, which a deployment's own label never names")
	}
	for _, need := range []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming} {
		if !edge.Supports(front, need) {
			t.Errorf("Supported() does not name %q, and the load balancer with Cloud CDN in front of Cloud Run serves it", need)
		}
	}
}

func TestAnEdgeThisProviderCannotFrontWithIsRefusedWithThePriceOfTheOneThatCan(t *testing.T) {
	t.Parallel()

	var refused refusal.Refusal
	_, err := testProvider(t).Edges().Open(edge.Kind("firebase"), nil)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Open(firebase) = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	for _, said := range []string{string(alb.Kind), "$18"} {
		if !strings.Contains(refused.Message, said) {
			t.Errorf("the refusal reads %q, want it to name %q so the reader knows what to name instead and what it costs", refused.Message, said)
		}
	}
}

func TestCloudflareFrontsTheLoadBalancerAsAProxyThatRunsNoCode(t *testing.T) {
	t.Parallel()

	p := testProvider(t)
	if !slices.Contains(p.Facts().Edges, cloudflare.Kind) {
		t.Errorf("Facts().Edges = %v, want cloudflare among them", p.Facts().Edges)
	}
	if _, err := p.Bootstrap(cloudflare.Kind); err != nil {
		t.Fatalf("Bootstrap(%q) = %v, want the bootstrap that raises the load balancer it forwards to", cloudflare.Kind, err)
	}
	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatalf("Open(%q) = %v", cloudflare.Kind, err)
	}
	facts := front.Facts()
	if facts.RunsCode || !facts.ProxiesRecords || facts.ServesUnbound {
		t.Errorf("the cloudflare edge's facts = %+v, want a proxy that forwards records to an origin and runs no worker", facts)
	}
	if !facts.ShieldsOrigin {
		t.Errorf("the cloudflare edge's facts = %+v, want the origin shielded, so a Cloud Run service answers only its load balancer", facts)
	}
	hooks := front.Hooks()
	if hooks.ClientCertificates == nil || hooks.CheckBootstrapInstalled == nil || hooks.ListBoundHostnames == nil {
		t.Error("the cloudflare edge presents no client certificate, or says nothing of the load balancer its bootstrap raises")
	}
	if got := p.Facts().ListPairedRouters(cloudflare.Kind); !slices.Equal(got, []router.Kind{router.Kind(alb.Kind)}) {
		t.Errorf("ListPairedRouters(cloudflare) = %v, want the load balancer alone", got)
	}
}

func TestAFrontedServiceStopsAnsweringOnItsOwnCloudRunUrl(t *testing.T) {
	t.Parallel()

	front, err := testProvider(t).Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatalf("Open(%q) = %v", alb.Kind, err)
	}
	unfronted, err := testProvider(t).Edges().Open(edge.None, nil)
	if err != nil {
		t.Fatalf("Open(no edge) = %v", err)
	}
	if !front.Facts().ShieldsOrigin {
		t.Errorf("the %q edge does not declare that it shields the origin, and the ingress a release takes is read from that fact "+
			"rather than from which edge it is", alb.Kind)
	}
	if unfronted.Facts().ShieldsOrigin {
		t.Error("a project with no edge in front declares that it shields the origin, and the url Cloud Run gives each service is the whole of what serves it")
	}
	if got := gcp.IngressFor(edge.Facts{ShieldsOrigin: true}); got != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" {
		t.Errorf("a service behind an edge that shields the origin takes ingress %q, want the load balancer only: the run.app url would "+
			"otherwise answer past the front's certificate, cache and host rules", got)
	}
	if got := gcp.IngressFor(edge.Facts{}); got != "INGRESS_TRAFFIC_ALL" {
		t.Errorf("a service under an edge that shields nothing takes ingress %q, want every caller", got)
	}
}

func TestNoPortIsNilForProviderserverToCallThrough(t *testing.T) {
	t.Parallel()

	p := testProvider(t)
	for name, port := range map[string]any{
		"Stacks":      p.Stacks(),
		"Artifacts":   p.Artifacts(),
		"Records":     p.KeyValues(),
		"Cipher":      p.Cipher(),
		"Credentials": p.Credentials(),
		"Edges":       p.Edges(),
		"DNS":         p.DNS(),
	} {
		if port == nil {
			t.Errorf("%s() is nil, and providerserver calls methods on it", name)
		}
	}
}

func TestAnArtifactThatNamesNoTierOrNoStoreIsTheCallersMistake(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := testProvider(t)
	tierless := provider.ArtifactRef{Bucket: provider.StoreFunctions, Key: "bundle.zip"}
	storeless := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: "somewhere-else", Key: "bundle.zip"}

	for name, refused := range map[string]error{
		"Put with no tier":   p.Artifacts().Put(ctx, tierless, bytes.NewReader(nil)),
		"Has with no tier":   errorOf(p.Artifacts().Has(ctx, tierless)),
		"Open with no tier":  errorOf(p.Artifacts().Open(ctx, tierless)),
		"Put with no store":  p.Artifacts().Put(ctx, storeless, bytes.NewReader(nil)),
		"Has with no store":  errorOf(p.Artifacts().Has(ctx, storeless)),
		"Open with no store": errorOf(p.Artifacts().Open(ctx, storeless)),
	} {
		var rejection refusal.Refusal
		if !errors.As(refused, &rejection) || rejection.Code != refusal.CodeInvalid {
			t.Errorf("%s = %v, want an %s refusal", name, refused, refusal.CodeInvalid)
		}
	}
}

func TestSealingAValueThatNamesNoTierIsTheCallersMistake(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := testProvider(t)

	for name, refused := range map[string]error{
		"Seal": errorOf(p.Cipher().Seal(ctx, "", seal.AssociatedData{{Name: "key", Value: "K"}}, nil)),
		"Open": errorOf(p.Cipher().Open(ctx, "", seal.AssociatedData{{Name: "key", Value: "K"}}, nil)),
	} {
		var rejection refusal.Refusal
		if !errors.As(refused, &rejection) || rejection.Code != refusal.CodeInvalid {
			t.Errorf("%s() under no tier = %v, want an %s refusal: each tier is sealed under a key of its own, so a value naming no tier names no key",
				name, refused, refusal.CodeInvalid)
		}
	}
}

func TestServesKVStoresTopicsTasksAndRealtimeAmongTheResourcePrimitives(t *testing.T) {
	t.Parallel()

	p := testProvider(t)
	if got, want := p.Facts().Bindings, []provider.BindingType{provider.BindingKV, provider.BindingTopic, provider.BindingTask, provider.BindingRealtime}; !slices.Equal(got, want) {
		t.Errorf("Facts().Bindings = %v, want %v: the resource primitives this provider provisions", got, want)
	}
	want := []provider.Compute{provider.ComputeServerless, provider.ComputeContainer}
	got := p.Facts().Computes
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Computes() = %v, want %v with serverless first, which makes it the default", got, want)
	}
}

func errorOf[T any](_ T, err error) error { return err }

func TestTheCredentialsPortNamesTheRolesEachPurposeIsGranted(t *testing.T) {
	t.Parallel()

	credentials := testProvider(t).Credentials()
	for purpose, named := range map[edge.CredentialPurpose][]string{
		edge.PurposeDeploy: {
			"roles/run.admin",
			"roles/storage.objectAdmin",
			"roles/artifactregistry.writer",
			"roles/iam.serviceAccountUser",
		},
		edge.PurposeBootstrap: {
			"roles/run.admin",
			"roles/storage.admin",
			"roles/artifactregistry.admin",
			"roles/iam.serviceAccountAdmin",
			"roles/cloudkms.admin",
		},
	} {
		document, err := credentials.Permissions(purpose)
		if err != nil {
			t.Errorf("Permissions(%s) = %v, want the roles that purpose is granted", purpose, err)
			continue
		}
		if document.Heading == "" {
			t.Errorf("Permissions(%s) returned a document under no heading, so nothing says what the run is looking at", purpose)
		}
		for _, role := range named {
			if !strings.Contains(document.Document, role) {
				t.Errorf("Permissions(%s) does not name %s, and a credential granted what it renders would fail on the resources that role covers", purpose, role)
			}
		}
	}
}

func TestTheRolesRenderedForADeployAreTheOnesADeployUses(t *testing.T) {
	t.Parallel()

	document, err := testProvider(t).Credentials().Permissions(edge.PurposeDeploy)
	if err != nil {
		t.Fatalf("Permissions(deploy) = %v", err)
	}
	for role, why := range map[string]string{
		"roles/run.developer": "roles/run.admin covers every permission it grants, so granting it says something the grant beside it did not",
		"roles/storage.admin": "a deploy reads and writes objects in buckets the bootstrap already made, and never makes or deletes one",
	} {
		if strings.Contains(document.Document, role) {
			t.Errorf("Permissions(deploy) names %s: %s", role, why)
		}
	}
}

func TestADeployMayKeepTheRealtimeSecretsOfItsNamespaceAndAdministersNoOtherSecret(t *testing.T) {
	t.Parallel()

	document, err := testProvider(t).Credentials().Permissions(edge.PurposeDeploy)
	if err != nil {
		t.Fatalf("Permissions(deploy) = %v", err)
	}
	if strings.Contains(document.Document, "roles/secretmanager.admin") {
		t.Errorf("Permissions(deploy) names roles/secretmanager.admin, which reads, rewrites and deletes every secret in the project and changes who may read each:\n%s", document.Document)
	}
	const realtime = `roles/secretmanager.editor, on the condition ` +
		`(resource.type != "secretmanager.googleapis.com/Secret" && resource.type != "secretmanager.googleapis.com/SecretVersion") || ` +
		`resource.name.startsWith("projects/PROJECT_NUMBER/secrets/ocel_realtime-")`
	if !strings.Contains(document.Document, realtime) {
		t.Errorf("Permissions(deploy) =\n%s\nwant the line %s: a deploy creates, writes and deletes the realtime secrets of its namespace and no others", document.Document, realtime)
	}
	if !strings.Contains(document.Document, "roles/secretmanager.secretAccessor") {
		t.Errorf("Permissions(deploy) does not name roles/secretmanager.secretAccessor, and a deploy reads the passphrase its edge's state is sealed with")
	}
}

func TestADeployMayCreateAccountsAndGrantThemOnlyTheRolesAnAppRuns(t *testing.T) {
	t.Parallel()

	document, err := testProvider(t).Credentials().Permissions(edge.PurposeDeploy)
	if err != nil {
		t.Fatalf("Permissions(deploy) = %v", err)
	}
	lines := strings.Split(document.Document, "\n")
	for _, want := range []string{
		"projects/acme-prod/roles/ocel_app_accounts",
		"roles/resourcemanager.projectIamAdmin, on the condition api.getAttribute('iam.googleapis.com/modifiedGrantsByRole', [])." +
			"hasOnly(['roles/datastore.viewer', 'roles/datastore.user', 'roles/cloudkms.cryptoKeyDecrypter', 'roles/storage.objectUser', 'projects/acme-prod/roles/ocel_cdn_purge'])",
		"roles/cloudtasks.queueAdmin on the queue projects/acme-prod/locations/europe-west1/queues/ocel-production-delays",
		"roles/cloudtasks.queueAdmin on the queue projects/acme-prod/locations/europe-west1/queues/ocel-preview-delays",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("Permissions(deploy) =\n%s\nwant the line %s", document.Document, want)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "on the service account") || strings.HasPrefix(line, "roles/iam.serviceAccount") && line != "roles/iam.serviceAccountUser" {
			t.Errorf("Permissions(deploy) names %q, and a deploy may only create accounts and set who acts as them through the custom role:\n%s", line, document.Document)
		}
	}
	if slices.Contains(lines, "roles/resourcemanager.projectIamAdmin") {
		t.Errorf("Permissions(deploy) names roles/resourcemanager.projectIamAdmin with no condition, and a deploy could then grant any role to anyone:\n%s", document.Document)
	}
}

func TestADeployMayGrantTheCachePurgeRoleAndNoRoleItCouldChange(t *testing.T) {
	t.Parallel()

	document, err := testProvider(t).Credentials().Permissions(edge.PurposeDeploy)
	if err != nil {
		t.Fatalf("Permissions(deploy) = %v", err)
	}
	if !strings.Contains(document.Document, "'projects/acme-prod/roles/ocel_cdn_purge'") {
		t.Errorf("Permissions(deploy) =\n%s\nwant the cache purge role among the roles it may grant", document.Document)
	}
	for _, line := range strings.Split(document.Document, "\n") {
		if line == "roles/iam.roleAdmin" || line == "roles/iam.organizationRoleAdmin" || strings.Contains(line, "iam.roles.") {
			t.Errorf("Permissions(deploy) names %q, and a deploy that could change a custom role could widen what the grant it holds hands out:\n%s", line, document.Document)
		}
	}
}

func TestACredentialPurposeNobodyDefinedIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	var refused refusal.Refusal
	_, err := testProvider(t).Credentials().Permissions(edge.CredentialPurpose("root"))
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Permissions(root) = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
}
