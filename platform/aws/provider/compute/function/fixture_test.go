package function

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

const (
	testNamespace             = "ocel"
	rolePolicyToken           = "aws:iam/rolePolicy:RolePolicy"
	productionVariablesKeyARN = "arn:aws:kms:us-east-1:1234:key/prod-key"
)

type testConfig struct {
	ArtifactRoot         string
	EdgeBucket           string
	ImageOptimizerURL    string
	Slug                 string
	Tier                 environment.Tier
	OriginSecret         string
	PreviousOriginSecret string
	StateTable           string
	Edge                 edge.Edge
}

func fixedRelease(t *testing.T) naming.ReleaseToken {
	t.Helper()
	release, err := naming.ParseReleaseToken("r3f8a1c90")
	if err != nil {
		t.Fatalf("parse release: %v", err)
	}
	return release
}

func testStack(t *testing.T, env, app string) naming.StackName {
	t.Helper()
	return naming.AppStack(env, app, fixedRelease(t))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.work found above the working directory")
		}
		dir = parent
	}
}

func nextCacheFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(repoRoot(t), "frameworks", "next", "cache", "fixtures", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func buildIDFor(label string) string {
	if naming.ValidateBuildID(label) == nil {
		return label
	}
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:16])
}

func deployedAs(buildID string) provider.Release {
	id, err := provider.NewRelease(buildIDFor(buildID), "p1", stackrecords.ProductionEnv, "")
	if err != nil {
		panic(err)
	}
	return id
}

func releaseTokenFor(buildID string) string {
	return deployedAs(buildID).Token().String()
}

func storageCoordinate(env, slug, app string, release naming.ReleaseToken) naming.Coordinate {
	return naming.Coordinate{
		Project: naming.Sanitize(slug),
		Env:     naming.Sanitize(env),
		App:     naming.Sanitize(app),
		Release: release,
	}
}

func nextManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "proj",
		Apps: []*contractv1.ManifestApp{{Name: "web", Framework: &contractv1.Framework{Name: "next"},
			Artifact: serverlessArtifact(&contractv1.ManifestFunction{LogicalName: "web_index", Framework: &contractv1.Framework{Name: "next"}})}},
	}
}

func serverlessArtifact(functions ...*contractv1.ManifestFunction) *contractv1.ManifestApp_Serverless {
	return &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: functions}}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, contents := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func namedFunction(stack naming.StackName, logical string) component.Name {
	names, err := NamesFor(testNamespace, "shop", stack, logical)
	if err != nil {
		panic(err)
	}
	return names.Function
}

func namedRole(stack naming.StackName) component.Name {
	name, err := component.RoleName(testNamespace, component.AppRoleCoordinate("shop", stack))
	if err != nil {
		panic(err)
	}
	return name
}

func mustSurfaces(t *testing.T, stack naming.StackName, logical string) map[string][]component.ResourceRef {
	t.Helper()
	names, err := NamesFor(testNamespace, "shop", stack, logical)
	if err != nil {
		t.Fatal(err)
	}
	return names.Surfaces()
}
