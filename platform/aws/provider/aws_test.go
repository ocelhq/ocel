package aws

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var defaultNamespace = bootstrap.Namespace(provider.DefaultNamespace)

func TestStateBackendURLCarriesTheEndpointTheAccountIsReachedOn(t *testing.T) {
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_ENDPOINT_URL_S3", "")
	if got := stateBackendURL("state", "hello"); got != "s3://state/hello" {
		t.Fatalf("with no endpoint the backend is %q, want the bare bucket path", got)
	}

	t.Setenv("AWS_ENDPOINT_URL", "http://127.0.0.1:4566")
	got := stateBackendURL("state", "hello")
	want := "s3://state/hello?disableSSL=true&endpoint=127.0.0.1%3A4566&s3ForcePathStyle=true"
	if got != want {
		t.Fatalf("with AWS_ENDPOINT_URL set the backend is\n %q\nwant\n %q", got, want)
	}

	t.Setenv("AWS_ENDPOINT_URL_S3", "https://s3.example.test")
	got = stateBackendURL("state", "hello")
	want = "s3://state/hello?endpoint=s3.example.test&s3ForcePathStyle=true"
	if got != want {
		t.Fatalf("the S3-specific endpoint wins and https keeps TLS; got %q, want %q", got, want)
	}
}

type stubBootstrap struct{ err error }

func (stubBootstrap) Catalogue() []provider.Feature { return nil }

func (stubBootstrap) Describe(context.Context, edge.Class) (provider.BootstrapDescription, error) {
	return provider.BootstrapDescription{}, nil
}

func (s stubBootstrap) Plan(context.Context, provider.BootstrapRequest) (provider.Plan, error) {
	return provider.Plan{}, s.err
}

func (s stubBootstrap) Apply(context.Context, provider.BootstrapRequest, edge.Progress) error {
	return s.err
}

func (stubBootstrap) PlanRemove(context.Context, edge.Class) (provider.Plan, error) {
	return provider.Plan{}, nil
}

func (s stubBootstrap) Remove(context.Context, edge.Class, edge.Progress) error {
	return s.err
}

func forgetOf(t *testing.T, p *Provider) func() {
	t.Helper()
	boot, err := p.Bootstrap(edges.DefaultKind)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := boot.(forgetting)
	if !ok {
		t.Fatalf("Bootstrap() = %T, want one that clears the provider's memo", boot)
	}
	return wrapped.forget
}

func primed(t *testing.T, p *Provider, table string) {
	t.Helper()
	if _, err := p.deployed.resolve(edge.ClassProduction, func() (bootstrap.Deployed, error) {
		return bootstrap.Deployed{StateTable: table}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.params.resolve(classEdge{class: edge.ClassProduction, kind: edges.DefaultKind}, func() (bootstrap.ClassParams, error) {
		return bootstrap.ClassParams{Passphrase: table}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func resolvedAfter(t *testing.T, p *Provider) (string, string) {
	t.Helper()
	deployed, err := p.deployed.resolve(edge.ClassProduction, func() (bootstrap.Deployed, error) {
		return bootstrap.Deployed{StateTable: "after"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	params, err := p.params.resolve(classEdge{class: edge.ClassProduction, kind: edges.DefaultKind}, func() (bootstrap.ClassParams, error) {
		return bootstrap.ClassParams{Passphrase: "after"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployed.StateTable, params.Passphrase
}

func TestBootstrapApplyForgetsWhatItInstalled(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	primed(t, p, "before")

	if err := (forgetting{Bootstrap: stubBootstrap{}, forget: forgetOf(t, p)}).
		Apply(context.Background(), provider.BootstrapRequest{Class: edge.ClassProduction}, nil); err != nil {
		t.Fatal(err)
	}

	table, passphrase := resolvedAfter(t, p)
	if table != "after" || passphrase != "after" {
		t.Fatalf("after Apply the provider still remembers %q/%q, want the account read afresh", table, passphrase)
	}
}

func TestBootstrapRemoveForgetsWhatItTookDown(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	primed(t, p, "before")

	if err := (forgetting{Bootstrap: stubBootstrap{}, forget: forgetOf(t, p)}).
		Remove(context.Background(), edge.ClassProduction, nil); err != nil {
		t.Fatal(err)
	}

	table, passphrase := resolvedAfter(t, p)
	if table != "after" || passphrase != "after" {
		t.Fatalf("after Remove the provider still remembers %q/%q, want the account read afresh", table, passphrase)
	}
}

func TestBootstrapKeepsWhatAFailedApplyNeverChanged(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	primed(t, p, "before")

	refused := errors.New("refused")
	if err := (forgetting{Bootstrap: stubBootstrap{err: refused}, forget: forgetOf(t, p)}).
		Apply(context.Background(), provider.BootstrapRequest{Class: edge.ClassProduction}, nil); !errors.Is(err, refused) {
		t.Fatalf("Apply() = %v, want the refusal it was given", err)
	}

	table, passphrase := resolvedAfter(t, p)
	if table != "before" || passphrase != "before" {
		t.Fatalf("a failed Apply forgot %q/%q, want what the account still has", table, passphrase)
	}
}

func TestBootstrapFrontsTheEdgeItWasAsked(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	for _, kind := range edges.SupportedEdges() {
		boot, err := p.Bootstrap(kind)
		if err != nil {
			t.Fatalf("Bootstrap(%q) error = %v", kind, err)
		}
		awsBoot, ok := boot.(forgetting).Bootstrap.(control.Bootstrap)
		if !ok {
			t.Fatalf("Bootstrap(%q) = %T, want the AWS boot", kind, boot)
		}
		if awsBoot.Edge.Kind() != kind {
			t.Errorf("Bootstrap(%q) fronts the %q edge, want the one it was asked for", kind, awsBoot.Edge.Kind())
		}
	}
	if _, err := p.Bootstrap("nowhere"); err == nil {
		t.Error("Bootstrap() accepted an edge this provider cannot front with")
	}
}

func TestClassParamsReadTheEdgeTheyAreGiven(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	wanted := classEdge{class: edge.ClassProduction, kind: cloudflare.Kind}
	if _, err := p.params.resolve(wanted, func() (bootstrap.ClassParams, error) {
		return bootstrap.ClassParams{Passphrase: string(cloudflare.Kind)}, nil
	}); err != nil {
		t.Fatal(err)
	}

	for _, opened := range edges.SupportedEdges() {
		if _, err := p.Edges().Open(opened); err != nil {
			t.Fatalf("Open(%q) error = %v", opened, err)
		}
	}

	params, err := p.classParams(context.Background(), edge.ClassProduction, cloudflare.Kind)
	if err != nil {
		t.Fatalf("classParams error = %v", err)
	}
	if params.Passphrase != string(cloudflare.Kind) {
		t.Fatalf("classParams read the %q namespace after other edges were opened, want %q",
			params.Passphrase, cloudflare.Kind)
	}
}

func TestPreflightRefusesADeployOverAnUnreadableOriginSecret(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	refused := refusal.Refuse(refusal.CodeNotReady, "/ocel/origin/secret contains something other than the origin secret bootstrap writes")
	if _, err := p.params.resolve(classEdge{class: edge.ClassProduction, kind: cloudflare.Kind}, func() (bootstrap.ClassParams, error) {
		return bootstrap.ClassParams{OriginSecretErr: refused}, nil
	}); err != nil {
		t.Fatal(err)
	}

	pre := provider.DeployPreflight{
		Deploy: provider.DeploySpec{Class: edge.ClassProduction},
		Edge:   cloudflare.Kind,
	}
	if err := p.refuseUnreadableOriginSecret(context.Background(), pre); !errors.Is(err, refused) {
		t.Fatalf("preflight = %v, want the refusal: a deploy hands every release the secret the edge presents", err)
	}
}

func TestBucketsSweepTheCacheStoreOfEveryInstalledEdge(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	if _, err := p.deployed.resolve(edge.ClassProduction, func() (bootstrap.Deployed, error) {
		return bootstrap.Deployed{
			ArtifactBucket: "functions",
			AssetBucket:    "assets",
			Features: bootstrap.FeatureSet{
				bootstrap.FeatureCloudflareEdge: true,
				bootstrap.FeatureCloudFrontEdge: true,
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	for kind, store := range map[edge.Kind]bootstrap.CacheStore{
		cloudflare.Kind: {
			Bucket:          "cache-cloudflare",
			Endpoint:        "https://account.r2.cloudflarestorage.com",
			Region:          "auto",
			AccessKeyID:     "key",
			SecretAccessKey: "secret",
		},
		cloudfront.Kind: {Bucket: "cache-cloudfront"},
	} {
		if _, err := p.params.resolve(classEdge{class: edge.ClassProduction, kind: kind}, func() (bootstrap.ClassParams, error) {
			return bootstrap.ClassParams{CacheStore: store}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	buckets, err := p.Buckets(context.Background(), edge.ClassProduction)
	if err != nil {
		t.Fatalf("Buckets error = %v", err)
	}
	var got []string
	for _, cache := range buckets.Caches {
		got = append(got, cache.Name)
		reached := cache.S3 != nil
		if want := cache.Name == "cache-cloudflare"; reached != want {
			t.Errorf("cache %q has its own client = %v, want %v: only a store off this account's endpoint needs one",
				cache.Name, reached, want)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"cache-cloudflare", "cache-cloudfront"}) {
		t.Fatalf("Buckets() returns caches %v, want one for each edge installed in the account", got)
	}
}

func TestBootstrapReadyWithoutAVarsKey(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	deployed := bootstrap.Deployed{
		StateBucket:    "state",
		ArtifactBucket: "artifacts",
		AssetBucket:    "assets",
		StateTable:     "state-table",
		VarsTable:      "vars-table",
	}

	if err := p.requireBootstrapped(deployed, edge.ClassProduction); err != nil {
		t.Errorf("requireBootstrapped() = %v, want a bootstrap with no key ready: a release with no sealed value needs none", err)
	}
}

type callerIdentity struct{}

func (callerIdentity) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
		Arn:     aws.String("arn:aws:iam::123456789012:user/deployer"),
	}, nil
}

func TestTheIdentityNamesTheVendorTheProviderNamesItself(t *testing.T) {
	t.Parallel()

	principal, err := control.Credentials{STS: callerIdentity{}, Region: "us-east-1"}.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v", err)
	}
	named := providerserver.PrincipalProto(Vendor, principal).GetProvider()
	if named != string(Vendor) {
		t.Errorf("the identity names %q and the provider names itself %q; the CLI matches a credential problem to its section by that string, so a mismatch loses the problem", named, Vendor)
	}
}

func TestAVarsKeyThatNamesNoKMSKeyIsRefused(t *testing.T) {
	if _, err := provider.Decode[Options](Vendor, provider.Options{"varsKey": "arn:aws:kms:eu-west-1:111122223333:key/abcd"}); err != nil {
		t.Fatalf("a kms key arn was refused: %v", err)
	}
	_, err := provider.Decode[Options](Vendor, provider.Options{"varsKey": "arn:aws:s3:::a-bucket"})
	if err == nil {
		t.Fatal("a varsKey that names no kms key was taken")
	}
	if !strings.Contains(err.Error(), "varsKey") {
		t.Errorf("error = %q, want it to name the option", err)
	}
}
