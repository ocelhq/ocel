package deployrecord

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/statedir"
)

func TestWriteLeavesTheDocumentedRecordInTheProjectStateDir(t *testing.T) {
	t.Parallel()

	t.Run("writes the shape the preview app reads", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		err := Write(dir, Record{
			Slug:        "proj-123",
			Environment: Environment{Tier: "preview", Identity: "e2e-42"},
			Provider:    Provider{Name: "aws"},
			PromotionID: "dep_abc",
			Tag:         "v1",
			Apps:        []App{{Name: "web", BuildID: "bld_1", DeploymentID: "3f7c1b9a5e2d4c8f", URLs: []string{"https://app.example.com"}}},
			DeployedAt:  time.Date(2026, 7, 25, 10, 30, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("Write() error = %v", err)
		}

		got, err := os.ReadFile(Path(dir))
		if err != nil {
			t.Fatalf("read result file: %v", err)
		}
		want, err := os.ReadFile(filepath.Join("testdata", "deploy-result.json"))
		if err != nil {
			t.Fatalf("read golden: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("result file =\n%s\nwant testdata/deploy-result.json =\n%s", got, want)
		}
	})

	t.Run("omits an unset tag and stamps the time", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		before := time.Now().Add(-time.Second)
		if err := Write(dir, Record{Slug: "proj-123"}); err != nil {
			t.Fatalf("Write() error = %v", err)
		}

		var got struct {
			Tag        *string   `json:"tag"`
			DeployedAt time.Time `json:"deployedAt"`
		}
		readInto(t, Path(dir), &got)
		if got.Tag != nil {
			t.Errorf("tag = %v, want it omitted when unset", *got.Tag)
		}
		if got.DeployedAt.Before(before) {
			t.Errorf("deployedAt = %v, want it stamped with the current time", got.DeployedAt)
		}
	})

	t.Run("overwrites an earlier run's result", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		if err := Write(dir, Record{PromotionID: "first", Tag: "old"}); err != nil {
			t.Fatalf("first Write() error = %v", err)
		}
		if err := Write(dir, Record{PromotionID: "second"}); err != nil {
			t.Fatalf("second Write() error = %v", err)
		}

		var got Record
		readInto(t, Path(dir), &got)
		if got.PromotionID != "second" {
			t.Errorf("promotionId = %q, want the latest run's", got.PromotionID)
		}
		if got.Tag != "" {
			t.Errorf("tag = %q, want the earlier run's value gone", got.Tag)
		}
	})
}

func TestClearRemovesAStaleRecordAndToleratesNone(t *testing.T) {
	t.Parallel()

	t.Run("tolerates the absence of a result", func(t *testing.T) {
		t.Parallel()
		if err := Clear(t.TempDir()); err != nil {
			t.Fatalf("Clear() on a project with no result error = %v", err)
		}
	})

	t.Run("removes a stale result", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		if err := Write(dir, Record{PromotionID: "stale"}); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if err := Clear(dir); err != nil {
			t.Fatalf("Clear() error = %v", err)
		}
		if _, err := os.Stat(Path(dir)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat after Clear() = %v, want the file removed", err)
		}
	})
}

func TestPathIsUnderTheProjectStateDir(t *testing.T) {
	t.Parallel()

	t.Run("is under the project scratch dir", func(t *testing.T) {
		t.Parallel()
		if got, want := Path("/p"), filepath.Join("/p", statedir.Name, "deploy-result.json"); got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
	})
}

func readInto(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
}
