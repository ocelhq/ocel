package aws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	cloudfrontpart "github.com/ocelhq/ocel/platform/aws/provider/tier/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func writeBuild(t *testing.T, root, app, hosting string, imageConfig bool) {
	t.Helper()
	dir := buildoutput.AppRoot(root, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if hosting != "" {
		if err := os.WriteFile(filepath.Join(dir, buildoutput.HostingFile), []byte(hosting), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if imageConfig {
		if err := os.WriteFile(filepath.Join(dir, naming.ImageConfigFile), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func partsOver(store records.Store) tier.Parts {
	p := NewProvider(Options{Region: "us-east-1"}, nil, aws.Config{Region: "us-east-1"}, defaultNamespace)
	return p.tierParts(tier.Records{KeyValues: store, WrittenBy: "1.0.0", Region: "us-east-1", Reads: &tier.Reads{}})
}

func partsAddedForBuilds(root string, appsAndFrameworks ...string) ([]string, error) {
	var uses []buildoutput.Uses
	for i := 0; i+1 < len(appsAndFrameworks); i += 2 {
		used, err := usesOf(root, appsAndFrameworks[i], appsAndFrameworks[i+1])
		if err != nil {
			return nil, err
		}
		uses = append(uses, used)
	}
	return partsOver(fake.NewKeyValues()).AddedFor(uses, edge.None).Names(), nil
}

func TestADeployNeedsThePartsItsBuildsUse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBuild(t, root, "web", `{"version":1,"framework":"next","needs":{"revalidation":{}}}`, true)
	writeBuild(t, root, "docs", `{"version":1,"framework":"next"}`, false)
	needed, err := partsAddedForBuilds(root, "web", "next", "docs", "next")
	if err != nil {
		t.Fatalf("AddedFor() = %v", err)
	}

	want := []string{imageoptimizer.Part, taginvalidator.Part}
	if !slices.Equal(needed, want) {
		t.Errorf("AddedFor() = %v, want %v: web optimizes images and revalidates", needed, want)
	}
}

func TestADeployWhoseBuildsUseNeitherNeedsNoPart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBuild(t, root, "api", `{"version":1,"framework":"go"}`, false)

	needed, err := partsAddedForBuilds(root, "api", "go")
	if err != nil {
		t.Fatalf("AddedFor() = %v", err)
	}
	if len(needed) != 0 {
		t.Errorf("AddedFor() = %v, want none", needed)
	}
}

func TestAnAppWithNoBuildToReadIsPresumedToUseWhatItsFrameworkCan(t *testing.T) {
	t.Parallel()

	needed, err := partsAddedForBuilds(t.TempDir(), "web", "next")
	if err != nil {
		t.Fatalf("AddedFor() = %v", err)
	}
	if want := []string{imageoptimizer.Part, taginvalidator.Part}; !slices.Equal(needed, want) {
		t.Errorf("AddedFor() = %v, want %v, as the app's serving presumes for a next app it cannot read the hosting of", needed, want)
	}
}

func TestADeployBehindCloudFrontAddsTheCloudFrontPartBesideWhatItsBuildsUse(t *testing.T) {
	t.Parallel()

	added := partsOver(fake.NewKeyValues()).AddedFor([]buildoutput.Uses{{ImageOptimization: true}}, cloudfront.Kind).Names()
	if want := []string{cloudfrontpart.Part, imageoptimizer.Part}; !slices.Equal(added, want) {
		t.Errorf("AddedFor() = %v, want %v in name order, the order a project's record of the parts it uses keeps", added, want)
	}
}

func TestThePreviewWildcardUsesThePartItsEdgeServesItThrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for kind, name := range map[edge.Kind]string{cloudfront.Kind: cloudfrontpart.Part, apigateway.Kind: notfoundapi.Part} {
		store := fake.NewKeyValues()
		recorded, err := json.Marshal(stackrecords.Wildcard{BaseDomain: "pr.example.com", Edge: kind})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Write(ctx, records.Entry{Key: stackrecords.WildcardKey(environment.TierPreview), Value: recorded}); err != nil {
			t.Fatal(err)
		}
		for _, each := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
			if err := stackrecords.MarkBootstrapPartReady(ctx, store, each, name, stackrecords.BootstrapPart{WrittenBy: "1.0.0"}); err != nil {
				t.Fatal(err)
			}
		}
		part, found := partsOver(store).Named(name)
		if !found {
			t.Fatalf("tierParts() holds no %s", name)
		}

		preview, _, err := part.Describe(ctx, environment.TierPreview)
		if err != nil {
			t.Fatalf("Describe() = %v", err)
		}
		if !slices.Contains(preview.UsedBy, "*.pr.example.com") {
			t.Errorf("%s users = %v with the preview wildcard on the %s edge, want *.pr.example.com: removing it would leave the wildcard unanswered", name, preview.UsedBy, kind)
		}
		production, _, err := part.Describe(ctx, environment.TierProduction)
		if err != nil || len(production.UsedBy) != 0 {
			t.Errorf("production %s users = %v, %v, want none: the wildcard serves previews", name, production.UsedBy, err)
		}
	}
}

func TestTheTagInvalidatorIsRefusedAsNotReadyOnABootstrapThatPredatesItsRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	preview := environment.TierPreview
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String(server.URL)}
	p := NewProvider(Options{Region: "us-east-1"}, nil, cfg, defaultNamespace)
	p.identity.remember(struct{}{}, &sts.GetCallerIdentityOutput{Account: aws.String("111122223333")})
	p.deployed.remember(preview, account.Reading{Deployed: account.Deployed{
		Present: true, StateBucket: "state", ArtifactBucket: "artifacts", StateTable: "state-table", VariablesTable: "variables", AssetBucket: "assets",
		StateTableStreamARN: "arn:aws:dynamodb:us-east-1:111122223333:table/state-table/stream/2026-10-16T00:00:00.000",
	}})

	spec, err := p.tagInvalidatorSpec(ctx, preview)
	if err != nil {
		t.Fatalf("tagInvalidatorSpec() = %v", err)
	}
	_, err = taginvalidator.Work(taginvalidator.NewClients(p.aws), spec).Make(ctx, nil, false)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("make() = %v, want a not-ready refusal: the preview bootstrap holds no tag invalidator role to run the function as", err)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap preview") {
		t.Errorf("make() = %q, want it to name `ocel bootstrap preview`, which makes the role", refused.Message)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("AWS was called %d times, want none: a function with no role to run as cannot be created", n)
	}
}
