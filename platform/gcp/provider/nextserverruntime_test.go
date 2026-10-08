package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
)

func TestCloudRunShipsNoNativeModuleToANextContainer(t *testing.T) {
	files, err := (&Provider{}).ReadNextServerRuntime(context.Background())
	if err != nil {
		t.Fatalf("ReadNextServerRuntime() = %v", err)
	}
	allowed := []string{containerimage.NextServerPreloadFile}
	for name := range files {
		if !slices.Contains(allowed, name) {
			t.Errorf("ReadNextServerRuntime() ships %s, and a Next container runs only %v", name, allowed)
		}
	}
	for _, name := range allowed {
		if len(files[name]) == 0 {
			t.Errorf("ReadNextServerRuntime() ships no %s", name)
		}
	}
	delete(files, containerimage.NextServerPreloadFile)
	again, _ := (&Provider{}).ReadNextServerRuntime(context.Background())
	if len(again[containerimage.NextServerPreloadFile]) == 0 {
		t.Error("ReadNextServerRuntime() hands out the map it keeps, and a caller's change would reach every later image")
	}
}
