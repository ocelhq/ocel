package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

func buildIDFor(label string) string {
	if naming.ValidateBuildID(label) == nil {
		return label
	}
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:16])
}

func deployedAs(buildID string) provider.Release { return fingerprinted(buildID, "") }

func fingerprinted(buildID, values string) provider.Release {
	return deployedInto(stackrecords.ProductionEnv, buildID, values)
}

func deployedInto(environment, buildID, values string) provider.Release {
	id, err := provider.NewRelease(buildIDFor(buildID), "p1", environment, values)
	if err != nil {
		panic(err)
	}
	return id
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

func bytecodePrefixOf(c naming.Coordinate) string {
	return strings.TrimSuffix(c.BytecodePrefix(), naming.PathSeparator)
}

func appEdgeBundleKey(c naming.Coordinate) string {
	return path.Join(c.StoragePrefix()+"edge", "bundle.json")
}

func edgeRouteTable(key string) *provider.EdgeRouteTable {
	return &provider.EdgeRouteTable{
		Location: router.RouteTableLocation{Format: edge.RouteTableNext, Key: key},
		Table:    []byte(`{"routes":[{"source":"^/(?<slug>[^/]+)$"}]}`),
	}
}

const appsDirName = "apps"

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, contents := range withHostings(t, files) {
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

type fakeArtifactStore struct {
	exists  map[string]bool
	headErr error
	putErr  error

	mu            sync.Mutex
	puts          []string
	buckets       []string
	putBodies     map[string]string
	contentTypes  map[string]string
	cacheControls map[string]string
	taggings      map[string]string
}

func (f *fakeArtifactStore) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.exists[aws.ToString(in.Key)] {
		return &s3.HeadObjectOutput{}, nil
	}
	return nil, &s3types.NotFound{}
}

func (f *fakeArtifactStore) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}
	key := aws.ToString(in.Key)
	if aws.ToString(in.IfNoneMatch) == "*" && f.exists[key] {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed"}
	}
	var body []byte
	if in.Body != nil {
		body, _ = io.ReadAll(in.Body)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, key)
	f.buckets = append(f.buckets, aws.ToString(in.Bucket))
	if in.ContentType != nil {
		if f.contentTypes == nil {
			f.contentTypes = map[string]string{}
		}
		f.contentTypes[key] = aws.ToString(in.ContentType)
	}
	if in.Tagging != nil {
		if f.taggings == nil {
			f.taggings = map[string]string{}
		}
		f.taggings[key] = aws.ToString(in.Tagging)
	}
	if in.CacheControl != nil {
		if f.cacheControls == nil {
			f.cacheControls = map[string]string{}
		}
		f.cacheControls[key] = aws.ToString(in.CacheControl)
	}
	if in.Body != nil {
		if f.putBodies == nil {
			f.putBodies = map[string]string{}
		}
		f.putBodies[key] = string(body)
	}
	return &s3.PutObjectOutput{}, nil
}

var nextStatic = &edge.Static{
	ImmutablePrefixes:      []string{"/_next/static/"},
	MustRevalidatePrefixes: []string{"/_next/static/service-worker/"},
}

func hostingJSON(t *testing.T, runtime, buildID string) string {
	t.Helper()
	hosting := buildoutput.Hosting{Version: buildoutput.HostingVersion, Framework: runtime, FrameworkBuildID: buildID, RootFunction: "/"}
	if runtime == buildoutput.FrameworkNext {
		hosting.RouteTable = edge.RouteTableNext
		hosting.Static = nextStatic
	}
	raw, err := json.Marshal(hosting)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func buildIDOf(t *testing.T, routeTable string) string {
	t.Helper()
	var routing struct {
		BuildID string `json:"buildId"`
	}
	if err := json.Unmarshal([]byte(routeTable), &routing); err != nil {
		t.Fatalf("parse route table %s: %v", routeTable, err)
	}
	return routing.BuildID
}

func withHostings(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	out := maps.Clone(files)
	for rel, contents := range files {
		app, ok := appOfRouteTable(rel)
		if !ok {
			continue
		}
		hostingPath := path.Join(appsDirName, app, buildoutput.HostingFile)
		if _, written := out[hostingPath]; written {
			continue
		}
		out[hostingPath] = hostingJSON(t, buildoutput.FrameworkNext, buildIDOf(t, contents))
	}
	return out
}

func appOfRouteTable(rel string) (string, bool) {
	parts := strings.Split(rel, "/")
	if len(parts) != 3 || parts[0] != appsDirName || parts[2] != edge.NextRouteTableFile {
		return "", false
	}
	return parts[1], true
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

func testRuntimeLayers() map[string]string {
	return map[string]string{
		arch.X8664: testRuntimeLayerARN(arch.X8664),
		arch.ARM64: testRuntimeLayerARN(arch.ARM64),
	}
}

func testRuntimeLayerARN(architecture string) string {
	return "arn:aws:lambda:us-east-1:123456789012:layer:ocel-runtime-" + architecture + "-beefbeefbeef:3"
}

const routedManifest = `{"rootFunction":"/","buildId":"WEB1","basePath":"","pathnames":[],"routes":{},"dispatch":{}}`

func routedArtifactRoot(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/next-route-table.json": routedManifest,
		"apps/web/hosting.json":          `{"version":1,"framework":"next","frameworkBuildId":"WEB1","rootFunction":"/","routeTable":"next","static":{"immutablePrefixes":["/_next/static/"],"mustRevalidatePrefixes":["/_next/static/service-worker/"]}}`,
	})
}

func routedConfig(t *testing.T, kind edge.Kind) Config {
	t.Helper()
	return Config{
		ArtifactRoot:      routedArtifactRoot(t),
		AssetBucket:       "assets-bucket",
		ImageOptimizerURL: "https://optimizer.lambda-url.us-east-1.on.aws/",
		RuntimeLayers:     testRuntimeLayers(),
		Slug:              "shop",
		Edge:              fakeEdgeOf(kind),
		AppBoundaryARN:    componenttest.BoundaryARN,
	}
}

func routedCoordinate(t *testing.T) naming.Coordinate {
	t.Helper()
	return storageCoordinate("prod", "shop", "web", fixedRelease(t))
}

func functionEnvOf(t *testing.T, rec *inputRecorder, name string) map[string]string {
	t.Helper()
	variables := rec.object(t, "aws:lambda/function:Function", name, "environment")["variables"]
	if !variables.IsObject() {
		t.Fatalf("function %s has no environment variables", name)
	}
	env := map[string]string{}
	for key, value := range variables.ObjectValue() {
		if value.IsString() {
			env[string(key)] = value.StringValue()
		}
	}
	return env
}
