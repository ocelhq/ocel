package seal_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/seal"
)

func TestAssociatedDataRendersEachValueEscapedInOrder(t *testing.T) {
	t.Parallel()

	bound := seal.AssociatedData{
		{Name: "project", Value: "shop"},
		{Name: "class", Value: "production"},
		{Name: "environment", Value: "*"},
		{Name: "folder", Value: "/a%2Fb"},
		{Name: "binding", Value: ""},
		{Name: "key", Value: "KEY"},
	}
	if want := "shop/production/*/%2Fa%252Fb//KEY/"; string(bound.Bytes()) != want {
		t.Errorf("Bytes() = %q, want %q", bound.Bytes(), want)
	}
}

func TestTwoValuesThatDifferOnlyInEscapingNeverRenderAlike(t *testing.T) {
	t.Parallel()

	escaped := seal.AssociatedData{{Name: "folder", Value: "/a%2Fb"}, {Name: "key", Value: "KEY"}}
	plain := seal.AssociatedData{{Name: "folder", Value: "/a/b"}, {Name: "key", Value: "KEY"}}
	if string(escaped.Bytes()) == string(plain.Bytes()) {
		t.Fatalf("%q and %q render to the same bytes, so a value sealed at one opens at the other",
			escaped[0].Value, plain[0].Value)
	}
}
