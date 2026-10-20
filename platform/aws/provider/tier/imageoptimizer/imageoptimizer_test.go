package imageoptimizer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/lambdapart/functiontest"
)

const (
	testBoundary  = "arn:aws:iam::" + functiontest.AccountID + ":policy/ocel-app-boundary"
	testBucket    = "ocel-bootstrap-assetbucket-abc"
	testBucketARN = "arn:aws:s3:::" + testBucket
)

func spec(tier environment.Tier) imageoptimizer.Spec {
	return imageoptimizer.Spec{
		Namespace:      "ocel",
		Tier:           tier,
		Region:         functiontest.Region,
		Boundary:       testBoundary,
		RolePath:       "/ocel/" + string(tier) + "/app/",
		AssetBucket:    testBucket,
		AssetBucketARN: testBucketARN,
	}
}

func clients(account *functiontest.Account) imageoptimizer.Clients {
	c := account.Clients()
	return imageoptimizer.Clients{IAM: c.IAM, Lambda: account.Lambda(), Logs: c.Logs}
}

func TestEnsureMakesAnOptimizerTheDeployCredentialMayMakeUnderTheAppBoundary(t *testing.T) {
	t.Parallel()
	account := functiontest.New()
	names := imageoptimizer.NamesFor("ocel", environment.TierPreview)

	resources, err := imageoptimizer.Ensure(context.Background(), clients(account), spec(environment.TierPreview))
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	if names.Function != "ocel-optimizer-preview" {
		t.Errorf("function name = %q, want the namespace's preview optimizer", names.Function)
	}
	role, ok := account.Roles[names.Role]
	if !ok {
		t.Fatalf("no role %s", names.Role)
	}
	if role.Path != "/ocel/preview/app/" || role.Boundary != testBoundary || role.Tags["ocel:env-tier"] != "preview" || role.Tags["ocel:managed-by"] != "ocel" {
		t.Errorf("role = %+v, want it at the app role path under the app boundary, tagged as ocel's of preview", role)
	}
	var policy struct {
		Statement []struct{ Action, Resource any }
	}
	for _, document := range role.Policies {
		if err := json.Unmarshal([]byte(document), &policy); err != nil {
			t.Fatal(err)
		}
	}
	var reads []string
	for _, statement := range policy.Statement {
		if statement.Action == "s3:GetObject" {
			reads = anyStrings(statement.Resource)
		}
	}
	if want := []string{testBucketARN + "/assets/*", testBucketARN + "/image-config/*"}; !slices.Equal(reads, want) {
		t.Errorf("role reads %v, want exactly %v: the asset and image-config kinds", reads, want)
	}
	fn, ok := account.Functions[names.Function]
	if !ok {
		t.Fatalf("no function %s", names.Function)
	}
	if !bytes.Equal(fn.Code, payloads.ImageOptimizer().Bytes) {
		t.Errorf("function code is not the image optimizer payload")
	}
	if fn.Tags["ocel:component"] != "image-optimizer" || fn.Tags["ocel:env-tier"] != "preview" {
		t.Errorf("function tags = %v, want the image-optimizer component of preview, the tag app roles invoke it by", fn.Tags)
	}
	if fn.Input.Environment.Variables["OCEL_IMAGE_ASSET_BUCKET"] != testBucket {
		t.Errorf("function environment = %v, want the asset bucket it reads", fn.Input.Environment.Variables)
	}
	if fn.URL == nil || fn.URL.AuthType != lambdatypes.FunctionUrlAuthTypeAwsIam || fn.URL.InvokeMode != lambdatypes.InvokeModeResponseStream {
		t.Errorf("function url = %+v, want an IAM-authenticated, response-streaming url", fn.URL)
	}
	if url := imageoptimizer.URLOf(resources); url != "https://"+names.Function+".lambda-url."+functiontest.Region+".on.aws/" {
		t.Errorf("URLOf(resources) = %q, want the function url Ensure made", url)
	}
}

func TestEnsureAgainLeavesOneOptimizerAtTheSameURL(t *testing.T) {
	t.Parallel()
	account := functiontest.New()
	first, err := imageoptimizer.Ensure(context.Background(), clients(account), spec(environment.TierProduction))
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	second, err := imageoptimizer.Ensure(context.Background(), clients(account), spec(environment.TierProduction))
	if err != nil {
		t.Fatalf("Ensure() again = %v", err)
	}

	if len(account.Functions) != 1 || imageoptimizer.URLOf(first) != imageoptimizer.URLOf(second) {
		t.Errorf("functions %d, urls %q then %q; want one function at one url", len(account.Functions), imageoptimizer.URLOf(first), imageoptimizer.URLOf(second))
	}
	if account.Called("UpdateFunctionUrlConfig") != 1 {
		t.Errorf("UpdateFunctionUrlConfig called %d times, want the second run to update the url it found", account.Called("UpdateFunctionUrlConfig"))
	}
}

func TestEnsuringAnOptimizerWhoseRoleTrustDriftedMovesTheRestForwardAndSaysItIsBehind(t *testing.T) {
	t.Parallel()
	account := functiontest.New()
	names := imageoptimizer.NamesFor("ocel", environment.TierProduction)
	if _, err := imageoptimizer.Ensure(context.Background(), clients(account), spec(environment.TierProduction)); err != nil {
		t.Fatal(err)
	}
	account.Roles[names.Role].Trust = `{"Version":"2012-10-17","Statement":[]}`
	account.Functions[names.Function].Code = []byte("an older build's optimizer")

	resources, err := imageoptimizer.Ensure(context.Background(), clients(account), spec(environment.TierProduction))
	if !errors.Is(err, stackrecords.ErrPartBehind) || imageoptimizer.URLOf(resources) == "" {
		t.Fatalf("Ensure() = %v, %v; want the optimizer's resources and the part said to be behind", resources, err)
	}
	if !bytes.Equal(account.Functions[names.Function].Code, payloads.ImageOptimizer().Bytes) || !account.RunsAsItsRole(names.Function) {
		t.Error("the optimizer was not moved forward as the role it runs as")
	}

	bootstrapping := spec(environment.TierProduction)
	bootstrapping.UpdatesTrust = true
	if _, err := imageoptimizer.Ensure(context.Background(), clients(account), bootstrapping); err != nil {
		t.Fatalf("Ensure() by a bootstrap = %v", err)
	}
	if account.Called("DeleteRole") > 0 || !account.RunsAsItsRole(names.Function) || account.Called("UpdateAssumeRolePolicy") != 1 {
		t.Errorf("calls = %v, want the bootstrap to put back the role's trust in place", account.Calls)
	}
}

func TestDigestChangesWithWhatTheOptimizerReads(t *testing.T) {
	t.Parallel()
	moved := spec(environment.TierProduction)
	moved.AssetBucket = "another"

	if imageoptimizer.Digest(spec(environment.TierProduction)) == imageoptimizer.Digest(moved) {
		t.Errorf("Digest() is the same for two asset buckets, so a moved bucket would leave the optimizer reading the old one")
	}
}

func TestRemoveTakesTheOptimizerAndItsRoleDown(t *testing.T) {
	t.Parallel()
	account := functiontest.New()
	if _, err := imageoptimizer.Ensure(context.Background(), clients(account), spec(environment.TierProduction)); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	if err := imageoptimizer.Remove(context.Background(), clients(account), "ocel", environment.TierProduction); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if len(account.Functions) != 0 || len(account.Roles) != 0 || len(account.LogGroups) != 0 {
		t.Errorf("left functions %v roles %v log groups %v, want none", account.Functions, account.Roles, account.LogGroups)
	}
}

func anyStrings(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, item.(string))
		}
		return out
	}
	return nil
}
