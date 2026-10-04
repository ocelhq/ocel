package docsurl_test

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/docsurl"
)

func TestTheSchemaAndErrorPagesAreServedFromTheOcelDocsSite(t *testing.T) {
	if got := docsurl.FormatSchema("1.2.3"); got != "https://ocel.dev/schema/1.2.3/ocel.schema.json" {
		t.Errorf("schema URL = %q, want the versioned schema on the docs site", got)
	}
	if got := docsurl.FormatErrorPage("project.no_config"); got != "https://ocel.dev/docs/errors/project.no_config" {
		t.Errorf("error page = %q, want the code's page under /docs/errors", got)
	}
}
