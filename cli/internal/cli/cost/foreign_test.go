package cost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/runui"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

func foreignDeps(t *testing.T, byCommand map[string]string) (string, cmddeps.Deps) {
	t.Helper()
	dir := t.TempDir()
	deps := clitest.NewDeps()
	deps.RunTool = func(_ context.Context, _ string, argv []string, _ io.Writer) ([]byte, error) {
		for prefix, file := range byCommand {
			if strings.HasPrefix(strings.Join(argv, " "), prefix) {
				if !strings.HasSuffix(file, ".json") {
					return []byte(file), nil
				}
				raw, err := os.ReadFile(filepath.Join("testdata", file))
				if err != nil {
					t.Fatal(err)
				}
				return raw, nil
			}
		}
		t.Fatalf("no fake output for %v", argv)
		return nil, nil
	}
	return dir, deps
}

func TestTheSourceIsWhatTheDirectoryHolds(t *testing.T) {
	for _, tc := range []struct {
		file string
		want string
	}{
		{file: "ocel.json", want: "ocel"},
		{file: "sst.config.ts", want: "sst"},
		{file: "Pulumi.yaml", want: "pulumi"},
	} {
		dir := t.TempDir()
		clitest.WriteFile(t, filepath.Join(dir, tc.file), "{}")

		got, err := sourceIn(dir, "")

		if err != nil || got != tc.want {
			t.Errorf("sourceIn(a directory holding %s) = %q, %v, want %q", tc.file, got, err, tc.want)
		}
	}

	_, err := sourceIn(t.TempDir(), "")
	if err == nil {
		t.Fatal("sourceIn(an empty directory) = nil, want a refusal")
	}
	for _, want := range []string{"ocel", "sst", "pulumi"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %v does not name %q", err, want)
		}
	}
}

func TestAStackOrStageIsTheEnvironmentSoEnvIsRefused(t *testing.T) {
	dir, deps := foreignDeps(t, nil)
	clitest.WriteFile(t, filepath.Join(dir, "Pulumi.yaml"), "name: shop\n")

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), deps, dir, Options{Env: "preview"}, &stdout, &stderr)

	if err == nil || !strings.Contains(err.Error(), "stacks and stages are the environment") {
		t.Errorf("Run(--env preview) = %v, want the refusal", err)
	}
}

func TestAPulumiPreviewIsPricedUnderTheComponentsItStandsUp(t *testing.T) {
	dir, deps := foreignDeps(t, map[string]string{
		"pulumi stack --show-name": "dev\n",
		"pulumi preview":           "pulumi_preview.json",
	})
	clitest.WriteFile(t, filepath.Join(dir, "Pulumi.yaml"), "name: shop\n")

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), deps, dir, Options{PricingURL: clitest.ServeCostService(t)}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() = %v; stderr=%s", err, stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{"pulumi stack dev", "component api", "handler", "main", "fake_postgres", "14.60"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestAnSSTStageIsTheStateTheDiffIsTakenAgainst(t *testing.T) {
	dir, deps := foreignDeps(t, map[string]string{
		"sst state export": "sst_state.json",
		"sst diff":         "sst_diff.json",
	})
	clitest.WriteFile(t, filepath.Join(dir, "sst.config.ts"), "export default {}\n")
	clitest.WriteFile(t, filepath.Join(dir, ".sst", "stage"), "victor\n")
	deps.Presentation = func(io.Writer) runui.Presentation {
		return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), deps, dir, Options{PricingURL: clitest.ServeCostService(t)}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() = %v; stderr=%s", err, stderr.String())
	}

	var envelope scanJSON
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout.String())
	}
	var set costv1.ResourceSet
	if err := protojson.Unmarshal(envelope.Resources, &set); err != nil {
		t.Fatal(err)
	}
	if set.GetSource() != "sst" {
		t.Errorf("source = %q, want sst", set.GetSource())
	}
	var types []string
	for _, held := range set.GetResources() {
		types = append(types, held.GetType())
	}
	if len(types) != 2 || !strings.Contains(strings.Join(types, " "), "fake_postgres") {
		t.Errorf("types = %v, want the state's function and the postgres the diff creates, without the bucket it deletes", types)
	}
}

func TestAToolThatIsNotInstalledIsSaidSoRatherThanShrugged(t *testing.T) {
	dir := t.TempDir()
	deps := clitest.NewDeps()
	deps.RunTool = func(context.Context, string, []string, io.Writer) ([]byte, error) {
		return nil, exec.ErrNotFound
	}
	clitest.WriteFile(t, filepath.Join(dir, "Pulumi.yaml"), "name: shop\n")

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), deps, dir, Options{}, &stdout, &stderr)

	if err == nil || !strings.Contains(err.Error(), "pulumi") || !strings.Contains(err.Error(), "--from") {
		t.Errorf("Run() = %v, want a refusal naming the tool and the way past it", err)
	}
}

func TestAFileStandsInForRunningTheToolAtAll(t *testing.T) {
	dir, deps := foreignDeps(t, nil)
	clitest.WriteFile(t, filepath.Join(dir, "Pulumi.yaml"), "name: shop\n")

	var stdout, stderr bytes.Buffer
	opts := Options{From: filepath.Join("testdata", "pulumi_preview.json"), PricingURL: clitest.ServeCostService(t)}
	if err := Run(context.Background(), deps, dir, opts, &stdout, &stderr); err != nil {
		t.Fatalf("Run() = %v; stderr=%s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "pulumi stack dev") {
		t.Errorf("stdout = %s, want the stack the preview was taken from", stdout.String())
	}
}
