package images_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
)

func TestTheDaemonStoreRemovesAnImageByTheCoordinateItWasWrittenUnder(t *testing.T) {
	var asked string
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.Path
		w.Write([]byte(`[{"Untagged":"ocel/shop/web:sha256-abc"}]`))
	})

	if err := images.DaemonStore().Remove(context.Background(), "ocel/shop/web:sha256-abc"); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if asked != "DELETE /images/ocel/shop/web:sha256-abc" {
		t.Errorf("Remove() asked the daemon %q, want a delete of the coordinate", asked)
	}
}

func TestTheDaemonStoreRemovingAnImageItDoesNotHaveIsDone(t *testing.T) {
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
	})

	if err := images.DaemonStore().Remove(context.Background(), "ocel/shop/web:sha256-abc"); err != nil {
		t.Errorf("Remove() = %v, want nothing for an image that is already gone", err)
	}
}

func TestTheDaemonStoreSaysWhyItKeptAnImageARunningContainerHolds(t *testing.T) {
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"conflict: unable to remove repository reference: container abc is using its referenced image"}`, http.StatusConflict)
	})

	err := images.DaemonStore().Remove(context.Background(), "ocel/shop/web:sha256-abc")
	if err == nil {
		t.Fatal("Remove() = nil for an image a container still runs")
	}
}
