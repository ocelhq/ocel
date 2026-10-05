package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

func TestAMissingConfigIsAPrerequisiteInitSetsUp(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(context.Background(), dir, "")
	var missing prerequisite.MissingError
	if !errors.As(err, &missing) || missing.Missing() != prerequisite.Project {
		t.Fatalf("error = %v, want the project reported missing", err)
	}
	if want := "Run `ocel init --provider <id>` and try again, where <id> is one of " + strings.Join(configdoc.ProviderIDs(), ", "); !strings.HasSuffix(err.Error(), want) {
		t.Errorf("error = %q, want it to end naming the command that sets the project up and the providers it takes", err)
	}
	if finding := missing.Finding(); !strings.Contains(finding, dir) || !strings.Contains(finding, DefaultFileName) {
		t.Errorf("finding = %q, want it to name the directory and the configs looked for", finding)
	}
	if missing.Remedy() != "`ocel init --provider <id>`" {
		t.Errorf("remedy = %q, want `ocel init --provider <id>`", missing.Remedy())
	}
	if missing.Hint() != "ocel init --provider <id>" {
		t.Errorf("hint = %q, want ocel init --provider <id> as typed", missing.Hint())
	}
}
