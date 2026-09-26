package naming_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestNamespaceFieldLeavesAShortNamespaceUnchanged(t *testing.T) {
	for _, given := range []string{"ocel", "shop", "a", strings.Repeat("a", naming.MaxNamespaceField)} {
		if got := naming.NamespaceField(given); got != given {
			t.Errorf("NamespaceField(%q) = %q, want the namespace itself", given, got)
		}
	}
}

func TestNamespaceFieldFitsALongNamespaceWithoutLosingTwoApart(t *testing.T) {
	one := naming.NamespaceField("j-1874-deploy-next-cloudflare")
	two := naming.NamespaceField("j-1874-deploy-next-cloudfront")
	for _, got := range []string{one, two} {
		if len(got) > naming.MaxNamespaceField {
			t.Errorf("NamespaceField = %q, which is %d characters and the field fits %d", got, len(got), naming.MaxNamespaceField)
		}
		if strings.Contains(got, naming.FieldSeparator) {
			t.Errorf("NamespaceField = %q, which contains the separator that divides fields", got)
		}
	}
	if one == two {
		t.Errorf("two namespaces both field as %q, so each would reach the other's workers", one)
	}
}
