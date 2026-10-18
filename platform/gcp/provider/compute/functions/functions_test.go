package functions

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAServerlessAppsWorkersRunFromTheImageOfTheFunctionThatCarriesTheWorkerEntry(t *testing.T) {
	t.Parallel()

	app := &provider.AppSpec{App: "web", Functions: []provider.FunctionSpec{
		{Name: "fn--web--a"},
		{Name: "fn--web--b", HasWorkerEntry: true},
	}}
	images := provider.ImagePushes{Pushes: []provider.ImagePush{
		{App: "fn--web--a", ImageRef: "europe-west1-docker.pkg.dev/acme/ocel/web-a@sha256:aaa", Function: true},
		{App: "fn--web--b", ImageRef: "europe-west1-docker.pkg.dev/acme/ocel/web-b@sha256:bbb", Function: true},
	}}
	if got, err := workerImageOf(app, images); err != nil || got != images.Pushes[1].ImageRef {
		t.Errorf("workerImageOf = %s, %v, want %s, the image of the function that carries the worker entry", got, err, images.Pushes[1].ImageRef)
	}
}

func TestAServerlessAppsWorkersAreRefusedWhenNoFunctionCarriesTheWorkerEntry(t *testing.T) {
	t.Parallel()

	app := &provider.AppSpec{App: "web", Functions: []provider.FunctionSpec{{Name: "fn--web--a"}}}
	images := provider.ImagePushes{Pushes: []provider.ImagePush{{App: "fn--web--a", ImageRef: "europe-west1-docker.pkg.dev/acme/ocel/web-a@sha256:aaa", Function: true}}}
	if got, err := workerImageOf(app, images); err == nil || !strings.Contains(err.Error(), "web") {
		t.Errorf("workerImageOf = %s, %v, want a refusal naming the app", got, err)
	}
}
