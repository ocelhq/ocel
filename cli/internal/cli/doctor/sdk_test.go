package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func sdkProject(t *testing.T, files map[string]string) *projectconfig.Config {
	t.Helper()

	dir := t.TempDir()
	for name, contents := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &projectconfig.Config{
		Slug: "fixture",
		Dir:  dir,
		Path: filepath.Join(dir, "ocel.json"),
		Apps: []projectconfig.App{{Name: "web", Path: "apps/web"}, {Name: "api", Path: "apps/api"}},
	}
}

func TestDoctorComparesEverySDKTheProjectDeclaresWithTheCLI(t *testing.T) {
	t.Parallel()

	cfg := sdkProject(t, map[string]string{
		"package.json": `{"dependencies": {"ocel": "^0.0.2"}}`,
		"pyproject.toml": `[project]
name = "shop"
dependencies = ["ocelot>=1", "ocel[postgres]==0.0.3"]
`,
		"Cargo.toml": `[package]
name = "shop"

[dependencies]
sdk = { package = "ocel-sdk", version = "0.0.3", features = ["postgres"] }
`,
		"go.mod":                "module example.com/shop\n\ngo 1.24\n\nrequire ocel.dev v0.0.1\n",
		"apps/web/package.json": `{"devDependencies": {"ocel": "workspace:*"}}`,
		"apps/api/package.json": `{"dependencies": {"react": "19.0.0"}}`,
	})

	got := sdkChecks(cfg, "0.0.3")
	want := []check{
		{verdict: verdictFail, text: "the JavaScript SDK (ocel) ^0.0.2 in package.json — this CLI is 0.0.3, and an SDK works with the CLI of its own release", fix: "run `npm i ocel@0.0.3`"},
		{verdict: verdictPass, text: "the Python SDK (ocel) ==0.0.3 in pyproject.toml — works with this CLI 0.0.3"},
		{verdict: verdictPass, text: "the Rust SDK (ocel-sdk) 0.0.3 in Cargo.toml — works with this CLI 0.0.3"},
		{verdict: verdictFail, text: "the Go SDK (ocel.dev) v0.0.1 in go.mod — this CLI is 0.0.3, and an SDK works with the CLI of its own release", fix: "run `go get ocel.dev@v0.0.3`"},
		{verdict: verdictNeutral, text: "the JavaScript SDK (ocel) workspace:* in apps/web/package.json — names no release to compare with this CLI"},
	}
	if len(got) != len(want) {
		t.Fatalf("sdkChecks() = %+v, want %d checks", got, len(want))
	}
	for i := range want {
		if got[i].verdict != want[i].verdict || got[i].text != want[i].text || got[i].fix != want[i].fix {
			t.Errorf("check %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDoctorComparesNoSDKWithADevelopmentCLI(t *testing.T) {
	t.Parallel()

	cfg := sdkProject(t, map[string]string{"package.json": `{"dependencies": {"ocel": "0.0.2"}}`})
	got := sdkChecks(cfg, "dev")
	if len(got) != 1 || got[0].verdict != verdictNeutral {
		t.Fatalf("sdkChecks() = %+v, want one neutral check", got)
	}
}

func TestDoctorSaysNothingOfAnSDKTheProjectDoesNotDeclare(t *testing.T) {
	t.Parallel()

	cfg := sdkProject(t, map[string]string{"go.mod": "module example.com/shop\n\ngo 1.24\n"})
	if got := sdkChecks(cfg, "0.0.3"); len(got) != 0 {
		t.Fatalf("sdkChecks() = %+v, want none", got)
	}
}
