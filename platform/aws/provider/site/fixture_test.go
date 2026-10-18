package site

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
)

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

const testBuildID = "d1a2b3c4d5e6f708192a3b4c5d6e7f80"

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

func fixedRelease(t *testing.T) naming.ReleaseToken {
	t.Helper()
	release, err := naming.ParseReleaseToken("r3f8a1c90")
	if err != nil {
		t.Fatalf("parse release: %v", err)
	}
	return release
}

func storageCoordinate(env, slug, app string, release naming.ReleaseToken) naming.Coordinate {
	return naming.Coordinate{
		Project: naming.Sanitize(slug),
		Env:     naming.Sanitize(env),
		App:     naming.Sanitize(app),
		Release: release,
	}
}

func isrPrefixOf(c naming.Coordinate) string {
	return strings.TrimSuffix(c.ISRPrefix(), naming.PathSeparator)
}

var liveVariables = variables.Store{
	Table:  "ocel-variables",
	KeyARN: "arn:aws:kms:us-east-1:1234:key/prod-key",
	Tier:   "production",
}

func variable(key, value string, class resourcesv1.VariableClass) *contractv1.ManifestVariable {
	return &contractv1.ManifestVariable{Key: key, Class: class, Value: value}
}

const routedManifest = `{"rootFunction":"/","buildId":"WEB1","basePath":"","pathnames":[],"routes":{},"dispatch":{}}`

func siblingAppRoot(t *testing.T, apps ...string) string {
	t.Helper()
	files := map[string]string{}
	for _, app := range apps {
		files["apps/"+app+"/next-route-table.json"] = routedManifest
		files["apps/"+app+"/hosting.json"] = `{"version":1,"framework":"next","frameworkBuildId":"WEB1","rootFunction":"/","routeTable":"next","static":{"immutablePrefixes":["/_next/static/"],"mustRevalidatePrefixes":["/_next/static/service-worker/"]}}`
		files["apps/"+app+"/static/"+app+".txt"] = "an asset only " + app + " ships"
	}
	return writeTree(t, files)
}

func assetShippingConfig(t *testing.T, app string) publication {
	t.Helper()
	return publication{
		root:    siblingAppRoot(t, app),
		buckets: Buckets{Assets: "assets-bucket", Artifacts: "artifacts-bucket", EdgeHoldsKey: true},
	}
}

type recordingProgress struct {
	mu    sync.Mutex
	said  []string
	spans []string
}

func (r *recordingProgress) Say(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.said = append(r.said, message)
}

func (r *recordingProgress) Warn(message string) { r.Say(message) }

func (r *recordingProgress) Error(message string) { r.Say(message) }

func (r *recordingProgress) Detail(message string) { r.Say(message) }

func (r *recordingProgress) Debug(line string) { r.Say(line) }

func (r *recordingProgress) Span(name string, _, _ time.Time, err error, _ ...progress.Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		name += " (failed)"
	}
	r.spans = append(r.spans, name)
}
