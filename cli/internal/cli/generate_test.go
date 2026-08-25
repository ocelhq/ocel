package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func declaring(sess *session.Session, definitions ...*resourcesv1.VariableDefinition) {
	sess.CollectDeclarations = func(ctx context.Context, _ *projectconfig.Config, gate *envgate.Gate, _, _ io.Writer) ([]declare.Resource, error) {
		if _, err := gate.DeclareEnv(ctx, &resourcesv1.DeclareEnvRequest{Definitions: definitions}); err != nil {
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
	if tsconfig != "" {
		clitest.WriteFile(t, filepath.Join(root, "tsconfig.json"), tsconfig)
	}
	return root
}

const generateSoloConfig = `
export default { slug: "test-app" };
`

func TestRunGenerate(t *testing.T) {
	t.Run("writes the accessor without a login or a provider", func(t *testing.T) {
		root := setUpGenerateFixture(t, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: ".", framework: "next" }],
};
`, "{\n  \"compilerOptions\": {}\n}\n")

		sess := newSession()
		declaring(&sess, plainClient("NEXT_PUBLIC_SITE_URL"), plainClient("NEXT_PUBLIC_APP_ID"))

		var stdout, stderr bytes.Buffer
		if err := runGenerate(context.Background(), sess, root, &stdout, &stderr); err != nil {
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
	})

	t.Run("generates for declarations no value backs", func(t *testing.T) {
		root := setUpGenerateFixture(t, generateSoloConfig, "{}\n")

		sess := newSession()
		declaring(&sess, plainClient("NEXT_PUBLIC_SITE_URL"))

		var stdout, stderr bytes.Buffer
		if err := runGenerate(context.Background(), sess, root, &stdout, &stderr); err != nil {
			t.Fatalf("runGenerate refused a declaration nothing has a value for: %v", err)
		}
		if _, err := os.ReadFile(filepath.Join(root, ".ocel", "env-client.ts")); err != nil {
			t.Fatalf("runGenerate wrote no accessor: %v", err)
		}
	})

	t.Run("names only client-accessible plaintext", func(t *testing.T) {
		root := setUpGenerateFixture(t, generateSoloConfig, "{}\n")

		sess := newSession()
		declaring(&sess,
			plainClient("NEXT_PUBLIC_SITE_URL"),
			&resourcesv1.VariableDefinition{Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
			&resourcesv1.VariableDefinition{Key: "API_TOKEN", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, ClientAccessible: true},
			&resourcesv1.VariableDefinition{Key: "SIGNING_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, ClientAccessible: true},
		)

		var stdout, stderr bytes.Buffer
		if err := runGenerate(context.Background(), sess, root, &stdout, &stderr); err != nil {
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
	})

	t.Run("writes nothing for a project with no client-accessible variable", func(t *testing.T) {
		root := setUpGenerateFixture(t, generateSoloConfig, "{}\n")

		sess := newSession()
		declaring(&sess, &resourcesv1.VariableDefinition{Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN})

		var stdout, stderr bytes.Buffer
		if err := runGenerate(context.Background(), sess, root, &stdout, &stderr); err != nil {
			t.Fatalf("runGenerate: %v", err)
		}

		if _, err := os.Stat(filepath.Join(root, ".ocel", "env-client.ts")); !errors.Is(err, fs.ErrNotExist) {
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
	})

	t.Run("surfaces a discovery failure", func(t *testing.T) {
		root := setUpGenerateFixture(t, generateSoloConfig, "")

		sess := newSession()
		sess.CollectDeclarations = func(context.Context, *projectconfig.Config, *envgate.Gate, io.Writer, io.Writer) ([]declare.Resource, error) {
			return nil, errors.New("discovery blew up")
		}

		var stdout, stderr bytes.Buffer
		err := runGenerate(context.Background(), sess, root, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "discovery blew up") {
			t.Fatalf("runGenerate = %v, want the discovery failure", err)
		}
	})
}
