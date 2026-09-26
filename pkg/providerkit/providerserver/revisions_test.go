package providerserver_test

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
)

func TestRevisionsAreKeyedToTheAppTheRecordIsFor(t *testing.T) {
	t.Parallel()

	result := provider.StackResult{
		Containers: []provider.AppContainer{
			{Name: "web", Physical: "ocel-shop-web", Revision: "ocel-shop-web-00001"},
			{Name: "admin", Physical: "ocel-shop-admin", Revision: "ocel-shop-admin-00001"},
		},
		Functions: []provider.Function{
			{Name: "web-server", Physical: "ocel-shop-web-server", Revision: "ocel-shop-web-server-00001"},
			{Name: "admin-server", Physical: "ocel-shop-admin-server", Revision: "ocel-shop-admin-server-00001"},
		},
	}

	got := providerserver.RevisionsOf(result, "web", []string{"web-server"})
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

	result := provider.StackResult{
		Containers: []provider.AppContainer{{Name: "web", Physical: "ocel-shop-web"}},
	}
	if got := providerserver.RevisionsOf(result, "web", nil); got != nil {
		t.Errorf("RevisionsOf(web) = %v, want nothing: a promotion refuses a record naming no revision rather than pinning an empty one", got)
	}
}
