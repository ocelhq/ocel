package records_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/records"
)

func TestWhatIsEscapedComesBackAsItWent(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "/", "%", "%2F", "/a/b", "100%/x"} {
		if back := records.Unescape(records.Escape(value)); back != value {
			t.Errorf("Unescape(Escape(%q)) = %q", value, back)
		}
	}
}
