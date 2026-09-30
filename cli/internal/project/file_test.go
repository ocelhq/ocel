package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/prerequisite"
)

func TestAMissingConfigIsAPrerequisiteInitSetsUp(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(context.Background(), dir, "")
	var missing prerequisite.MissingError
	if !errors.As(err, &missing) || missing.Missing() != prerequisite.Project {
		t.Fatalf("error = %v, want the project reported missing", err)
	}
	if !strings.HasSuffix(err.Error(), "Run `ocel init` and try again") {
		t.Errorf("error = %q, want it to end naming the command that sets the project up", err)
	}
	if finding := missing.Finding(); !strings.Contains(finding, dir) || !strings.Contains(finding, DefaultFileName) {
		t.Errorf("finding = %q, want it to name the directory and the configs looked for", finding)
	}
	if missing.Remedy() != "`ocel init`" {
		t.Errorf("remedy = %q, want `ocel init`", missing.Remedy())
	}
}
