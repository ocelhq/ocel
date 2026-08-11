package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestLongSlugsStayDistinct(t *testing.T) {
	const shared = "my-very-long-project-name-that-runs-past-the-old-limit"

	a, b := shared+"-alpha", shared+"-beta"

	if one, two := naming.Sanitize(a), naming.Sanitize(b); one == two {
		t.Fatalf("two slugs collapsed to the index project %q — teardown scopes on this value and would plan across projects", one)
	}
	if one, two := naming.StateBackendURL("state", a), naming.StateBackendURL("state", b); one == two {
		t.Fatalf("two slugs share the state subpath %q", one)
	}
}
