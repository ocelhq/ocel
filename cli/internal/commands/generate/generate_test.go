package generate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	"github.com/ocelhq/ocel/pkg/statedir"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func declaring(dependencies *Dependencies, definitions ...*resourcesv1.VariableDefinition) {
	dependencies.CollectDeclarations = func(ctx context.Context, _ *project.Project, declarations *variables.Declarations, _, _ io.Writer) ([]declaration.Resource, error) {
		if _, err := declarations.DeclareEnv(ctx, &resourcesv1.DeclareEnvRequest{Definitions: definitions}); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

func plainClient(key string) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{
		Key:              key,
		Class:            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		ClientAccessible: true,
		Required:         true,
	}
}

func setUpGenerateFixture(t *testing.T, config, tsconfig string) string {
	t.Helper()
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), config)
	clitest.WriteFile(t, filepath.Join(root, "package.json"), "{}\n")
	if tsconfig != "" {
		clitest.WriteFile(t, filepath.Join(root, "tsconfig.json"), tsconfig)
	}
	return root
}

const generateSoloConfig = `
export default { slug: "test-app" };
`

func TestGenerateWritesTheClientAccessorWithoutALoginOrAProvider(t *testing.T) {
	t.Run("writes the accessor without a login or a provider", func(t *testing.T) {
		root := setUpGenerateFixture(t, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: ".", compute: { serverless: { framework: "next" } } }],
};
`, "{\n  \"compilerOptions\": {}\n}\n")

		dependencies := newTestDependencies()
		declaring(&dependencies, plainClient("NEXT_PUBLIC_SITE_URL"), plainClient("NEXT_PUBLIC_APP_ID"))

		var stdout, stderr bytes.Buffer
		if err := runGenerate(context.Background(), dependencies, root, &stdout, &stderr); err != nil {
			t.Fatalf("runGenerate: %v", err)
		}

		accessor, err := os.ReadFile(filepath.Join(root, statedir.Name, "env-client.ts"))
		if err != nil {
			t.Fatalf("runGenerate wrote no accessor: %v", err)
		}
		for _, want := range []string{
			"NEXT_PUBLIC_APP_ID: inlined(schema, \"NEXT_PUBLIC_APP_ID\", process.env.NEXT_PUBLIC_APP_ID)",
			"NEXT_PUBLIC_SITE_URL: inlined(schema, \"NEXT_PUBLIC_SITE_URL\", process.env.NEXT_PUBLIC_SITE_URL)",
		} {
			if !strings.Contains(string(accessor), want) {
				t.Errorf("accessor = %s, want it to name %q", accessor, want)
			}
		}

		tsconfig, err := os.ReadFile(filepath.Join(root, "tsconfig.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(tsconfig), statedir.Name+"/env-client.ts") {
			t.Errorf("tsconfig.json = %s, want it to map 'ocel/env/client' at the accessor", tsconfig)
		}

		if got, want := stdout.String(), "Generated the client accessor for 3 client-accessible variables\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
	})

	t.Run("generates for declarations no value backs", func(t *testing.T) {
		root := setUpGenerateFixture(t, generateSoloConfig, "{}\n")

		dependencies := newTestDependencies()
		declaring(&dependencies, plainClient("NEXT_PUBLIC_SITE_URL"))

		var stdout, stderr bytes.Buffer
		if err := runGenerate(context.Background(), dependencies, root, &stdout, &stderr); err != nil {
			t.Fatalf("runGenerate refused a declaration nothing has a value for: %v", err)
		}
		if _, err := os.ReadFile(filepath.Join(root, statedir.Name, "env-client.ts")); err != nil {
			t.Fatalf("runGenerate wrote no accessor: %v", err)
		}
	})

	t.Run("names only client-accessible plaintext", func(t *testing.T) {
		root := setUpGenerateFixture(t, generateSoloConfig, "{}\n")

		dependencies := newTestDependencies()
		declaring(&dependencies,
			plainClient("NEXT_PUBLIC_SITE_URL"),
			&resourcesv1.VariableDefinition{Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
			&resourcesv1.VariableDefinition{Key: "API_TOKEN", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, ClientAccessible: true},
			&resourcesv1.VariableDefinition{Key: "SIGNING_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, ClientAccessible: true},
		)

		var stdout, stderr bytes.Buffer
		if err := runGenerate(context.Background(), dependencies, root, &stdout, &stderr); err != nil {
			t.Fatalf("runGenerate: %v", err)
		}

		accessor, err := os.ReadFile(filepath.Join(root, statedir.Name, "env-client.ts"))
		if err != nil {
			t.Fatal(err)
		}
		for _, unwanted := range []string{"DATABASE_URL", "API_TOKEN", "SIGNING_KEY"} {
			if strings.Contains(string(accessor), unwanted) {
				t.Errorf("accessor = %s, want it not to name %q", accessor, unwanted)
			}
		}
	})

	t.Run("writes the built-in deployment url for a project that declares no client value", func(t *testing.T) {
		root := setUpGenerateFixture(t, generateSoloConfig, "{}\n")

		dependencies := newTestDependencies()
		declaring(&dependencies, &resourcesv1.VariableDefinition{Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN})

		var stdout, stderr bytes.Buffer
		if err := runGenerate(context.Background(), dependencies, root, &stdout, &stderr); err != nil {
			t.Fatalf("runGenerate: %v", err)
		}

		accessor, err := os.ReadFile(filepath.Join(root, statedir.Name, "env-client.ts"))
		if err != nil {
			t.Fatalf("runGenerate wrote no accessor: %v", err)
		}
		if !strings.Contains(string(accessor), "NEXT_PUBLIC_OCEL_URL") {
			t.Errorf("accessor = %s, want the deployment url every app is handed", accessor)
		}
		tsconfig, err := os.ReadFile(filepath.Join(root, "tsconfig.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(tsconfig), statedir.Name+"/env-client.ts") {
			t.Errorf("tsconfig.json = %s, want it to map 'ocel/env/client' at the accessor", tsconfig)
		}
		if got, want := stdout.String(), "Generated the client accessor for 1 client-accessible variable\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
	})

	t.Run("surfaces a discovery failure", func(t *testing.T) {
		root := setUpGenerateFixture(t, generateSoloConfig, "")

		dependencies := newTestDependencies()
		dependencies.CollectDeclarations = func(context.Context, *project.Project, *variables.Declarations, io.Writer, io.Writer) ([]declaration.Resource, error) {
			return nil, errors.New("discovery blew up")
		}

		var stdout, stderr bytes.Buffer
		err := runGenerate(context.Background(), dependencies, root, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "discovery blew up") {
			t.Fatalf("runGenerate = %v, want the discovery failure", err)
		}
	})
}

func TestGenerateWritesTheRealtimeChannelTypesBesideTheConfig(t *testing.T) {
	root := setUpGenerateFixture(t, generateSoloConfig, "")

	dependencies := newTestDependencies()
	dependencies.CollectDeclarations = func(context.Context, *project.Project, *variables.Declarations, io.Writer, io.Writer) ([]declaration.Resource, error) {
		return []declaration.Resource{{
			Name: "app",
			Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME,
			Realtime: &resourcesv1.RealtimeConfig{Channels: []*resourcesv1.RealtimeChannel{{
				Pattern: "orders/:orderId",
				Schema:  `{"type":"object","properties":{"status":{"type":"string"}},"required":["status"]}`,
				Publish: resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
			}}},
		}}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := runGenerate(context.Background(), dependencies, root, &stdout, &stderr); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	types, err := os.ReadFile(filepath.Join(root, "ocel-realtime.d.ts"))
	if err != nil {
		t.Fatalf("runGenerate wrote no realtime types: %v", err)
	}
	if want := `"orders/:orderId": { event: { status: string } };`; !strings.Contains(string(types), want) {
		t.Errorf("ocel-realtime.d.ts = %s, want it to type %s", types, want)
	}
	if want := "Generated the realtime channel types in ocel-realtime.d.ts\n"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to say %q", stdout.String(), want)
	}
}

func TestGenerateAsJSONPrintsTheFilesItWrote(t *testing.T) {
	root := setUpGenerateFixture(t, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: ".", compute: { serverless: { framework: "next" } } }],
};
`, "{}\n")
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	dependencies.CollectDeclarations = func(ctx context.Context, _ *project.Project, declarations *variables.Declarations, _, _ io.Writer) ([]declaration.Resource, error) {
		if _, err := declarations.DeclareEnv(ctx, &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{plainClient("NEXT_PUBLIC_SITE_URL")}}); err != nil {
			return nil, err
		}
		return []declaration.Resource{{
			Name: "app",
			Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME,
			Realtime: &resourcesv1.RealtimeConfig{Channels: []*resourcesv1.RealtimeChannel{{
				Pattern: "orders/:orderId",
				Schema:  `{"type":"object"}`,
				Publish: resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
			}}},
		}}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := runGenerate(context.Background(), dependencies, root, &stdout, &stderr); err != nil {
		t.Fatalf("runGenerate err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var got resultv1.GenerateResult
	clitest.DecodeResultInto(t, stdout.String(), &got)
	wantFiles := []string{
		filepath.Join(root, statedir.Name, "env-client.ts"),
		filepath.Join(root, "tsconfig.json"),
		filepath.Join(root, "ocel-realtime.d.ts"),
	}
	if !slices.Equal(got.GetFiles(), wantFiles) {
		t.Errorf("files = %v, want %v", got.GetFiles(), wantFiles)
	}
	if got.GetClientVariableCount() != 2 {
		t.Errorf("client_variable_count = %d, want the declared one and the built-in deployment url", got.GetClientVariableCount())
	}
	for _, file := range got.GetFiles() {
		if _, err := os.Stat(file); err != nil {
			t.Errorf("file %s is reported written but %v", file, err)
		}
	}
}

func TestGenerateAsJSONOfAProjectWithoutRealtimeNamesOnlyTheAccessor(t *testing.T) {
	root := setUpGenerateFixture(t, generateSoloConfig, "")
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	declaring(&dependencies)

	var stdout, stderr bytes.Buffer
	if err := runGenerate(context.Background(), dependencies, root, &stdout, &stderr); err != nil {
		t.Fatalf("runGenerate err = %v", err)
	}

	var got resultv1.GenerateResult
	clitest.DecodeResultInto(t, stdout.String(), &got)
	if want := []string{filepath.Join(root, statedir.Name, "env-client.ts")}; !slices.Equal(got.GetFiles(), want) {
		t.Errorf("files = %v, want %v", got.GetFiles(), want)
	}
}

func TestGenerateAsJSONNamesTheTsconfigOnlyWhenItRewritesIt(t *testing.T) {
	root := setUpGenerateFixture(t, generateSoloConfig, "{\n  \"compilerOptions\": {}\n}\n")
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	declaring(&dependencies, plainClient("NEXT_PUBLIC_SITE_URL"))

	var first bytes.Buffer
	if err := runGenerate(context.Background(), dependencies, root, &first, io.Discard); err != nil {
		t.Fatalf("runGenerate err = %v", err)
	}
	var rewrote resultv1.GenerateResult
	clitest.DecodeResultInto(t, first.String(), &rewrote)
	if want := []string{filepath.Join(root, statedir.Name, "env-client.ts"), filepath.Join(root, "tsconfig.json")}; !slices.Equal(rewrote.GetFiles(), want) {
		t.Errorf("files after mapping the tsconfig = %v, want %v", rewrote.GetFiles(), want)
	}

	var second bytes.Buffer
	if err := runGenerate(context.Background(), dependencies, root, &second, io.Discard); err != nil {
		t.Fatalf("runGenerate err = %v", err)
	}
	var unchanged resultv1.GenerateResult
	clitest.DecodeResultInto(t, second.String(), &unchanged)
	if want := []string{filepath.Join(root, statedir.Name, "env-client.ts")}; !slices.Equal(unchanged.GetFiles(), want) {
		t.Errorf("files with the tsconfig already mapped = %v, want %v", unchanged.GetFiles(), want)
	}
}

func TestGenerateReportsTheClientAccessorBeforeTheRealtimeTypesFail(t *testing.T) {
	root := setUpGenerateFixture(t, generateSoloConfig, "")
	if err := os.Mkdir(filepath.Join(root, "ocel-realtime.d.ts"), 0o755); err != nil {
		t.Fatal(err)
	}
	dependencies := newTestDependencies()
	dependencies.CollectDeclarations = func(context.Context, *project.Project, *variables.Declarations, io.Writer, io.Writer) ([]declaration.Resource, error) {
		return []declaration.Resource{{
			Name: "app",
			Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME,
			Realtime: &resourcesv1.RealtimeConfig{Channels: []*resourcesv1.RealtimeChannel{{
				Pattern: "orders/:orderId",
				Schema:  `{"type":"object"}`,
				Publish: resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
			}}},
		}}, nil
	}

	var stdout bytes.Buffer
	if err := runGenerate(context.Background(), dependencies, root, &stdout, io.Discard); err == nil {
		t.Fatal("runGenerate succeeded writing the realtime types over a directory")
	}
	if got, want := stdout.String(), "Generated the client accessor for 1 client-accessible variable\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}
