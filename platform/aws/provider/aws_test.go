package aws

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

var defaultNamespace = account.Namespace(provider.DefaultNamespace)

func TestStateBackendURLIncludesTheEndpointTheAccountIsReachedOn(t *testing.T) {
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

func (stubBootstrap) Parts() []provider.Part { return nil }

func (stubBootstrap) Describe(context.Context, environment.Tier) (provider.BootstrapDescription, error) {
	return provider.BootstrapDescription{}, nil
}

func (s stubBootstrap) Plan(context.Context, provider.BootstrapRequest) (provider.Plan, error) {
	return provider.Plan{}, s.err
}

func (s stubBootstrap) Apply(context.Context, provider.BootstrapRequest, progress.Log) error {
	return s.err
}

func (stubBootstrap) PlanRemove(context.Context, environment.Tier) (provider.Plan, error) {
	return provider.Plan{}, nil
}

func (s stubBootstrap) Remove(context.Context, environment.Tier, progress.Log) error {
	return s.err
}

func (stubBootstrap) DescribeParts(context.Context, provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	return nil, nil
}

func unkeyed(context.Context, environment.Tier) (string, error) { return "", nil }

func keyedAs(keys ...string) func(context.Context, environment.Tier) (string, error) {
	return func(context.Context, environment.Tier) (string, error) {
		next := keys[0]
		keys = keys[1:]
		return next, nil
	}
}

func digestKeyAfter(t *testing.T, req provider.BootstrapRequest, keys ...string) error {
	t.Helper()
	ctx := context.Background()
	store := variablestore.Store{KeyValues: fake.NewKeyValues(), Cipher: fake.NewCipher()}
	if _, err := envsource.EnsureDigestKey(ctx, store, req.Tier); err != nil {
		t.Fatal(err)
	}
	if err := (tierBootstrap{Bootstrap: stubBootstrap{}, forget: func() {}, key: keyedAs(keys...), keyValues: store.KeyValues}).
		Apply(ctx, req, nil); err != nil {
		t.Fatal(err)
	}
	_, err := store.KeyValues.Read(ctx, records.Partition{Tier: req.Tier, Root: envsource.RootDigestKey}.Key("digestkey"))
	return err
}

func TestAnApplyThatTakesTheVariablesKeyAwayForgetsTheDigestKeySealedUnderIt(t *testing.T) {
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{provider.VariablesKeyPart}}
	if err := digestKeyAfter(t, req, "arn:aws:kms:us-east-1:111122223333:key/one", ""); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("the digest key after the variables key was removed reads %v, want it forgotten: nothing opens it once its key is gone, and a sync never replaces a key it cannot open", err)
	}
}

func TestAnApplyThatBringsAnotherVariablesKeyForgetsTheDigestKeySealedUnderTheOldOne(t *testing.T) {
	req := provider.BootstrapRequest{Tier: environment.TierPreview}
	if err := digestKeyAfter(t, req, "arn:aws:kms:us-east-1:111122223333:key/one", "arn:aws:kms:us-east-1:111122223333:key/two"); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("the digest key after the variables key changed reads %v, want it forgotten", err)
	}
}

func TestAnApplyThatKeepsTheVariablesKeyKeepsTheDigestKey(t *testing.T) {
	req := provider.BootstrapRequest{Tier: environment.TierProduction}
	if err := digestKeyAfter(t, req, "arn:aws:kms:us-east-1:111122223333:key/one", "arn:aws:kms:us-east-1:111122223333:key/one"); err != nil {
		t.Fatalf("the digest key after an apply that kept the variables key reads %v, want it kept", err)
	}
}

func forgetOf(t *testing.T, p *Provider) func() {
	t.Helper()
	boot, err := p.Bootstrap(edges.DefaultKind)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := boot.(tierBootstrap)
	if !ok {
		t.Fatalf("Bootstrap() = %T, want one that clears the provider's memo", boot)
	}
	return wrapped.forget
}

func primed(t *testing.T, p *Provider, table string) {
	t.Helper()
	if _, err := p.deployed.resolve(environment.TierProduction, func() (account.Reading, error) {
		return account.Reading{Deployed: account.Deployed{StateTable: table}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.params.resolve(tierEdge{tier: environment.TierProduction, kind: edges.DefaultKind}, func() (account.TierParams, error) {
		return account.TierParams{Passphrase: table}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func resolvedAfter(t *testing.T, p *Provider) (string, string) {
	t.Helper()
	deployed, err := p.deployed.resolve(environment.TierProduction, func() (account.Reading, error) {
		return account.Reading{Deployed: account.Deployed{StateTable: "after"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	params, err := p.params.resolve(tierEdge{tier: environment.TierProduction, kind: edges.DefaultKind}, func() (account.TierParams, error) {
		return account.TierParams{Passphrase: "after"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployed.Deployed.StateTable, params.Passphrase
}

func TestBootstrapApplyForgetsWhatItInstalled(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	primed(t, p, "before")

	if err := (tierBootstrap{Bootstrap: stubBootstrap{}, forget: forgetOf(t, p), key: unkeyed}).
		Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction}, nil); err != nil {
		t.Fatal(err)
	}

	table, passphrase := resolvedAfter(t, p)
	if table != "after" || passphrase != "after" {
		t.Fatalf("after Apply the provider still remembers %q/%q, want the account read afresh", table, passphrase)
	}
}

type repairedParts struct {
	tiers []environment.Tier
	names []string
}

type saying struct {
	progress.Log
	said []string
}

func (s *saying) Say(message string) { s.said = append(s.said, message) }

func (r *repairedParts) parts() tier.Parts {
	parts := make(tier.Parts, 0, len(r.names))
	for _, name := range r.names {
		parts = append(parts, tier.Part{Name: name, Repair: func(_ context.Context, tier environment.Tier) (bool, error) {
			r.tiers = append(r.tiers, tier)
			return true, nil
		}})
	}
	return parts
}

func TestBootstrapApplyRemakesTheRecordedPartsOfItsTierOnceTheBaseIsApplied(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	repaired := &repairedParts{names: []string{"env-source-sync"}}

	log := &saying{Log: progress.Discard()}
	if err := (tierBootstrap{Bootstrap: stubBootstrap{}, forget: forgetOf(t, p), key: unkeyed, parts: repaired.parts()}).
		Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierPreview}, log); err != nil {
		t.Fatal(err)
	}

	if len(repaired.tiers) != 1 || repaired.tiers[0] != environment.TierPreview {
		t.Errorf("Apply() repaired the parts of %v, want the preview tier's once", repaired.tiers)
	}
	if !strings.Contains(strings.Join(log.said, "\n"), "env-source-sync") {
		t.Errorf("Apply() said %q, want it to name the part it made again", log.said)
	}
}

func TestAFailedBootstrapApplyRemakesNoPart(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	repaired := &repairedParts{names: []string{"env-source-sync"}}

	refused := errors.New("refused")
	if err := (tierBootstrap{Bootstrap: stubBootstrap{err: refused}, forget: forgetOf(t, p), key: unkeyed, parts: repaired.parts()}).
		Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierPreview}, nil); !errors.Is(err, refused) {
		t.Fatalf("Apply() = %v, want the refusal it was given", err)
	}

	if len(repaired.tiers) != 0 {
		t.Errorf("a failed Apply repaired the parts of %v, want none", repaired.tiers)
	}
}

func TestBootstrapRemoveForgetsWhatItTookDown(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	primed(t, p, "before")

	if err := (tierBootstrap{Bootstrap: stubBootstrap{}, forget: forgetOf(t, p), key: unkeyed}).
		Remove(context.Background(), environment.TierProduction, nil); err != nil {
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
	if err := (tierBootstrap{Bootstrap: stubBootstrap{err: refused}, forget: forgetOf(t, p), key: unkeyed}).
		Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction}, nil); !errors.Is(err, refused) {
		t.Fatalf("Apply() = %v, want the refusal it was given", err)
	}

	table, passphrase := resolvedAfter(t, p)
	if table != "before" || passphrase != "before" {
		t.Fatalf("a failed Apply forgot %q/%q, want what the account still has", table, passphrase)
	}
}

func TestDescribingTheBootstrapReadsTheAccountTheProviderAlreadyRead(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	if _, err := p.deployed.resolve(environment.TierProduction, func() (account.Reading, error) {
		return account.Reading{Deployed: account.Deployed{Present: true, Parts: account.PartSet{}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	boot, err := p.Bootstrap(edges.DefaultKind)
	if err != nil {
		t.Fatal(err)
	}

	described, err := boot.Describe(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Describe() error = %v, want the bootstrap this provider already read, with no call to the account", err)
	}
	if !described.Present {
		t.Error("Describe() reads no bootstrap, want the one this provider already read")
	}
}

func TestTheProviderOpensEachEdgeOnceForEveryCallThatAsksForTheSameOptions(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	open := func(options provider.Options) edge.Edge {
		t.Helper()
		front, err := p.Edges().Open(cloudflare.Kind, options)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		return front
	}

	if first, again := open(nil), open(provider.Options{}); first != again {
		t.Error("two opens of the cloudflare edge with no options built two edges, want the zones it read shared between them")
	}
	if plain, tunnelled := open(nil), open(provider.Options{"tunnel": true}); plain == tunnelled {
		t.Error("an open with other options got the edge opened without them, want one built for its own options")
	}
}

func TestBootstrapFrontsTheEdgeItWasAsked(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	for _, kind := range edges.SupportedEdges() {
		boot, err := p.Bootstrap(kind)
		if err != nil {
			t.Fatalf("Bootstrap(%q) error = %v", kind, err)
		}
		awsBoot, ok := boot.(tierBootstrap).Bootstrap.(control.Bootstrap)
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

func TestTierParamsReadTheEdgeTheyAreGiven(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	wanted := tierEdge{tier: environment.TierProduction, kind: cloudflare.Kind}
	if _, err := p.params.resolve(wanted, func() (account.TierParams, error) {
		return account.TierParams{Passphrase: string(cloudflare.Kind)}, nil
	}); err != nil {
		t.Fatal(err)
	}

	for _, opened := range edges.SupportedEdges() {
		if _, err := p.Edges().Open(opened, nil); err != nil {
			t.Fatalf("Open(%q) error = %v", opened, err)
		}
	}

	params, err := p.tierParams(context.Background(), environment.TierProduction, cloudflare.Kind)
	if err != nil {
		t.Fatalf("tierParams error = %v", err)
	}
	if params.Passphrase != string(cloudflare.Kind) {
		t.Fatalf("tierParams read the %q namespace after other edges were opened, want %q",
			params.Passphrase, cloudflare.Kind)
	}
}

func TestPreflightRefusesADeployOverAnUnreadableOriginSecret(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	refused := refusal.Refuse(refusal.CodeNotReady, "/ocel/origin/secret contains something other than the origin secret bootstrap writes")
	if _, err := p.params.resolve(tierEdge{tier: environment.TierProduction, kind: cloudflare.Kind}, func() (account.TierParams, error) {
		return account.TierParams{OriginSecretErr: refused}, nil
	}); err != nil {
		t.Fatal(err)
	}

	pre := provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction},
		Edge:   cloudflare.Kind,
	}
	if err := p.refuseUnreadableOriginSecret(context.Background(), pre); !errors.Is(err, refused) {
		t.Fatalf("preflight = %v, want the refusal: a deploy hands every release the secret the edge presents", err)
	}
}

func TestTheALBKeepsItsTrustRecordInTheStateBucketWhichExpiresNoCurrentObject(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	if _, err := p.deployed.resolve(environment.TierProduction, func() (account.Reading, error) {
		return account.Reading{Deployed: account.Deployed{Present: true, StateBucket: "state", ArtifactBucket: "artifacts"}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	bucket, err := p.edgeDeps().StateBucket(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("StateBucket error = %v", err)
	}
	if bucket != "state" {
		t.Errorf("the ALB keeps its trust record and leases in %q, want the state bucket: the artifact bucket expires every object after 30 days, and a trust record read back as absent drops every CA the balancer trusts", bucket)
	}
}

func TestBucketsAreTheTiersArtifactEdgeAndCacheBucketsWhateverEdgesAreInstalled(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	if _, err := p.deployed.resolve(environment.TierProduction, func() (account.Reading, error) {
		return account.Reading{Deployed: account.Deployed{
			ArtifactBucket: "functions",
			EdgeBucket:     "edge",
			CacheBucket:    "cache",
			Parts: account.PartSet{
				account.CloudflareEdgePart: true,
			},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	buckets, err := p.Buckets(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Buckets error = %v", err)
	}
	if want := (awsports.Buckets{Artifacts: "functions", Edge: "edge", Cache: "cache"}); buckets != want {
		t.Fatalf("Buckets() = %+v, want %+v: the ISR entries an edge reads live in the edge bucket, and no edge keeps a store of its own", buckets, want)
	}
}

func TestAReleaseIntoATierWithNoBootstrapSaysSoRatherThanThatPartOfItIsMissing(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)

	err := p.requireBootstrapped(account.Deployed{}, environment.TierPreview)

	if err == nil || !strings.Contains(err.Error(), "the preview tier has no Ocel bootstrap") || strings.Contains(err.Error(), "partial rollback") {
		t.Errorf("requireBootstrapped() of a tier with no bootstrap = %v, want it to say the tier has none", err)
	}
}

func TestBootstrapReadyWithoutAVariablesKey(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	deployed := account.Deployed{
		Present:        true,
		StateBucket:    "state",
		ArtifactBucket: "artifacts",
		EdgeBucket:     "edge",
		CacheBucket:    "cache",
		StateTable:     "state-table",
		VariablesTable: "variables-table",
	}

	if err := p.requireBootstrapped(deployed, environment.TierProduction); err != nil {
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

type countedCallerIdentity struct {
	callerIdentity
	calls int
}

func (c *countedCallerIdentity) GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	c.calls++
	return c.callerIdentity.GetCallerIdentity(ctx, in, optFns...)
}

func TestTheAccountIdAfterAWhoamiAsksSTSNothingMore(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{Region: "us-east-1"}, defaultNamespace)
	counted := &countedCallerIdentity{}
	p.sts = counted

	principal, err := p.Credentials().Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v", err)
	}
	account, err := p.accountID(context.Background())
	if err != nil {
		t.Fatalf("accountID() = %v", err)
	}

	if account != principal.Account {
		t.Errorf("accountID() = %q, want the account Whoami named, %q", account, principal.Account)
	}
	if counted.calls != 1 {
		t.Errorf("a Whoami and an account id asked STS who the caller is %d times, want once", counted.calls)
	}
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

func TestAVariablesKeyThatNamesNoKMSKeyIsRefused(t *testing.T) {
	if _, err := provider.Decode[Options](Vendor, provider.Options{"variablesKey": "arn:aws:kms:eu-west-1:111122223333:key/abcd"}); err != nil {
		t.Fatalf("a kms key arn was refused: %v", err)
	}
	_, err := provider.Decode[Options](Vendor, provider.Options{"variablesKey": "arn:aws:s3:::a-bucket"})
	if err == nil {
		t.Fatal("a variablesKey that names no kms key was taken")
	}
	if !strings.Contains(err.Error(), "variablesKey") {
		t.Errorf("error = %q, want it to name the option", err)
	}
}

func TestLambdaDeclaresA200MiBFunctionSizeBudget(t *testing.T) {
	t.Parallel()

	p := NewProvider(Options{Region: "us-east-1"}, nil, aws.Config{Region: "us-east-1"}, defaultNamespace)
	if got := p.Facts().MaxFunctionBytes; got != 200*1024*1024 {
		t.Errorf("Facts().MaxFunctionBytes = %d, want 200 MiB, the room Lambda leaves under its 250 MB unzipped limit", got)
	}
}

func TestEmbeddingACompileCacheNobodyWarmsIsRefused(t *testing.T) {
	t.Parallel()

	_, err := New(context.Background(), provider.Settings{Options: provider.Options{
		"region":   "eu-west-2",
		"bytecode": map[string]any{"embed": true},
	}})
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || !strings.Contains(err.Error(), "bytecode.cache") {
		t.Fatalf("New() with bytecode.embed and no bytecode.cache = %v, want it refused as invalid naming bytecode.cache", err)
	}
}
