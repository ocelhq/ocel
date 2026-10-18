package seal_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/seal"
)

func decode(t *testing.T, rendered []byte) []string {
	t.Helper()
	var elements []string
	rest := string(rendered)
	for rest != "" {
		length, after, found := strings.Cut(rest, ":")
		n, err := strconv.Atoi(length)
		if !found || err != nil || n < 0 || len(after) < n+1 || after[n] != ',' {
			t.Fatalf("%q is not a run of length-prefixed elements", rendered)
		}
		elements = append(elements, after[:n])
		rest = after[n+1:]
	}
	return elements
}

func TestABindingRendersItsPurposeProjectAndValuesEachPrefixedWithItsLength(t *testing.T) {
	t.Parallel()

	bound := seal.BindToProject("shop", "variable", "production", "*", "/a,b", "", "KEY")
	if want := "8:variable,4:shop,10:production,1:*,4:/a,b,0:,3:KEY,"; string(bound.Bytes()) != want {
		t.Errorf("Bytes() = %q, want %q", bound.Bytes(), want)
	}
}

func FuzzABindingDecodesBackToThePurposeProjectAndValuesItWasMadeFrom(f *testing.F) {
	f.Add("variable", "shop", "production", "*")
	f.Add("ab", "", "c", "")
	f.Add("a", "", "bc", "")
	f.Add("1:a,", "", "", "")
	f.Add("", "", "", "")
	f.Add("gates", "", "3:abc,", ":")
	f.Fuzz(func(t *testing.T, purpose, project, first, second string) {
		for _, values := range [][]string{nil, {first}, {first, second}} {
			bound := seal.BindToProject(project, purpose, values...)
			if got, want := decode(t, bound.Bytes()), append([]string{purpose, project}, values...); !slices.Equal(got, want) {
				t.Fatalf("BindToProject(%q, %q, %q).Bytes() decodes to %q, so another binding could render alike", project, purpose, values, got)
			}
		}
	})
}

func TestTwoBindingsThatDifferInPurposeProjectOrAnyValueNeverRenderAlike(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]seal.AssociatedData{
		{seal.Bind("ab", "c"), seal.Bind("a", "bc")},
		{seal.Bind("gates"), seal.Bind("gates", "")},
		{seal.Bind("a", "b", ""), seal.Bind("a", "b")},
		{seal.Bind("a", "b,1:c"), seal.Bind("a", "b", "c")},
		{seal.Bind("variable", "shop"), seal.BindToProject("shop", "variable")},
		{seal.BindToProject("shop", "variable", "production"), seal.BindToProject("shop", "release secrets", "production")},
		{seal.BindToProject("shop", "variable"), seal.BindToProject("shap", "variable")},
		{seal.Bind("alb gate", "production", "shop"), seal.Bind("alb gate publication", "production")},
	} {
		if string(pair[0].Bytes()) == string(pair[1].Bytes()) {
			t.Errorf("%q renders the same for two bindings, so a value sealed under one opens under the other", pair[0].Bytes())
		}
	}
}

func TestABindingNamesItsProjectOnlyWhenMadeForOne(t *testing.T) {
	t.Parallel()

	if project, owned := seal.BindToProject("shop", "variable").Project(); !owned || project != "shop" {
		t.Errorf("BindToProject(shop).Project() = %q, %v, want shop, true", project, owned)
	}
	if project, owned := seal.Bind("gates").Project(); owned {
		t.Errorf("Bind(gates).Project() = %q, true, and a tier-wide binding names no project", project)
	}
}
