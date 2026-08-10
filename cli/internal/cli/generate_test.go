package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func declaring(t *testing.T, definitions ...*resourcesv1.VariableDefinition) {
	t.Helper()
	prev := collectDeclarations
	collectDeclarations = func(ctx context.Context, _ *projectconfig.Config, gate *envgate.Gate, _, _ io.Writer) ([]declare.Resource, error) {
		if _, err := gate.DeclareEnv(ctx, &resourcesv1.DeclareEnvRequest{Definitions: definitions}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	t.Cleanup(func() { collectDeclarations = prev })
}

func plainClient(key string) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{
		Key:              key,
		Class:            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		ClientAccessible: true,
		Required:         true,
	}
}

func TestRunGenerate_WritesAccessorWithoutLoginOrProvider(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: ".", framework: "next" }],
};
`)
	writeFile(t, filepath.Join(root, "tsconfig.json"), "{\n  \"compilerOptions\": {}\n}\n")

	declaring(t, plainClient("NEXT_PUBLIC_SITE_URL"), plainClient("NEXT_PUBLIC_APP_ID"))

	var stdout, stderr bytes.Buffer
	if err := runGenerate(context.Background(), root, &stdout, &stderr); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	accessor, err := os.ReadFile(filepath.Join(root, ".ocel", "env-client.ts"))
	if err != nil {
		t.Fatalf("runGenerate wrote no accessor: %v", err)
	}
	for _, want := range []string{
		"NEXT_PUBLIC_APP_ID: inlined(\"NEXT_PUBLIC_APP_ID\", process.env.NEXT_PUBLIC_APP_ID)",
		"NEXT_PUBLIC_SITE_URL: inlined(\"NEXT_PUBLIC_SITE_URL\", process.env.NEXT_PUBLIC_SITE_URL)",
	} {
		if !strings.Contains(string(accessor), want) {
			t.Errorf("accessor = %s, want it to name %q", accessor, want)
		}
	}

	tsconfig, err := os.ReadFile(filepath.Join(root, "tsconfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tsconfig), ".ocel/env-client.ts") {
		t.Errorf("tsconfig.json = %s, want it to map 'ocel/env/client' at the accessor", tsconfig)
	}

	if got, want := stdout.String(), "Generated the client accessor for 2 client-accessible variables\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestRunGenerate_GeneratesForDeclarationsNoValueBacks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
	writeFile(t, filepath.Join(root, "tsconfig.json"), "{}\n")

	declaring(t, plainClient("NEXT_PUBLIC_SITE_URL"))

	var stdout, stderr bytes.Buffer
	if err := runGenerate(context.Background(), root, &stdout, &stderr); err != nil {
		t.Fatalf("runGenerate refused a declaration nothing has a value for: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(root, ".ocel", "env-client.ts")); err != nil {
		t.Fatalf("runGenerate wrote no accessor: %v", err)
	}
}

func TestRunGenerate_NamesOnlyClientAccessiblePlaintext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
	writeFile(t, filepath.Join(root, "tsconfig.json"), "{}\n")

	declaring(t,
		plainClient("NEXT_PUBLIC_SITE_URL"),
		&resourcesv1.VariableDefinition{Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
		&resourcesv1.VariableDefinition{Key: "API_TOKEN", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, ClientAccessible: true},
		&resourcesv1.VariableDefinition{Key: "SIGNING_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, ClientAccessible: true},
	)

	var stdout, stderr bytes.Buffer
	if err := runGenerate(context.Background(), root, &stdout, &stderr); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	accessor, err := os.ReadFile(filepath.Join(root, ".ocel", "env-client.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"DATABASE_URL", "API_TOKEN", "SIGNING_KEY"} {
		if strings.Contains(string(accessor), unwanted) {
			t.Errorf("accessor = %s, want it not to name %q", accessor, unwanted)
		}
	}
}

func TestRunGenerate_NoClientVariables(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
	writeFile(t, filepath.Join(root, "tsconfig.json"), "{}\n")

	declaring(t, &resourcesv1.VariableDefinition{Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN})

	var stdout, stderr bytes.Buffer
	if err := runGenerate(context.Background(), root, &stdout, &stderr); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, ".ocel", "env-client.ts")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat accessor = %v, want it not to exist for a project with no client-accessible variable", err)
	}
	tsconfig, err := os.ReadFile(filepath.Join(root, "tsconfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(tsconfig); got != "{}\n" {
		t.Errorf("tsconfig.json = %q, want it untouched", got)
	}
	if got, want := stdout.String(), "No client-accessible variables declared; nothing to generate\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestRunGenerate_DiscoveryFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

	prev := collectDeclarations
	collectDeclarations = func(context.Context, *projectconfig.Config, *envgate.Gate, io.Writer, io.Writer) ([]declare.Resource, error) {
		return nil, errors.New("discovery blew up")
	}
	t.Cleanup(func() { collectDeclarations = prev })

	var stdout, stderr bytes.Buffer
	err := runGenerate(context.Background(), root, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "discovery blew up") {
		t.Fatalf("runGenerate = %v, want the discovery failure", err)
	}
}
