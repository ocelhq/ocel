package gcp

import (
	"context"
	"slices"
	"testing"
)

func TestCloudRunShipsNoNativeModuleToANextContainer(t *testing.T) {
	files, err := (&Provider{}).ReadNextServerRuntime(context.Background())
	if err != nil {
		t.Fatalf("ReadNextServerRuntime() = %v", err)
	}
	allowed := []string{"server-adapter.mjs", "cache-handler.cjs", "use-cache-default.cjs", "use-cache-remote.cjs"}
	for name := range files {
		if !slices.Contains(allowed, name) {
			t.Errorf("ReadNextServerRuntime() ships %s, and next start loads only %v", name, allowed)
		}
	}
	for _, name := range allowed {
		if len(files[name]) == 0 {
			t.Errorf("ReadNextServerRuntime() ships no %s", name)
		}
	}
	delete(files, "server-adapter.mjs")
	again, _ := (&Provider{}).ReadNextServerRuntime(context.Background())
	if len(again["server-adapter.mjs"]) == 0 {
		t.Error("ReadNextServerRuntime() hands out the map it keeps, and a caller's change would reach every later image")
	}
}
