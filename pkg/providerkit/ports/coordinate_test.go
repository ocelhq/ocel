package ports_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

func TestTwoCoordinatesNeverBindToTheSameBytes(t *testing.T) {
	t.Parallel()

	held := ports.Coordinate{Project: "shop", Class: ports.ClassProduction, Env: "*", Folder: "/a%2Fb", Link: "", Name: "KEY"}
	beside := ports.Coordinate{Project: "shop", Class: ports.ClassProduction, Env: "*", Folder: "/a/b", Link: "", Name: "KEY"}

	if string(held.Binding()) == string(beside.Binding()) {
		t.Fatalf("%s and %s bind to the same bytes, so a value sealed at one opens at the other",
			held.Folder, beside.Folder)
	}
	if want := "shop/production/*/%2Fa%252Fb//KEY/"; string(held.Binding()) != want {
		t.Errorf("Binding() = %q, want %q", held.Binding(), want)
	}
}

func TestWhatIsEscapedComesBackAsItWent(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "/", "%", "%2F", "/a/b", "100%/x"} {
		if back := ports.Unescape(ports.Escape(value)); back != value {
			t.Errorf("Unescape(Escape(%q)) = %q", value, back)
		}
	}
}
