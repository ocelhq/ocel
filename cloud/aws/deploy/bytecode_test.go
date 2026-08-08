package deploy

import (
	"encoding/json"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// Bytecode caching costs a standing IAM grant and two extra deploy passes, so
// it must be something a project asks for rather than something every deploy
// pays for by default — off unless the deploying process names the literal
// "1", mirroring the strict-opt-in precedent every other flag in this package
// follows (bytecodeEmbedRequested's old spelling, before embedding folded into
// this gate).
func TestBytecodeCacheEnabled_OffByDefaultOnOnlyForTheLiteralOne(t *testing.T) {
	t.Setenv(bytecodeCacheEnv, "")
	if bytecodeCacheEnabled() {
		t.Error("bytecodeCacheEnabled() = true with no override, want false")
	}
	for _, v := range []string{"true", "yes", "on", "01", "1 ", "0"} {
		t.Setenv(bytecodeCacheEnv, v)
		if bytecodeCacheEnabled() {
			t.Errorf("bytecodeCacheEnabled() = true with OCEL_BYTECODE_CACHE=%q, want false (only \"1\" enables)", v)
		}
	}
	t.Setenv(bytecodeCacheEnv, "1")
	if !bytecodeCacheEnabled() {
		t.Error("bytecodeCacheEnabled() = false with OCEL_BYTECODE_CACHE=1, want true")
	}
}

// The gate is the function's own runtime, never its framework: translateFunction
// resolves nodejs24.x for an empty one, so an ordinary function (declaring
// nothing) is admitted, an explicit nodejs runtime is admitted, and anything
// else is not.
func TestIsNodeRuntime(t *testing.T) {
	for _, tc := range []struct {
		runtime string
		want    bool
	}{
		{"nodejs24.x", true},
		{"nodejs20.x", true},
		{"python3.12", false},
		{"go1.x", false},
		{"", false},
	} {
		if got := isNodeRuntime(tc.runtime); got != tc.want {
			t.Errorf("isNodeRuntime(%q) = %v, want %v", tc.runtime, got, tc.want)
		}
	}
}

// TestResolveBytecodeFunctionConfig_GateAndRuntime proves the two conditions
// that decide whether a function gets a bytecode config at all: the deploy-wide
// gate, and its own resolved runtime — nothing about its app or framework.
func TestResolveBytecodeFunctionConfig_GateAndRuntime(t *testing.T) {
	dir := writeTree(t, map[string]string{"src/server.js": "handler"})
	cfg := Config{ArtifactRoot: dir, AssetBucket: "assets", Env: "prod"}
	fn := &deploymentsv1.ManifestFunction{LogicalName: "api", ArtifactPath: ".", App: "api"}

	t.Run("nil with the gate off", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "")
		got, err := resolveBytecodeFunctionConfig(cfg, "proj", "api", fn)
		if err != nil {
			t.Fatalf("resolveBytecodeFunctionConfig: %v", err)
		}
		if got != nil {
			t.Errorf("config = %+v, want nil with the gate off", got)
		}
	})

	t.Run("nil for a non-node runtime even with the gate on", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "1")
		nonNode := &deploymentsv1.ManifestFunction{LogicalName: "worker", ArtifactPath: ".", App: "worker", Runtime: "python3.12"}
		got, err := resolveBytecodeFunctionConfig(cfg, "proj", "worker", nonNode)
		if err != nil {
			t.Fatalf("resolveBytecodeFunctionConfig: %v", err)
		}
		if got != nil {
			t.Errorf("config = %+v, want nil for a python3.12 function", got)
		}
	})

	t.Run("set for an ordinary (default-runtime) function with the gate on", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "1")
		got, err := resolveBytecodeFunctionConfig(cfg, "proj", "api", fn)
		if err != nil {
			t.Fatalf("resolveBytecodeFunctionConfig: %v", err)
		}
		if got == nil {
			t.Fatal("config = nil, want one for an ordinary nodejs function with the gate on")
		}
		if got.Bucket != "assets" {
			t.Errorf("Bucket = %q, want the asset bucket", got.Bucket)
		}
		hash, err := hashArtifact(dir, nil)
		if err != nil {
			t.Fatalf("hashArtifact: %v", err)
		}
		if want := bytecodePrefixFor("prod", "proj", "api", hash); got.Prefix != want {
			t.Errorf("Prefix = %q, want %q", got.Prefix, want)
		}
	})
}

// TestBytecodePrefix_StableAcrossTheOverlayMovesWithCode is the property the
// whole namespace exists for: renderAppBundle draws a fresh crypto/rand data
// key on every render, so an overlay-inclusive hash would move on every single
// deploy of an app declaring a `sensitive` variable — defeating the point of a
// cache meant to survive across builds. hashArtifact(dir, nil) — no overlay —
// is what resolveBytecodeFunctionConfig actually calls; this proves the
// resulting key is stable under exactly the thing that changes on every
// render, and still moves when the code itself does.
func TestBytecodePrefix_StableAcrossTheOverlayMovesWithCode(t *testing.T) {
	dir := writeTree(t, map[string]string{"src/server.js": "handler"})

	overlayA := map[string][]byte{".ocel/variables.enc": []byte("ciphertext-one")}
	overlayB := map[string][]byte{".ocel/variables.enc": []byte("ciphertext-two-and-longer")}

	bare, err := hashArtifact(dir, nil)
	if err != nil {
		t.Fatalf("hashArtifact: %v", err)
	}
	withA, err := hashArtifact(dir, overlayA)
	if err != nil {
		t.Fatalf("hashArtifact: %v", err)
	}
	withB, err := hashArtifact(dir, overlayB)
	if err != nil {
		t.Fatalf("hashArtifact: %v", err)
	}
	// The overlay changes hashArtifact's own output — that is what
	// TestHashArtifact_SensitiveToTheOverlay proves — but the bytecode config
	// never passes it an overlay at all, so its key is exactly the bare hash
	// regardless of what a fresh render's overlay happens to be.
	if withA == withB {
		t.Fatal("test setup: two different overlays must hash differently through hashArtifact")
	}
	prefixBare := bytecodePrefixFor("prod", "proj", "api", bare)
	if prefixBare != bytecodePrefixFor("prod", "proj", "api", bare) {
		t.Error("bytecodePrefixFor is not deterministic for the same hash")
	}

	changedCode := writeTree(t, map[string]string{"src/server.js": "handler v2"})
	movedHash, err := hashArtifact(changedCode, nil)
	if err != nil {
		t.Fatalf("hashArtifact: %v", err)
	}
	if movedHash == bare {
		t.Fatal("test setup: changed source must hash differently")
	}
	if bytecodePrefixFor("prod", "proj", "api", movedHash) == prefixBare {
		t.Error("bytecodePrefixFor did not move when the function's code changed")
	}
}

// TestBytecodeAppNamespace_OneGrantCoversEveryHash proves the namespace shape:
// the hash sits below the app segment, so a single IAM wildcard on the app's
// namespace covers every content hash that app's functions will ever produce,
// without a new grant per deploy.
func TestBytecodeAppNamespace_OneGrantCoversEveryHash(t *testing.T) {
	namespace := bytecodeAppNamespace("prod", "proj", "web")
	for _, hash := range []string{"aaaa", "bbbb", "0123456789abcdef"} {
		prefix := bytecodePrefixFor("prod", "proj", "web", hash)
		if len(prefix) <= len(namespace) || prefix[:len(namespace)] != namespace {
			t.Errorf("bytecodePrefixFor(%q) = %q, not under the app namespace %q", hash, prefix, namespace)
		}
	}
}

// TestBytecodeAppNamespace_NeverCollidesWithISROrAssetPrefix proves the
// dedicated root: appAssetPrefixFor (ISR/static assets, build-keyed) always
// leads with an env segment ("prod" or "preview-<identity>", envSegment in
// cloud/aws/server/server.go), never the literal "bytecode" — so it can never
// address the same object as bytecodeAppNamespace, which leads with the fixed
// "bytecode" segment instead.
func TestBytecodeAppNamespace_NeverCollidesWithISROrAssetPrefix(t *testing.T) {
	assetPrefix := appAssetPrefixFor("prod", "proj", "web", "BUILD1")
	bytecodeNS := bytecodeAppNamespace("prod", "proj", "web")
	if assetPrefix == bytecodeNS {
		t.Fatalf("asset prefix and bytecode namespace collided: %q", assetPrefix)
	}
	// The bytecode namespace must not even sit inside the asset prefix (or vice
	// versa) — prune sweeps the asset prefix's build segment, and a bytecode key
	// living under it would be reaped by the very build it exists to survive.
	if len(bytecodeNS) >= len(assetPrefix) && bytecodeNS[:len(assetPrefix)] == assetPrefix {
		t.Errorf("bytecode namespace %q sits inside the build-keyed asset prefix %q", bytecodeNS, assetPrefix)
	}
	if len(assetPrefix) >= len(bytecodeNS) && assetPrefix[:len(bytecodeNS)] == bytecodeNS {
		t.Errorf("asset prefix %q sits inside the bytecode namespace %q", assetPrefix, bytecodeNS)
	}
}

// TestBytecodePolicy_GrantsExactlyGetAndPutOnTheNamespace proves the grant:
// scoped to the app's bytecode namespace and nothing else, with the two
// actions the membrane's warm (Put) and read (Get) legs actually make.
func TestBytecodePolicy_GrantsExactlyGetAndPutOnTheNamespace(t *testing.T) {
	raw, err := bytecodePolicy("assets-xyz", "bytecode/prod/proj/web")
	if err != nil {
		t.Fatalf("bytecodePolicy: %v", err)
	}
	var doc struct {
		Statement []struct {
			Effect   string   `json:"Effect"`
			Action   []string `json:"Action"`
			Resource string   `json:"Resource"`
		}
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	if len(doc.Statement) != 1 {
		t.Fatalf("got %d statements, want 1", len(doc.Statement))
	}
	stmt := doc.Statement[0]
	if want := "arn:aws:s3:::assets-xyz/bytecode/prod/proj/web/*"; stmt.Resource != want {
		t.Errorf("Resource = %q, want %q", stmt.Resource, want)
	}
	wantActions := []string{"s3:GetObject", "s3:PutObject"}
	if len(stmt.Action) != len(wantActions) {
		t.Fatalf("Action = %v, want %v", stmt.Action, wantActions)
	}
	for i, a := range wantActions {
		if stmt.Action[i] != a {
			t.Errorf("Action[%d] = %q, want %q", i, stmt.Action[i], a)
		}
	}
}

// TestAppBytecodeNamespace_GateAndAtLeastOneNodeFunction proves the IAM grant
// follows the same two facts functionEnv does: the deploy-wide gate, and at
// least one of the app's functions resolving a nodejs* runtime. An app whose
// every function runs another runtime earns no grant at all.
func TestAppBytecodeNamespace_GateAndAtLeastOneNodeFunction(t *testing.T) {
	cfg := Config{Env: "prod"}
	mixed := []*deploymentsv1.ManifestFunction{
		{LogicalName: "worker", Runtime: "python3.12"},
		{LogicalName: "api"}, // defaults to nodejs24.x
	}
	allNonNode := []*deploymentsv1.ManifestFunction{{LogicalName: "worker", Runtime: "python3.12"}}

	t.Setenv(bytecodeCacheEnv, "")
	if got := appBytecodeNamespace(cfg, "proj", "web", mixed); got != "" {
		t.Errorf("appBytecodeNamespace with the gate off = %q, want none", got)
	}

	t.Setenv(bytecodeCacheEnv, "1")
	if got := appBytecodeNamespace(cfg, "proj", "web", mixed); got == "" {
		t.Error("appBytecodeNamespace = \"\", want a namespace: the app has at least one nodejs* function")
	}
	if got := appBytecodeNamespace(cfg, "proj", "web", allNonNode); got != "" {
		t.Errorf("appBytecodeNamespace = %q, want none for an app with no nodejs* function", got)
	}
}

// TestFunctionEnv_CarriesBytecodePrefixAndBucketForAnyRuntime proves the
// consequence of the gate moving off framework: a plain (non-Next) function's
// environment now carries both OCEL_BYTECODE_PREFIX and OCEL_BYTECODE_BUCKET
// when it has a bytecode config, which previously it could never get since
// isrConfig — and therefore OCEL_BYTECODE_PREFIX — only ever existed for a
// Next app.
func TestFunctionEnv_CarriesBytecodePrefixAndBucketForAnyRuntime(t *testing.T) {
	bc := &bytecodeFunctionConfig{Bucket: "assets-xyz", Prefix: "prod/proj/bytecode/api/deadbeef"}

	env := functionEnv(map[string]string{}, functionArgs{Handler: "src/server.js"}, nil, bc)

	if env["OCEL_BYTECODE_PREFIX"] != bc.Prefix {
		t.Errorf("OCEL_BYTECODE_PREFIX = %q, want %q", env["OCEL_BYTECODE_PREFIX"], bc.Prefix)
	}
	if env["OCEL_BYTECODE_BUCKET"] != bc.Bucket {
		t.Errorf("OCEL_BYTECODE_BUCKET = %q, want %q", env["OCEL_BYTECODE_BUCKET"], bc.Bucket)
	}
}

// TestFunctionEnv_OmitsBytecodeVarsWhenConfigIsNil proves a function the
// feature does not reach carries neither var, matching the membrane's own
// gate (resolveBytecodeResolution treats either as absent).
func TestFunctionEnv_OmitsBytecodeVarsWhenConfigIsNil(t *testing.T) {
	env := functionEnv(map[string]string{}, functionArgs{Handler: "src/server.js"}, nil, nil)

	for _, key := range []string{"OCEL_BYTECODE_PREFIX", "OCEL_BYTECODE_BUCKET"} {
		if _, ok := env[key]; ok {
			t.Errorf("env carries %s = %q, want it unset with no bytecode config", key, env[key])
		}
	}
}
