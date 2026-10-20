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

func TestNamespaceFieldCompactsALongNamespaceToItsHeadAndAHash(t *testing.T) {
	for _, tc := range []struct{ namespace, want string }{
		{"j-1874-deploy-next-cloudflare", "j-187-da21fb"},
		{"acme-staging-eu-west-blue-team", "acme-20ddf6"},
	} {
		if got := naming.NamespaceField(tc.namespace); got != tc.want {
			t.Errorf("NamespaceField(%q) = %q, want %q", tc.namespace, got, tc.want)
		}
	}
}

func TestNamespaceFieldNeverCutsANamespaceIntoAnotherNamespacesFence(t *testing.T) {
	field := naming.NamespaceField("x-apple-orchard-namespace")
	if got, want := field, "x-ap-7e71aa"; got != want {
		t.Errorf("NamespaceField = %q, want %q", got, want)
	}
	if strings.HasPrefix(field+naming.WordSeparator, naming.AppFence("x")) {
		t.Errorf("NamespaceField = %q, so every name it mints starts with namespace %q's fence %q", field, "x", naming.AppFence("x"))
	}
}
