package docsurl_test

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/docsurl"
)

func TestTheSchemaAndErrorPagesAreServedFromTheOcelDocsSite(t *testing.T) {
	if docsurl.Schema != "https://ocel.dev/schema/ocel.schema.json" {
		t.Errorf("schema URL = %q, want the one schema on the docs site, whatever the version", docsurl.Schema)
	}
	if got := docsurl.FormatErrorPage("project.no_config"); got != "https://ocel.dev/docs/errors/project.no_config" {
		t.Errorf("error page = %q, want the code's page under /docs/errors", got)
	}
}
