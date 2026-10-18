package tier

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestRemovingAPartSaysWhichDeployOrCommandAddedIt(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		recorded stackrecords.BootstrapPart
		want     string
	}{
		{stackrecords.BootstrapPart{AddedWith: "ocel domain use --preview"}, "added by `ocel domain use --preview` on first use; it goes"},
		{stackrecords.BootstrapPart{AddedBy: "shop"}, "added by shop on first use; it goes"},
		{stackrecords.BootstrapPart{AddedBy: "shop", AddedWith: "ocel bootstrap"}, "added by `ocel bootstrap` for shop on first use; it goes"},
	} {
		if got := addedDeletion("cloudfront", "it goes", c.recorded).Reason; got != c.want {
			t.Errorf("reason for %+v = %q, want %q", c.recorded, got, c.want)
		}
	}
}
