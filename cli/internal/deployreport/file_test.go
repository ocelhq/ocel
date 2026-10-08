package deployreport

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/project"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func TestWriteLeavesTheDeploymentAsProtoJSONInTheProjectStateDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	deployment := productionDeployment()

	if err := Write(dir, deployment); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, statedir.Name, "deploy-report.json"))
	if err != nil {
		t.Fatalf("read the report: %v", err)
	}
	var read consolev1.Deployment
	if err := protojson.Unmarshal(raw, &read); err != nil {
		t.Fatalf("the report is not the deployment's protojson: %v\n%s", err, raw)
	}
	if !proto.Equal(&read, deployment) {
		t.Errorf("report = %v, want %v", &read, deployment)
	}
}

func TestTheGoldenReportIsADeploymentTheConsoleAccepts(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "deploy-report.json"))
	if err != nil {
		t.Fatal(err)
	}

	var golden consolev1.Deployment
	if err := protojson.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("the golden is not a deployment's protojson: %v", err)
	}

	if err := protovalidate.Validate(&golden); err != nil {
		t.Errorf("the golden is a record the console refuses: %v", err)
	}
}

func TestWriteReplacesTheReportOfAnEarlierDeployment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, promotion := range []string{"p_first", "p_second"} {
		if err := Write(dir, productionAttempt().Succeeded(finishedAt, liveApps(), &consolev1.Promotion{Id: promotion, Seq: 1})); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	var read consolev1.Deployment
	if err := protojson.Unmarshal(raw, &read); err != nil {
		t.Fatal(err)
	}
	if read.GetPromotion().GetId() != "p_second" {
		t.Errorf("promotion = %q, want the later deployment's p_second", read.GetPromotion().GetId())
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, statedir.Name, "deploy-report.json.*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestASucceededDeploymentWhoseReportCannotBeWrittenIsStillTheSucceededRecord(t *testing.T) {
	t.Parallel()
	notADirectory := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADirectory, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	attempt := productionAttempt()
	attempt.Project = &project.Project{Dir: notADirectory}

	deployment, err := WriteSucceeded(&attempt, liveApps(), livePromotion(), nil)

	if err == nil || !strings.Contains(err.Error(), "p_01 is live") {
		t.Errorf("WriteSucceeded() error = %v, want one saying promotion p_01 is live", err)
	}
	if deployment.GetOutcome() != consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED || deployment.GetPromotion().GetId() != "p_01" {
		t.Errorf("deployment = %v, want the succeeded record of p_01", deployment)
	}
}

func TestClearRemovesTheReportAndIgnoresAMissingOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Write(dir, productionDeployment()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := Clear(dir); err != nil {
			t.Fatalf("Clear() error = %v", err)
		}
	}

	if _, err := os.Stat(Path(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat report = %v, want it gone", err)
	}
}
