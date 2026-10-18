package aws

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/cloudfrontpart"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/taginvalidator"
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

func partsNeededByBuilds(root string, appsAndFrameworks ...string) ([]string, error) {
	var uses []buildoutput.Uses
	for i := 0; i+1 < len(appsAndFrameworks); i += 2 {
		used, err := usesOf(root, appsAndFrameworks[i], appsAndFrameworks[i+1])
		if err != nil {
			return nil, err
		}
		uses = append(uses, used)
	}
	return partsNeededBy(uses), nil
}

func TestADeployNeedsThePartsItsBuildsUse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBuild(t, root, "web", `{"version":1,"framework":"next","needs":{"revalidation":{}}}`, true)
	writeBuild(t, root, "docs", `{"version":1,"framework":"next"}`, false)
	needed, err := partsNeededByBuilds(root, "web", "next", "docs", "next")
	if err != nil {
		t.Fatalf("partsNeededBy() = %v", err)
	}

	want := []string{imageoptimizer.Part, taginvalidator.Part}
	if !slices.Equal(needed, want) {
		t.Errorf("partsNeededBy() = %v, want %v: web optimizes images and revalidates", needed, want)
	}
}

func TestADeployWhoseBuildsUseNeitherNeedsNoPart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBuild(t, root, "api", `{"version":1,"framework":"go"}`, false)

	needed, err := partsNeededByBuilds(root, "api", "go")
	if err != nil {
		t.Fatalf("partsNeededBy() = %v", err)
	}
	if len(needed) != 0 {
		t.Errorf("partsNeededBy() = %v, want none", needed)
	}
}

func TestAnAppWithNoBuildToReadIsPresumedToUseWhatItsFrameworkCan(t *testing.T) {
	t.Parallel()

	needed, err := partsNeededByBuilds(t.TempDir(), "web", "next")
	if err != nil {
		t.Fatalf("partsNeededBy() = %v", err)
	}
	if want := []string{imageoptimizer.Part, taginvalidator.Part}; !slices.Equal(needed, want) {
		t.Errorf("partsNeededBy() = %v, want %v, as the app's serving presumes for a next app it cannot read the hosting of", needed, want)
	}
}

func TestThePreviewWildcardUsesThePartItsEdgeServesItThrough(t *testing.T) {
	t.Parallel()
	for kind, part := range map[edge.Kind]string{cloudfront.Kind: cloudfrontpart.Part, apigateway.Kind: notfoundapi.Part} {
		store := fake.NewKeyValues()
		recorded, err := json.Marshal(stackrecords.Wildcard{BaseDomain: "pr.example.com", Edge: kind})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Write(context.Background(), keyvalue.Entry{Key: stackrecords.WildcardKey(environment.TierPreview), Value: recorded}); err != nil {
			t.Fatal(err)
		}

		users, err := addedPartUsers(context.Background(), store, environment.TierPreview)
		if err != nil {
			t.Fatalf("addedPartUsers() = %v", err)
		}
		if !slices.Contains(users[part], "*.pr.example.com") {
			t.Errorf("%s users = %v with the preview wildcard on the %s edge, want *.pr.example.com: removing it would leave the wildcard unanswered", part, users[part], kind)
		}
		production, err := addedPartUsers(context.Background(), store, environment.TierProduction)
		if err != nil || len(production[part]) != 0 {
			t.Errorf("production %s users = %v, %v, want none: the wildcard serves previews", part, production[part], err)
		}
	}
}
