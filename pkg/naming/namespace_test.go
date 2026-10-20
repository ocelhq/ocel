package naming_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
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

func TestAppFenceIsTheNamespaceFieldThenApp(t *testing.T) {
	for namespace, want := range map[string]string{
		"ocel":                           "ocel-app-",
		"acme-staging-eu-west-blue-team": "acme-20ddf6-app-",
	} {
		if got := naming.AppFence(namespace); got != want {
			t.Errorf("AppFence(%q) = %q, want %q", namespace, got, want)
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

var updateNamespaceFields = flag.Bool("update", false, "rewrite the namespace field fixture the TypeScript harnesses read")

type namespaceFieldCase struct {
	Namespace string `json:"namespace"`
	Field     string `json:"field"`
}

func TestTheHarnessesNamespaceFieldIsTheOneGoFits(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "shared", "fixtures", "namespace-fields.json")
	namespaces := []string{
		"ocel",
		strings.Repeat("a", naming.MaxNamespaceField),
		strings.Repeat("a", naming.MaxNamespaceField+1),
		"j-1874-deploy-next-cloudflare",
		"j-1874-deploy-next-cloudfront",
		"ab-app-cdefghij",
		"x-app-namespaces",
		"abc--defghijklm",
		strings.Repeat("n", 44),
	}
	if *updateNamespaceFields {
		cases := make([]namespaceFieldCase, 0, len(namespaces))
		for _, namespace := range namespaces {
			cases = append(cases, namespaceFieldCase{Namespace: namespace, Field: naming.NamespaceField(namespace)})
		}
		encoded, err := json.MarshalIndent(cases, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []namespaceFieldCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != len(namespaces) {
		t.Fatalf("the fixture holds %d namespaces, want %d: rewrite it with go test ./pkg/naming -run TestTheHarnessesNamespaceFieldIsTheOneGoFits -update", len(cases), len(namespaces))
	}
	for _, c := range cases {
		if got := naming.NamespaceField(c.Namespace); got != c.Field {
			t.Errorf("NamespaceField(%q) = %q, but the harnesses read %q", c.Namespace, got, c.Field)
		}
	}
}
