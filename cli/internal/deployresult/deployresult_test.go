package deployresult

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWrite_WritesTheDocumentedShape(t *testing.T) {
	dir := t.TempDir()

	err := Write(dir, Result{
		ProjectID:   "proj_123",
		Environment: Environment{Class: "preview", Identity: "e2e-42"},
		PromotionID: "dep_abc",
		Tag:         "v1",
		AppURLs:     []string{"https://app.example.com"},
		Apps:        []App{{Name: "web", BuildID: "bld_1"}},
		DeployedAt:  time.Date(2026, 7, 25, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatalf("read result file: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("result file is not valid JSON: %v", err)
	}

	want := map[string]any{
		"schemaVersion": float64(SchemaVersion),
		"projectId":     "proj_123",
		"environment":   map[string]any{"class": "preview", "identity": "e2e-42"},
		"promotionId":   "dep_abc",
		"tag":           "v1",
		"appUrls":       []any{"https://app.example.com"},
		"apps":          []any{map[string]any{"name": "web", "buildId": "bld_1"}},
		"deployedAt":    "2026-07-25T10:30:00Z",
	}
	for key, wantVal := range want {
		if gotVal, ok := got[key]; !ok {
			t.Errorf("result file is missing %q; got %v", key, got)
		} else if !jsonEqual(gotVal, wantVal) {
			t.Errorf("%s = %#v, want %#v", key, gotVal, wantVal)
		}
	}
	if len(got) != len(want) {
		t.Errorf("result file keys = %v, want exactly %v", keys(got), keys(want))
	}
}

func TestWrite_OmitsAnUnsetTagAndStampsTheTime(t *testing.T) {
	dir := t.TempDir()

	before := time.Now().Add(-time.Second)
	if err := Write(dir, Result{ProjectID: "proj_123"}); err != nil {
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
}

func TestWrite_OverwritesAnEarlierRunsResult(t *testing.T) {
	dir := t.TempDir()

	if err := Write(dir, Result{PromotionID: "first", Tag: "old"}); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	if err := Write(dir, Result{PromotionID: "second"}); err != nil {
		t.Fatalf("second Write() error = %v", err)
	}

	var got Result
	readInto(t, Path(dir), &got)
	if got.PromotionID != "second" {
		t.Errorf("promotionId = %q, want the latest run's", got.PromotionID)
	}
	if got.Tag != "" {
		t.Errorf("tag = %q, want the earlier run's value gone", got.Tag)
	}
}

func TestClear_RemovesAStaleResultAndToleratesAbsence(t *testing.T) {
	dir := t.TempDir()

	if err := Clear(dir); err != nil {
		t.Fatalf("Clear() on a project with no result error = %v", err)
	}
	if err := Write(dir, Result{PromotionID: "stale"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := Clear(dir); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if _, err := os.Stat(Path(dir)); !os.IsNotExist(err) {
		t.Errorf("stat after Clear() = %v, want the file removed", err)
	}
}

func TestPath_IsUnderTheProjectScratchDir(t *testing.T) {
	if got, want := Path("/p"), filepath.Join("/p", ".ocel", "deploy-result.json"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
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

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
