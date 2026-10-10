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

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

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

func TestGenerateSurfacesADiscoveryFailure(t *testing.T) {
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
	root := setUpGenerateFixture(t, generateSoloConfig, "")
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
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

	var stdout, stderr bytes.Buffer
	if err := runGenerate(context.Background(), dependencies, root, &stdout, &stderr); err != nil {
		t.Fatalf("runGenerate err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var got resultv1.GenerateResult
	clitest.DecodeResultInto(t, stdout.String(), &got)
	if want := []string{filepath.Join(root, "ocel-realtime.d.ts")}; !slices.Equal(got.GetFiles(), want) {
		t.Errorf("files = %v, want %v", got.GetFiles(), want)
	}
	for _, file := range got.GetFiles() {
		if _, err := os.Stat(file); err != nil {
			t.Errorf("file %s is reported written but %v", file, err)
		}
	}
}

func TestGenerateWritesNothingForAProjectWithoutRealtime(t *testing.T) {
	root := setUpGenerateFixture(t, generateSoloConfig, "{}\n")
	dependencies := newTestDependencies()
	dependencies.CollectDeclarations = func(context.Context, *project.Project, *variables.Declarations, io.Writer, io.Writer) ([]declaration.Resource, error) {
		return nil, nil
	}

	var stdout bytes.Buffer
	if err := runGenerate(context.Background(), dependencies, root, &stdout, io.Discard); err != nil {
		t.Fatalf("runGenerate err = %v", err)
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing said when nothing was generated", stdout.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "ocel-realtime") {
			t.Errorf("runGenerate wrote %s for a project that declares no realtime resource", entry.Name())
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "tsconfig.json")); string(got) != "{}\n" {
		t.Errorf("tsconfig.json = %q, want it untouched: ocel never writes a file the user owns", got)
	}
}
