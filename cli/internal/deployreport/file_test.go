package deployreport

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func TestWriteLeavesTheDeploymentAsProtoJSONInTheProjectStateDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	deployment := productionAttempt().Succeeded(finishedAt)

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

func TestWriteReplacesTheReportOfAnEarlierDeployment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := productionAttempt()
	first.PromotionID = "p_first"
	second := productionAttempt()
	second.PromotionID = "p_second"
	for _, attempt := range []Attempt{first, second} {
		if err := Write(dir, attempt.Succeeded(finishedAt)); err != nil {
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

func TestClearRemovesTheReportAndIgnoresAMissingOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Write(dir, productionAttempt().Succeeded(finishedAt)); err != nil {
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
