package records_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestTwoCoordinatesNeverBindToTheSameBytes(t *testing.T) {
	t.Parallel()

	scope := records.SealScope{Project: "shop", Class: edge.ClassProduction, Env: "*", Folder: "/a%2Fb", Binding: "", Name: "KEY"}
	beside := records.SealScope{Project: "shop", Class: edge.ClassProduction, Env: "*", Folder: "/a/b", Binding: "", Name: "KEY"}

	if string(scope.AAD()) == string(beside.AAD()) {
		t.Fatalf("%s and %s bind to the same bytes, so a value sealed at one opens at the other",
			scope.Folder, beside.Folder)
	}
	if want := "shop/production/*/%2Fa%252Fb//KEY/"; string(scope.AAD()) != want {
		t.Errorf("AAD() = %q, want %q", scope.AAD(), want)
	}
}

func TestWhatIsEscapedComesBackAsItWent(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "/", "%", "%2F", "/a/b", "100%/x"} {
		if back := records.Unescape(records.Escape(value)); back != value {
			t.Errorf("Unescape(Escape(%q)) = %q", value, back)
		}
	}
}
