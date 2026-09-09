package providerkit_test

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestRevisionsAreKeyedToTheAppTheRecordIsFor(t *testing.T) {
	t.Parallel()

	result := providerkit.StackResult{
		Containers: []providerkit.AppContainer{
			{Name: "web", Physical: "ocel-shop-web", Revision: "ocel-shop-web-00001"},
			{Name: "admin", Physical: "ocel-shop-admin", Revision: "ocel-shop-admin-00001"},
		},
		Functions: []providerkit.Function{
			{Name: "web-server", Physical: "ocel-shop-web-server", Revision: "ocel-shop-web-server-00001"},
			{Name: "admin-server", Physical: "ocel-shop-admin-server", Revision: "ocel-shop-admin-server-00001"},
		},
	}

	got := providerkit.RevisionsOf(result, "web", []string{"web-server"})
	want := map[string]string{
		"ocel-shop-web":        "ocel-shop-web-00001",
		"ocel-shop-web-server": "ocel-shop-web-server-00001",
	}
	if !maps.Equal(got, want) {
		t.Errorf("RevisionsOf(web) = %v, want %v: a promotion pins every revision the record names, so another app's in it re-pins that app too", got, want)
	}
}

func TestAnAppThatStoodUpNoRevisionRecordsNone(t *testing.T) {
	t.Parallel()

	result := providerkit.StackResult{
		Containers: []providerkit.AppContainer{{Name: "web", Physical: "ocel-shop-web"}},
	}
	if got := providerkit.RevisionsOf(result, "web", nil); got != nil {
		t.Errorf("RevisionsOf(web) = %v, want nothing: a promotion refuses a record naming no revision rather than pinning an empty one", got)
	}
}
